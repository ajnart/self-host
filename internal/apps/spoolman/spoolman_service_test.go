package spoolman_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/spoolman"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/service"
	"github.com/ajnart/self-host/internal/systemtest"
)

const spoolmanOriginalPassword = "synthetic-original-spoolman-database-password"

type spoolmanRefreshApp struct{ spoolman.App }

func (spoolmanRefreshApp) Compose(rc app.RenderContext) string {
	return strings.Replace((spoolman.App{}).Compose(rc), "ghcr.io/donkie/spoolman:latest", "ghcr.io/donkie/spoolman:next", 1)
}

func TestSpoolmanServiceInstallHoldsMissingDatabaseCredential(t *testing.T) {
	for _, layout := range []string{"absent", "empty database", "retained database"} {
		t.Run(layout, func(t *testing.T) {
			var requests []string
			server := spoolmanServer(t, nil, &requests)
			deps := systemtest.NewDeps(t)
			svc := service.New(deps, events.Discard(), catalog.New([]app.App{spoolman.App{}}, nil))
			p, err := svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			data := p.Path("apps", "spoolman", "database", "18", "docker", "inventory-fixture")
			if layout != "absent" {
				if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if layout == "retained database" {
				if err := os.WriteFile(data, []byte("kept-inventory-history"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = svc.Install(t.Context(), service.InstallOptions{
				Targets: []string{"spoolman"}, Exposure: service.ExposureLocal,
				Sets: []string{"spoolman.port=" + clitest.Port(server)},
			})
			if !errors.Is(err, helpers.ErrManual) {
				t.Errorf("missing database credential did not remain Manual: %v", err)
			}
			values, err := p.LoadSecrets()
			if err != nil || values["SPOOLMAN_DB_PASSWORD"] != "" {
				t.Error("Install persisted a replacement database password")
			}
			for _, call := range deps.Runner.(*systemtest.Runner).Calls() {
				if strings.Contains(call, " up ") || strings.Contains(call, " pull ") {
					t.Error("held Install pulled or started the App")
				}
			}
			if len(requests) != 0 {
				t.Error("held Install reached the running App")
			}
			if layout == "retained database" && clitest.Read(t, data) != "kept-inventory-history" {
				t.Fatal("held Install changed retained database data")
			}
		})
	}
}

func TestSpoolmanServiceCredentialLossBlocksLifecycleBeforeReplacementOrStartup(t *testing.T) {
	for _, operation := range []string{"install", "remove-reinstall", "missing App state", "missing Project state", "update", "refresh", "start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			var requests []string
			server := spoolmanServer(t, nil, &requests)
			deps := systemtest.NewDeps(t)
			svc := service.New(deps, events.Discard(), catalog.New([]app.App{spoolman.App{}}, nil))
			opts := service.InstallOptions{
				Targets: []string{"spoolman"}, Exposure: service.ExposureLocal,
				Sets:          []string{"spoolman.port=" + clitest.Port(server)},
				PrivateInputs: map[string]map[string]string{"spoolman": {"db_password": spoolmanOriginalPassword}},
			}
			if err := svc.Install(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			opts.PrivateInputs = nil
			p, err := svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			data := p.Path("apps", "spoolman", "database", "18", "docker", "inventory-fixture")
			if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(data, []byte("kept-inventory-history"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "remove-reinstall":
				if err := svc.Remove(t.Context(), []string{"spoolman"}, false); err != nil {
					t.Fatal(err)
				}
			case "missing App state":
				state, err := p.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				delete(state.Apps, "spoolman")
				if err := p.SaveState(state); err != nil {
					t.Fatal(err)
				}
			case "missing Project state":
				if err := os.Remove(p.Path("state.json")); err != nil {
					t.Fatal(err)
				}
			case "refresh":
				svc.Catalog = catalog.New([]app.App{spoolmanRefreshApp{}}, nil)
			}
			values, err := p.LoadSecrets()
			if err != nil {
				t.Fatal(err)
			}
			delete(values, "SPOOLMAN_DB_PASSWORD")
			if err := p.SaveSecrets(values); err != nil {
				t.Fatal(err)
			}
			before := clitest.Read(t, p.Path("compose.yml"))
			runner := deps.Runner.(*systemtest.Runner)
			calls, reads := len(runner.Calls()), len(requests)
			switch operation {
			case "update", "refresh":
				err = svc.Update(t.Context(), []string{"spoolman"})
			case "start":
				err = svc.Start(t.Context(), []string{"spoolman"})
			case "restart":
				err = svc.Restart(t.Context(), []string{"spoolman"})
			default:
				err = svc.Install(t.Context(), opts)
			}
			if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "--set-file spoolman.db_password=PATH") {
				t.Fatalf("credential loss did not hold with private original-password recovery: %v", err)
			}
			after, err := p.LoadSecrets()
			if err != nil || !maps.Equal(values, after) {
				t.Fatal("held lifecycle operation replaced or changed saved credentials")
			}
			for _, call := range runner.Calls()[calls:] {
				if strings.Contains(call, " up ") || strings.Contains(call, " pull ") || strings.Contains(call, " restart ") {
					t.Fatal("held lifecycle operation pulled, started or restarted the App")
				}
			}
			if len(requests) != reads || clitest.Read(t, p.Path("compose.yml")) != before || clitest.Read(t, data) != "kept-inventory-history" {
				t.Fatal("held lifecycle operation probed the App or changed its definition or database")
			}
		})
	}
}

func TestSpoolmanServiceReusesKnownCredentialAndRecoversOriginalPrivately(t *testing.T) {
	var requests []string
	server := spoolmanServer(t, nil, &requests)
	deps := systemtest.NewDeps(t)
	var generated bool
	var output strings.Builder
	log := events.New(events.SinkFunc(func(e events.Event) {
		output.WriteString(e.Msg + e.Error + e.Hint + e.Command)
		if e.App == "spoolman" && e.Step == "secret" {
			generated = true
		}
	}))
	svc := service.New(deps, log, catalog.New([]app.App{spoolman.App{}}, nil))
	opts := service.InstallOptions{
		Targets: []string{"spoolman"}, Exposure: service.ExposureLocal,
		Sets:          []string{"spoolman.port=" + clitest.Port(server)},
		PrivateInputs: map[string]map[string]string{"spoolman": {"db_password": spoolmanOriginalPassword}},
	}
	if err := svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	opts.PrivateInputs = nil
	p, err := svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.WriteAppFile("spoolman", "database/18/docker/inventory-fixture", []byte("kept-inventory-history"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := svc.Install(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
		if err := svc.Remove(t.Context(), []string{"spoolman"}, false); err != nil {
			t.Fatal(err)
		}
		if err := svc.Install(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
	}
	values, err := p.LoadSecrets()
	if err != nil || values["SPOOLMAN_DB_PASSWORD"] != spoolmanOriginalPassword {
		t.Fatal("Install or remove-reinstall failed to reuse the known original password")
	}
	delete(values, "SPOOLMAN_DB_PASSWORD")
	if err := p.SaveSecrets(values); err != nil {
		t.Fatal(err)
	}
	if err := svc.Install(t.Context(), opts); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("lost password must hold until the original is recovered: %v", err)
	}
	deps.In = strings.NewReader(spoolmanOriginalPassword + "\n")
	opts.SetFiles = []string{"spoolman.db_password=-"}
	if err := svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	svc.Catalog = catalog.New([]app.App{spoolmanRefreshApp{}}, nil)
	if err := svc.Update(t.Context(), []string{"spoolman"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clitest.Read(t, p.Path("compose.yml")), "ghcr.io/donkie/spoolman:next") || clitest.Read(t, p.Path("apps", "spoolman", "database", "18", "docker", "inventory-fixture")) != "kept-inventory-history" {
		t.Fatal("private recovery did not permit the reviewed refresh while preserving database data")
	}
	values, err = p.LoadSecrets()
	if err != nil || values["SPOOLMAN_DB_PASSWORD"] != spoolmanOriginalPassword || generated {
		t.Fatal("private recovery or refresh generated or replaced the original password")
	}
	state, err := p.LoadState()
	if err != nil || state.Apps["spoolman"] == nil || state.Apps["spoolman"].Inputs["db_password"] != "" {
		t.Fatal("the recovered password escaped into public App inputs")
	}
	if strings.Contains(output.String(), spoolmanOriginalPassword) || strings.Contains(clitest.Read(t, p.Path("state.json")), spoolmanOriginalPassword) || strings.Contains(clitest.Read(t, p.Path("compose.yml")), spoolmanOriginalPassword) {
		t.Fatal("private recovery exposed the original password in events, state or Compose")
	}
}
