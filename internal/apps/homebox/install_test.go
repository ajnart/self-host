package homebox_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/homebox"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/service"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type homeboxInstallTransport struct{ handler http.Handler }

func (transport homeboxInstallTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

type homeboxRefreshDefinition struct {
	homebox.App
	updated bool
}

func (definition *homeboxRefreshDefinition) Compose(rc app.RenderContext) string {
	compose := definition.App.Compose(rc)
	if definition.updated {
		compose = strings.Replace(compose, "      TZ: ${TZ}", "      TZ: ${TZ}\n      HBOX_LOG_LEVEL: info", 1)
	}
	return compose
}

type homeboxInstallCounts struct {
	generated     int
	started       int
	invalidations int
}

type homeboxInstallHarness struct {
	mu         sync.Mutex
	counts     homeboxInstallCounts
	svc        *service.Service
	project    *project.Project
	definition *homeboxRefreshDefinition
	fixture    *homeboxFixture
	pepper     string
}

func newHomeboxInstallHarness(t *testing.T) *homeboxInstallHarness {
	t.Helper()
	account := systemtest.NewAccounts(system.User{Name: "fixture", Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()})
	deps := systemtest.NewDeps(t, systemtest.WithAccounts(account))
	harness := &homeboxInstallHarness{definition: &homeboxRefreshDefinition{}, fixture: newHomeboxFixture(), pepper: strings.Repeat("p", 64)}
	harness.fixture.authorize = "Bearer " + homeboxKey
	deps.HTTP = &http.Client{Transport: homeboxInstallTransport{handler: harness.fixture.handler(t)}}
	log := events.Discard()
	log.AddSink(events.SinkFunc(func(event events.Event) {
		if event.App == "homebox" && event.Step == "secret" && strings.Contains(event.Msg, "generated HOMEBOX_API_KEY_PEPPER") {
			harness.mu.Lock()
			harness.counts.generated++
			harness.mu.Unlock()
		}
	}))
	harness.svc = service.New(deps, log, catalog.New([]app.App{harness.definition}, nil))
	var err error
	harness.project, err = harness.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	deps.Runner.(*systemtest.Runner).OnFunc("docker compose --project-directory", func(command system.Cmd) system.Result {
		line := systemtest.Line(command)
		if strings.Contains(line, " up -d ") || strings.Contains(line, " restart ") {
			values, err := harness.project.LoadSecrets()
			if err != nil {
				t.Error(err)
			}
			harness.mu.Lock()
			harness.counts.started++
			if values["HOMEBOX_API_KEY_PEPPER"] != harness.pepper {
				harness.counts.invalidations++
			}
			harness.mu.Unlock()
		}
		return system.Result{}
	})
	return harness
}

func (h *homeboxInstallHarness) snapshot() homeboxInstallCounts {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts
}

func (h *homeboxInstallHarness) options() service.InstallOptions {
	return service.InstallOptions{
		Targets: []string{"homebox"}, Exposure: service.ExposureLocal, AdminUser: "admin", AdminPassword: homeboxPassword,
		PrivateInputs: map[string]map[string]string{"homebox": {"api_key": homeboxKey, "account_identity": homeboxIdentity()}},
	}
}

func (h *homeboxInstallHarness) installWithPepper(t *testing.T) {
	t.Helper()
	opts := h.options()
	opts.PrivateInputs["homebox"]["api_key_pepper"] = h.pepper
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
}

func (h *homeboxInstallHarness) losePepper(t *testing.T) {
	t.Helper()
	values, err := h.project.LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	delete(values, "HOMEBOX_API_KEY_PEPPER")
	if err := h.project.SaveSecrets(values); err != nil {
		t.Fatal(err)
	}
}

func (h *homeboxInstallHarness) retainData(t *testing.T) {
	t.Helper()
	if err := h.project.MkdirAll("apps", "homebox", "data"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"homebox.db", "homebox.db-wal", "attachment.bin"} {
		if err := os.WriteFile(h.project.Path("apps", "homebox", "data", name), []byte("synthetic-retained-inventory"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *homeboxInstallHarness) assertHeld(t *testing.T, before homeboxInstallCounts) {
	t.Helper()
	if h.snapshot() != before {
		t.Fatal("a held prerequisite generated a pepper, started Homebox or invalidated its existing key")
	}
	values, err := h.project.LoadSecrets()
	if err != nil || values["HOMEBOX_API_KEY_PEPPER"] != "" {
		t.Fatal("Install or refresh persisted a replacement pepper")
	}
	for _, name := range []string{"homebox.db", "homebox.db-wal", "attachment.bin"} {
		data, err := os.ReadFile(h.project.Path("apps", "homebox", "data", name))
		if err != nil || string(data) != "synthetic-retained-inventory" {
			t.Fatal("held recovery changed retained inventory, journal or attachment data")
		}
	}
}

func TestHomeboxProductionInstallNeverRegeneratesLostPepper(t *testing.T) {
	for _, scenario := range []string{"retained data without Project", "installed missing pepper", "non-purge reinstall", "missing state", "missing all credentials", "missing environment file"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHomeboxInstallHarness(t)
			if scenario != "retained data without Project" {
				h.installWithPepper(t)
			}
			h.retainData(t)
			if scenario == "non-purge reinstall" {
				if err := h.svc.Remove(t.Context(), []string{"homebox"}, false); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "retained data without Project" {
				h.losePepper(t)
			}
			switch scenario {
			case "missing state":
				if err := os.Remove(h.project.Path("state.json")); err != nil {
					t.Fatal(err)
				}
			case "missing all credentials":
				if err := h.project.SaveSecrets(map[string]string{}); err != nil {
					t.Fatal(err)
				}
			case "missing environment file":
				if err := os.Remove(h.project.Path(".env")); err != nil {
					t.Fatal(err)
				}
			}
			before := h.snapshot()
			err := h.svc.Install(t.Context(), h.options())
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("missing pepper did not remain an actionable Manual prerequisite: %v", err)
			}
			h.assertHeld(t, before)
		})
	}
}

func TestHomeboxProductionInitialPepperRequiresExplicitPrivateProvisioning(t *testing.T) {
	h := newHomeboxInstallHarness(t)
	if err := h.svc.Install(t.Context(), h.options()); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("a new Project incorrectly established automatic generation authority: %v", err)
	}
	if h.snapshot() != (homeboxInstallCounts{}) {
		t.Fatal("a missing initial pepper was generated or used to start Homebox")
	}
	file := filepath.Join(t.TempDir(), "synthetic-pepper")
	if err := os.WriteFile(file, []byte(h.pepper+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := h.options()
	opts.SetFiles = []string{"homebox.api_key_pepper=" + file}
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	values, err := h.project.LoadSecrets()
	if err != nil || values["HOMEBOX_API_KEY_PEPPER"] != h.pepper || values["HOMEBOX_API_KEY"] != homeboxKey {
		t.Fatal("approved private provisioning lost the known pepper or existing API key")
	}
	counts := h.snapshot()
	if counts.generated != 0 || counts.started != 1 || counts.invalidations != 0 {
		t.Fatal("explicit initial provisioning unexpectedly generated or replaced a pepper")
	}
}

func TestHomeboxProductionSavedPepperSurvivesReinstallAndStateLoss(t *testing.T) {
	for _, scenario := range []string{"rerun", "non-purge reinstall", "missing state", "empty private input"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHomeboxInstallHarness(t)
			h.installWithPepper(t)
			h.retainData(t)
			if scenario == "non-purge reinstall" {
				if err := h.svc.Remove(t.Context(), []string{"homebox"}, false); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing state" {
				if err := os.Remove(h.project.Path("state.json")); err != nil {
					t.Fatal(err)
				}
			}
			opts := h.options()
			if scenario == "empty private input" {
				opts.PrivateInputs["homebox"]["api_key_pepper"] = ""
			}
			before := h.snapshot()
			if err := h.svc.Install(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			values, err := h.project.LoadSecrets()
			if err != nil || values["HOMEBOX_API_KEY_PEPPER"] != h.pepper || values["HOMEBOX_API_KEY"] != homeboxKey {
				t.Fatal("saved pepper or credential was replaced during reinstall")
			}
			after := h.snapshot()
			if after.generated != 0 || after.invalidations != 0 || after.started != before.started+1 {
				t.Fatal("saved-pepper reuse did not start without generating or invalidating keys")
			}
		})
	}
}

func TestHomeboxProductionLostPepperRecoversThroughPrivateInput(t *testing.T) {
	h := newHomeboxInstallHarness(t)
	h.installWithPepper(t)
	h.retainData(t)
	h.losePepper(t)
	before := h.snapshot()
	if err := h.svc.Install(t.Context(), h.options()); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("lost pepper was not held before private recovery: %v", err)
	}
	h.assertHeld(t, before)
	file := filepath.Join(t.TempDir(), "synthetic-original-pepper")
	if err := os.WriteFile(file, []byte(h.pepper), 0600); err != nil {
		t.Fatal(err)
	}
	opts := h.options()
	opts.SetFiles = []string{"homebox.api_key_pepper=" + file}
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	values, err := h.project.LoadSecrets()
	if err != nil || values["HOMEBOX_API_KEY_PEPPER"] != h.pepper || values["HOMEBOX_API_KEY"] != homeboxKey {
		t.Fatal("private recovery did not restore the original pepper and existing key")
	}
	after := h.snapshot()
	if after.generated != 0 || after.invalidations != 0 || after.started != before.started+1 {
		t.Fatal("original-pepper recovery generated a replacement or invalidated keys")
	}
}

func TestHomeboxProductionMissingPepperBlocksStartAndRestart(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			h := newHomeboxInstallHarness(t)
			h.installWithPepper(t)
			h.retainData(t)
			h.losePepper(t)
			before := h.snapshot()
			var err error
			if operation == "start" {
				err = h.svc.Start(t.Context(), []string{"homebox"})
			} else {
				err = h.svc.Restart(t.Context(), []string{"homebox"})
			}
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("lifecycle startup ignored missing pepper: %v", err)
			}
			h.assertHeld(t, before)
		})
	}
}

func TestHomeboxProductionRefreshChecksPepperBeforeGenerationOrWrite(t *testing.T) {
	for _, lose := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved pepper", true: "lost pepper"}[lose], func(t *testing.T) {
			h := newHomeboxInstallHarness(t)
			h.installWithPepper(t)
			h.retainData(t)
			beforeFragment, err := h.project.ReadFragment("homebox")
			if err != nil {
				t.Fatal(err)
			}
			h.definition.updated = true
			if lose {
				h.losePepper(t)
			}
			before := h.snapshot()
			err = h.svc.Update(t.Context(), []string{"homebox"})
			afterFragment, readErr := h.project.ReadFragment("homebox")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if lose {
				if !errors.Is(err, helpers.ErrManual) || string(beforeFragment) != string(afterFragment) {
					t.Fatalf("refresh mutated a fragment before its missing-pepper hold: %v", err)
				}
				h.assertHeld(t, before)
				return
			}
			if err != nil || !strings.Contains(string(afterFragment), "HBOX_LOG_LEVEL: info") {
				t.Fatalf("saved-pepper refresh did not retain supported refresh behavior: %v", err)
			}
			after := h.snapshot()
			if after.generated != 0 || after.invalidations != 0 || after.started != before.started+1 {
				t.Fatal("refresh replaced a saved pepper or invalidated the existing key")
			}
		})
	}
}
