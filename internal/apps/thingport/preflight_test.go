package thingport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/thingport"
	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/service"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

const zeroPUIDHint = "Thingport's official backend cannot use container PUID 0, including self-host's rootless defaults. Use a rootful Docker Project with a nonzero PUID and correctly owned Thingport storage, or wait for a supported rootless configuration. Do not change existing Project IDs or recursively chown data to bypass this hold."

type serviceHarness struct {
	svc    *service.Service
	p      *project.Project
	runner *systemtest.Runner
	fake   *fakeThingport
	opts   service.InstallOptions
	port   int
}

func newServiceHarness(t *testing.T) *serviceHarness {
	t.Helper()
	fake := &fakeThingport{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	deps := systemtest.NewDeps(t)
	deps.HTTP = server.Client()
	runner := deps.Runner.(*systemtest.Runner)
	runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if strings.Contains(systemtest.Line(cmd), " ps ") {
			return system.Result{Stdout: []byte(`[{"Service":"thingport","State":"running"},{"Service":"thingport-backend","State":"running","Health":"healthy"},{"Service":"thingport-database","State":"running","Health":"healthy"}]`)}
		}
		return system.Result{}
	})
	svc := service.New(deps, events.Discard(), catalog.New([]app.App{thingport.App{}}, nil))
	p, err := svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"media", "torrents"} {
		if err := os.MkdirAll(filepath.Join(p.Owner.Home, "data", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	port, err := strconv.Atoi(clitest.Port(server))
	if err != nil {
		t.Fatal(err)
	}
	return &serviceHarness{
		svc: svc, p: p, runner: runner, fake: fake, port: port,
		opts: service.InstallOptions{
			Targets: []string{"thingport"}, Sets: []string{"thingport.email=owner@example.test", "thingport.port=" + clitest.Port(server)},
			AdminUser: "admin", AdminPassword: "synthetic-password",
		},
	}
}

func requireZeroPUIDManual(t *testing.T, err error) {
	t.Helper()
	var manual *helpers.ManualError
	if !errors.Is(err, helpers.ErrManual) || !errors.As(err, &manual) || manual.Hint != zeroPUIDHint {
		t.Fatalf("expected the fixed UID-zero Manual hint, got %v", err)
	}
}

func countCommand(calls []string, verb string) int {
	count := 0
	for _, call := range calls {
		if strings.Contains(call, " "+verb+" ") {
			count++
		}
	}
	return count
}

func TestPreflightRequiresValidPublicPUIDWithoutMutations(t *testing.T) {
	for _, uid := range []string{"", "-1", "not-a-uid", "1000\n", "4294967296", "000", "1000"} {
		t.Run(strconv.Quote(uid), func(t *testing.T) {
			settings := map[string]string{"PUID": uid, "PGID": "1000"}
			before := maps.Clone(settings)
			saved := ownerSecrets()
			original := maps.Clone(saved)
			h := helpers.New(helpers.Config{Settings: settings, Secrets: saved, Log: events.Discard()})
			err := (thingport.App{}).Preflight(t.Context(), h)
			if uid == "1000" {
				if err != nil {
					t.Fatal(err)
				}
			} else if uid == "000" {
				requireZeroPUIDManual(t, err)
			} else if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("invalid public PUID passed admission: %v", err)
			}
			if !maps.Equal(settings, before) || !maps.Equal(saved, original) {
				t.Fatal("read-only Preflight changed settings or credentials")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (thingport.App{}).Preflight(ctx, helpers.New(helpers.Config{Log: events.Discard()})); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight = %v", err)
	}
}

func TestServiceInstallRejectsZeroPUIDBeforeStartup(t *testing.T) {
	for _, mode := range []string{"rootless defaults", "rootful explicit zero"} {
		t.Run(mode, func(t *testing.T) {
			h := newServiceHarness(t)
			if mode == "rootless defaults" {
				h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			} else {
				h.opts.Settings = map[string]string{"PUID": "0"}
			}
			for range 2 {
				requireZeroPUIDManual(t, h.svc.Install(t.Context(), h.opts))
			}
			if h.p.Exists() || len(h.fake.requests) != 0 {
				t.Fatal("unsupported Install initialized a Project or contacted Thingport")
			}
			for _, call := range h.runner.Calls() {
				if strings.HasPrefix(call, "docker compose ") {
					t.Fatalf("unsupported Install reached Compose: %s", call)
				}
			}
			if _, err := os.Stat(h.p.Root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsupported Install created Project files or secrets")
			}
		})
	}
}

func TestServiceInstallPreparedDefaultsAndRetryPreserveIdentity(t *testing.T) {
	h := newServiceHarness(t)
	if err := h.svc.Install(t.Context(), h.opts); err != nil {
		t.Fatal(err)
	}
	settings, err := h.p.LoadSettings()
	if err != nil || settings["PUID"] != "1000" || settings["PGID"] != "1000" || settings["MEDIA_ROOT"] != filepath.Join(h.p.Owner.Home, "data", "media") {
		t.Fatal("Install did not retain the supported detected defaults")
	}
	saved, err := h.p.LoadSecrets()
	if err != nil || saved["THINGPORT_OWNER_ID"] != "fixture-owner" || saved["THINGPORT_AUTH_SECRET"] == "" || saved["THINGPORT_DB_PASSWORD"] == "" {
		t.Fatal("supported Install did not reach verified-owner Setup")
	}
	saved["THINGPORT_HOMARR_API_KEY"] = "tp_synthetic-existing-key"
	if err := h.p.SaveSecrets(saved); err != nil {
		t.Fatal(err)
	}
	h.opts.Settings = map[string]string{"PUID": "0"}
	if err := h.svc.Install(t.Context(), h.opts); err != nil {
		t.Fatalf("retry used ignored new-Project settings or lost early public context: %v", err)
	}
	after, err := h.p.LoadSecrets()
	if err != nil || !maps.Equal(saved, after) || countCommand(h.runner.Calls(), "up") != 2 {
		t.Fatal("supported retry failed to start or changed credential identity")
	}
	for _, request := range h.fake.requests {
		if request != "GET /api/health" && request != "POST /api/login" && request != "GET /api/admin/users" {
			t.Fatalf("unexpected token/provider request: %s", request)
		}
	}
}

func TestServiceExistingZeroPUIDCannotRestartOrBypassInstall(t *testing.T) {
	for _, operation := range []string{"install", "start", "restart", "update"} {
		t.Run(operation, func(t *testing.T) {
			h := newServiceHarness(t)
			if err := h.svc.Install(t.Context(), h.opts); err != nil {
				t.Fatal(err)
			}
			settings, err := h.p.LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			settings["PUID"] = "0"
			if err := h.p.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			before := len(h.runner.Calls())
			switch operation {
			case "install":
				h.opts.Settings = map[string]string{"PUID": "1000"}
				err = h.svc.Install(t.Context(), h.opts)
			case "start":
				err = h.svc.Start(t.Context(), []string{"thingport"})
			case "restart":
				err = h.svc.Restart(t.Context(), []string{"thingport"})
			case "update":
				err = h.svc.Update(t.Context(), []string{"thingport"})
			}
			requireZeroPUIDManual(t, err)
			calls := h.runner.Calls()[before:]
			if countCommand(calls, "up") != 0 || countCommand(calls, "restart") != 0 || countCommand(calls, "pull") != 0 {
				t.Fatal("unsupported existing Project reached startup or pulling")
			}
		})
	}
}

func thingportArchive(t *testing.T, h *serviceHarness, uid string) string {
	t.Helper()
	saved := map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password",
		"THINGPORT_AUTH_SECRET": "synthetic-signing-secret", "THINGPORT_DB_PASSWORD": "synthetic-database-password",
		"THINGPORT_OWNER_ID": "fixture-owner", "THINGPORT_HOMARR_API_KEY": "tp_synthetic-existing-key",
	}
	if h.p.Exists() {
		var err error
		saved, err = h.p.LoadSecrets()
		if err != nil {
			t.Fatal(err)
		}
	}
	env := "TZ=UTC\nPUID=" + uid + "\nPGID=1000\nUMASK=022\nEXPOSURE=local\nBIND_IP=127.0.0.1\nLAN_IP=127.0.0.1\n"
	for key, value := range saved {
		env += key + "=" + value + "\n"
	}
	state, err := json.Marshal(project.State{Version: 1, Apps: map[string]*project.AppState{
		"thingport": {Port: h.port, Inputs: map[string]string{"email": "owner@example.test"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "thingport.tar.zst")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	w, err := backup.Create(f, "", backup.Manifest{Version: 1, Apps: []backup.ManifestApp{{ID: "thingport", Managed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		".env": []byte(env), "state.json": state,
		"apps/thingport/compose.yml": []byte((thingport.App{}).Compose(app.RenderContext{Port: h.port})),
		"apps/thingport/storage/db":  []byte("archived-storage"),
		"apps/thingport/database/db": []byte("archived-database"),
	} {
		if err := w.AddBytes(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestServiceRestoreFreshUsesArchivedPUID(t *testing.T) {
	for _, uid := range []string{"0", "1000"} {
		t.Run(uid, func(t *testing.T) {
			h := newServiceHarness(t)
			h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			err := h.svc.Restore(t.Context(), thingportArchive(t, h, uid), nil, "")
			if uid == "0" {
				requireZeroPUIDManual(t, err)
				if h.p.Exists() || len(h.runner.Calls()) != 0 || len(h.fake.requests) != 0 {
					t.Fatal("unsupported fresh Restore initialized or reached startup")
				}
				pending, err := h.p.LoadRestoreStartup()
				if err != nil || pending != nil {
					t.Fatal("unsupported Restore registered startup/rollback intent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			settings, err := h.p.LoadSettings()
			if err != nil || settings["PUID"] != uid || countCommand(h.runner.Calls(), "up") != 1 {
				t.Fatal("fresh Restore redetected defaults instead of using archived public settings")
			}
			saved, err := h.p.LoadSecrets()
			if err != nil || saved["THINGPORT_OWNER_ID"] != "fixture-owner" || saved["THINGPORT_HOMARR_API_KEY"] != "tp_synthetic-existing-key" {
				t.Fatal("supported fresh Restore lost credential identity")
			}
		})
	}
}

func TestServiceRestoreDestinationPUIDBeforeRemovalAndRollback(t *testing.T) {
	for _, whole := range []bool{false, true} {
		for _, mode := range []string{"unsupported", "supported", "rollback control"} {
			t.Run(fmt.Sprintf("whole=%t/%s", whole, mode), func(t *testing.T) {
				h := newServiceHarness(t)
				if err := h.svc.Install(t.Context(), h.opts); err != nil {
					t.Fatal(err)
				}
				saved, err := h.p.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				saved["THINGPORT_HOMARR_API_KEY"] = "tp_synthetic-existing-key"
				if err := h.p.SaveSecrets(saved); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"storage/db", "database/db"} {
					if err := h.p.WriteAppFile("thingport", name, []byte("keep-original-data"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				settings, err := h.p.LoadSettings()
				if err != nil {
					t.Fatal(err)
				}
				archiveUID := "0"
				if mode == "unsupported" {
					settings["PUID"] = "0"
					if err := h.p.SaveSettings(settings); err != nil {
						t.Fatal(err)
					}
					archiveUID = "1000"
				}
				archive := thingportArchive(t, h, archiveUID)
				originalCompose, err := h.p.ReadCompose()
				if err != nil {
					t.Fatal(err)
				}
				originalState, err := h.p.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				originalSecrets, err := h.p.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				removals := 0
				if mode != "supported" {
					h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
						line := systemtest.Line(cmd)
						if strings.Contains(line, " ps ") {
							return system.Result{Stdout: []byte(`[{"Service":"thingport","State":"running"}]`)}
						}
						if strings.Contains(line, " rm ") {
							removals++
							return system.Result{ExitCode: 1}
						}
						return system.Result{}
					})
				}
				ids := []string{"thingport"}
				if whole {
					ids = nil
				}
				before := len(h.runner.Calls())
				err = h.svc.Restore(t.Context(), archive, ids, "")
				calls := h.runner.Calls()[before:]
				switch mode {
				case "unsupported":
					requireZeroPUIDManual(t, err)
					if removals != 0 || len(calls) != 0 {
						t.Fatal("unsupported Restore reached inspection, destruction or rollback Up")
					}
				case "rollback control":
					if err == nil || errors.Is(err, helpers.ErrManual) || removals != 1 || countCommand(calls, "up") != 1 {
						t.Fatalf("armed removal failure did not exercise rollback startup: %v", err)
					}
				case "supported":
					if err != nil || countCommand(calls, "rm") != 1 || countCommand(calls, "up") != 1 {
						t.Fatalf("destination nonzero PUID was not admitted: %v", err)
					}
				}
				afterSettings, err := h.p.LoadSettings()
				if err != nil || !maps.Equal(settings, afterSettings) {
					t.Fatal("Restore replaced destination public settings with archive settings")
				}
				afterSecrets, err := h.p.LoadSecrets()
				if err != nil || !maps.Equal(originalSecrets, afterSecrets) {
					t.Fatal("Restore changed credential/token identity")
				}
				if mode == "supported" {
					for _, name := range []string{"storage", "database"} {
						data, err := os.ReadFile(h.p.Path("apps", "thingport", name, "db"))
						if err != nil || string(data) != "archived-"+name {
							t.Fatal("supported Restore did not replace the App data")
						}
					}
					return
				}
				afterCompose, err := h.p.ReadCompose()
				if err != nil || string(originalCompose) != string(afterCompose) {
					t.Fatal("refused or rolled-back Restore changed Compose")
				}
				afterState, err := h.p.LoadState()
				if err != nil || !reflect.DeepEqual(originalState, afterState) {
					t.Fatal("refused or rolled-back Restore changed App records")
				}
				for _, name := range []string{"storage/db", "database/db"} {
					data, err := os.ReadFile(h.p.Path("apps", "thingport", name))
					if err != nil || string(data) != "keep-original-data" {
						t.Fatal("refused or rolled-back Restore replaced original data")
					}
				}
				if mode == "unsupported" {
					pending, err := h.p.LoadRestoreStartup()
					if err != nil || pending != nil {
						t.Fatal("refused Restore registered startup/rollback intent")
					}
				}
			})
		}
	}
}
