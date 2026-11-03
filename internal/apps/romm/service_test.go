package romm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/romm"
	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/service"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type serviceHarness struct {
	svc    *service.Service
	deps   *system.Deps
	runner *systemtest.Runner
	p      *project.Project
	fake   *fakeRomM
	port   int
}

func newServiceHarness(t *testing.T, fake *fakeRomM) *serviceHarness {
	t.Helper()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	owner := system.User{Name: "romm-test", Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	deps := systemtest.NewDeps(t, systemtest.WithAccounts(systemtest.NewAccounts(owner)))
	svc := service.New(deps, events.Discard(), catalog.New([]app.App{romm.App{}}, nil))
	p, err := svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	return &serviceHarness{svc: svc, deps: deps, runner: deps.Runner.(*systemtest.Runner), p: p, fake: fake, port: server.Listener.Addr().(*net.TCPAddr).Port}
}

func (h *serviceHarness) options() service.InstallOptions {
	return service.InstallOptions{Targets: []string{"romm"}, Sets: []string{fmt.Sprintf("romm.port=%d", h.port)}, Exposure: service.ExposureLocal, AdminUser: "admin", AdminPassword: "synthetic-admin-password"}
}

func prepareServiceLibrary(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "romm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "romm", "preserve"), []byte("external-library-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireServiceLibraryPreserved(t *testing.T, root string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, "romm"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("consumer changed the prepared external library directory")
	}
	data, err := os.ReadFile(filepath.Join(root, "romm", "preserve"))
	if err != nil || string(data) != "external-library-sentinel" {
		t.Fatal("consumer changed external library content")
	}
}

func requireServiceInstalled(t *testing.T, h *serviceHarness, root, layout string) {
	t.Helper()
	settings, err := h.p.LoadSettings()
	if err != nil || settings["MEDIA_ROOT"] != root || settings["MEDIA_LAYOUT"] != layout {
		t.Fatal("Install did not persist the effective approved media settings")
	}
	state, err := h.p.LoadState()
	if err != nil || state.Apps["romm"] == nil || state.Apps["romm"].Setup != stepstatus.SetupOK || state.Apps["romm"].Port != h.port {
		t.Fatal("real RomM Setup did not complete through Service.Install")
	}
	fragment, err := h.p.ReadFragment("romm")
	if err != nil || !strings.Contains(string(fragment), "source: ${MEDIA_ROOT}/romm") || !strings.Contains(string(fragment), "create_host_path: false") {
		t.Fatal("installed RomM lost its guarded external media bind")
	}
	sec, err := h.p.LoadSecrets()
	if err != nil || len(sec["ROMM_AUTH_SECRET"]) != 64 || len(sec["ROMM_DB_PASSWORD"]) != 48 || len(sec["ROMM_DB_ROOT_PASSWORD"]) != 48 {
		t.Fatal("real RomM first-start credentials were not persisted")
	}
	requireServiceStarted(t, h)
	requireServiceLibraryPreserved(t, root)
}

func requireServiceStarted(t *testing.T, h *serviceHarness) {
	t.Helper()
	for _, call := range h.runner.Calls() {
		if strings.Contains(call, " up -d ") && strings.Contains(call+" ", " romm ") && strings.Contains(call+" ", " romm-db ") {
			return
		}
	}
	t.Fatal("consumer did not reach startup for both RomM services through the fake runner")
}

func TestServiceInstallRomMPreparedLibrary(t *testing.T) {
	for _, mode := range []string{"default shared", "data root", "legacy media root", "rootless"} {
		t.Run(mode, func(t *testing.T) {
			h := newServiceHarness(t, &fakeRomM{})
			opts := h.options()
			root, layout := filepath.Join(h.p.Owner.Home, "data", "media"), "shared"
			switch mode {
			case "data root":
				opts.DataRoot = filepath.Join(t.TempDir(), "chosen-data")
				root = filepath.Join(opts.DataRoot, "media")
			case "legacy media root":
				opts.MediaRoot, opts.DownloadsRoot = t.TempDir(), t.TempDir()
				root, layout = opts.MediaRoot, "legacy"
			case "rootless":
				h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			}
			prepareServiceLibrary(t, root)
			if err := h.svc.Install(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			requireServiceInstalled(t, h, root, layout)
			if mode == "rootless" {
				settings, err := h.p.LoadSettings()
				if err != nil || settings["PUID"] != "0" || settings["PGID"] != "0" {
					t.Fatal("rootless consumer lost its effective container identities")
				}
			}
			h.fake.mu.Lock()
			defer h.fake.mu.Unlock()
			if h.fake.posts != 1 || h.fake.creates != 1 {
				t.Fatal("real consumer did not create exactly one CSRF-protected first administrator")
			}
		})
	}
}

func TestServiceInstallRomMMissingLibraryRecovers(t *testing.T) {
	for _, mode := range []string{"missing", "file", "wrong root"} {
		t.Run(mode, func(t *testing.T) {
			h := newServiceHarness(t, &fakeRomM{})
			opts := h.options()
			opts.MediaRoot = t.TempDir()
			path := filepath.Join(opts.MediaRoot, "romm")
			switch mode {
			case "file":
				if err := os.WriteFile(path, []byte("not-a-directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong root":
				prepareServiceLibrary(t, filepath.Join(h.p.Owner.Home, "data", "media"))
			}
			requireManual(t, h.svc.Install(t.Context(), opts))
			if _, err := os.Stat(h.p.Root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused Install created Project files or credentials")
			}
			for _, call := range h.runner.Calls() {
				if strings.HasPrefix(call, "docker compose ") {
					t.Fatal("missing-library admission reached Compose")
				}
			}
			if h.fake.requests != 0 {
				t.Fatal("missing-library admission reached the RomM API")
			}
			if mode == "file" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			prepareServiceLibrary(t, opts.MediaRoot)
			if err := h.svc.Install(t.Context(), opts); err != nil {
				t.Fatalf("preparing the effective external library did not recover Install: %v", err)
			}
			requireServiceInstalled(t, h, opts.MediaRoot, "legacy")
		})
	}
}

func seedServiceProject(t *testing.T, h *serviceHarness, root string, withRomM bool) {
	t.Helper()
	settings := map[string]string{"TZ": "UTC", "PUID": strconv.Itoa(h.p.Owner.UID), "PGID": strconv.Itoa(h.p.Owner.GID), "UMASK": "022", "EXPOSURE": "local", "BIND_IP": "127.0.0.1", "LAN_IP": "127.0.0.1", "MEDIA_ROOT": root, "DOWNLOADS_ROOT": t.TempDir()}
	if err := h.p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := h.p.SaveSecrets(map[string]string{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-admin-password", "ROMM_AUTH_SECRET": strings.Repeat("a", 64), "ROMM_DB_PASSWORD": strings.Repeat("b", 48), "ROMM_DB_ROOT_PASSWORD": strings.Repeat("c", 48), "SURVIVOR_KEY": "synthetic-unrelated-key"}); err != nil {
		t.Fatal(err)
	}
	state := &project.State{Version: 1, Apps: map[string]*project.AppState{}}
	compose := "include:\n  - extra/survivor/compose.yml\n"
	if withRomM {
		state.Apps["romm"] = &project.AppState{Port: h.port}
		compose += "  - apps/romm/compose.yml\n"
		if err := h.p.WriteAppFile("romm", "compose.yml", []byte((romm.App{}).Compose(app.RenderContext{Port: h.port})), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := h.p.WriteAppFile("romm", "database/sentinel", []byte("current-database"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"extra/survivor/compose.yml": "services:\n  survivor:\n    image: example/survivor:latest\n", "extra/survivor/config/db": "current-unrelated-data", "compose.yml": compose} {
		if err := h.p.WriteHomeFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.p.SaveState(state); err != nil {
		t.Fatal(err)
	}
}

type serviceFile struct {
	Data string
	Mode os.FileMode
}

func serviceFiles(t *testing.T, p *project.Project) map[string]serviceFile {
	t.Helper()
	files := map[string]serviceFile{}
	for _, name := range []string{".env", "secrets.env", "state.json", "state.json.corrupt", "compose.yml", "compose.legacy.yml", "apps/romm/compose.yml", "apps/romm/database/sentinel", "extra/survivor/compose.yml", "extra/survivor/config/db", project.NamesFolder + "/runtime.json", "restore-startup.json"} {
		data, err := os.ReadFile(p.Path(name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p.Path(name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = serviceFile{Data: string(data), Mode: info.Mode()}
	}
	return files
}

func TestServiceInstallRomMExistingSettings(t *testing.T) {
	h := newServiceHarness(t, &fakeRomM{id: 37, user: "admin", password: "synthetic-admin-password"})
	root, ignored := t.TempDir(), t.TempDir()
	seedServiceProject(t, h, root, false)
	prepareServiceLibrary(t, ignored)
	before := serviceFiles(t, h.p)
	opts := h.options()
	opts.MediaRoot, opts.DownloadsRoot, opts.DataRoot = ignored, ignored, ignored
	opts.EasyNames, opts.SeparateMediaUsers, opts.MediaCopyFallback = true, true, true
	opts.Settings = map[string]string{"MEDIA_ROOT": ignored, "MEDIA_LAYOUT": "shared"}
	requireManual(t, h.svc.Install(t.Context(), opts))
	if !reflect.DeepEqual(before, serviceFiles(t, h.p)) || len(h.runner.Calls()) != 0 || h.fake.requests != 0 {
		t.Fatal("ignored prepared root bypassed the current Project's missing library")
	}
	prepareServiceLibrary(t, root)
	h.deps.LANIP = func() (string, error) { t.Fatal("existing Install redetected Host settings"); return "", nil }
	h.deps.TimeZone = func() string { t.Fatal("existing Install redetected Host settings"); return "" }
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	requireServiceInstalled(t, h, root, "")
	after := serviceFiles(t, h.p)
	for _, name := range []string{".env", "extra/survivor/config/db"} {
		if before[name] != after[name] {
			t.Fatal("existing Install replaced saved credentials, Host settings or unrelated data")
		}
	}
	if h.fake.posts != 0 {
		t.Fatal("existing administrator was recreated")
	}
}

func serviceArchive(t *testing.T, h *serviceHarness, root string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "romm.tar.zst")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	w, err := backup.Create(f, "", backup.Manifest{Version: 1, Apps: []backup.ManifestApp{{ID: "romm", Managed: true}, {ID: "survivor", Extra: true}}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(project.State{Version: 1, Apps: map[string]*project.AppState{"romm": {Port: h.port}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		".env":                        fmt.Sprintf("TZ=UTC\nPUID=%d\nPGID=%d\nUMASK=022\nEXPOSURE=local\nBIND_IP=127.0.0.1\nLAN_IP=127.0.0.1\nMEDIA_ROOT=%s\nADMIN_USER=admin\nADMIN_PASSWORD=synthetic-admin-password\nROMM_AUTH_SECRET=%s\nROMM_DB_PASSWORD=%s\nROMM_DB_ROOT_PASSWORD=%s\n", h.p.Owner.UID, h.p.Owner.GID, root, strings.Repeat("d", 64), strings.Repeat("e", 48), strings.Repeat("f", 48)),
		"state.json":                  string(state),
		"apps/romm/compose.yml":       (romm.App{}).Compose(app.RenderContext{Port: h.port}),
		"apps/romm/database/sentinel": "archived-database",
		"extra/survivor/compose.yml":  "services:\n  survivor:\n    image: example/survivor:latest\n",
		"extra/survivor/config/db":    "archived-unrelated-data",
	} {
		if err := w.AddBytes(name, []byte(data), 0o600); err != nil {
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

func TestServiceRestoreRomMFreshArchive(t *testing.T) {
	h := newServiceHarness(t, &fakeRomM{})
	prepareServiceLibrary(t, filepath.Join(h.p.Owner.Home, "data", "media"))
	root := t.TempDir()
	archive := serviceArchive(t, h, root)
	requireManual(t, h.svc.Restore(t.Context(), archive, nil, ""))
	if h.p.Exists() || len(h.runner.Calls()) != 0 || len(serviceFiles(t, h.p)) != 0 {
		t.Fatal("fresh Restore ignored the archived library prerequisite or initialized live records")
	}
	prepareServiceLibrary(t, root)
	if err := h.svc.Restore(t.Context(), archive, nil, ""); err != nil {
		t.Fatalf("prepared archived library did not recover fresh Restore: %v", err)
	}
	settings, err := h.p.LoadSettings()
	if err != nil || settings["MEDIA_ROOT"] != root || settings["MEDIA_LAYOUT"] != "" {
		t.Fatal("fresh Restore lost archived legacy Host settings")
	}
	files := serviceFiles(t, h.p)
	if files["apps/romm/database/sentinel"].Data != "archived-database" || files["extra/survivor/config/db"].Data != "archived-unrelated-data" || files["restore-startup.json"].Data != "" {
		t.Fatal("fresh Restore lost archived data or retained startup intent")
	}
	if h.fake.requests != 0 {
		t.Fatal("Restore bootstrapped an administrator")
	}
	requireServiceStarted(t, h)
	requireServiceLibraryPreserved(t, root)
}

func TestServiceRestoreRomMCurrentDestination(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprintf("whole=%t", whole), func(t *testing.T) {
			h := newServiceHarness(t, &fakeRomM{})
			root, archived := t.TempDir(), t.TempDir()
			seedServiceProject(t, h, root, true)
			prepareServiceLibrary(t, archived)
			archive := serviceArchive(t, h, archived)
			ids := []string{"romm"}
			if whole {
				ids = nil
			}
			before := serviceFiles(t, h.p)
			requireManual(t, h.svc.Restore(t.Context(), archive, ids, ""))
			if !reflect.DeepEqual(before, serviceFiles(t, h.p)) || len(h.runner.Calls()) != 0 {
				t.Fatal("prepared archive root bypassed the current destination library prerequisite")
			}
			if err := os.Remove(filepath.Join(archived, "romm", "preserve")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(archived, "romm")); err != nil {
				t.Fatal(err)
			}
			prepareServiceLibrary(t, root)
			h.runner.On("docker compose --project-directory", system.Result{Stdout: []byte(`[{"Service":"romm","State":"running"},{"Service":"romm-db","State":"running"},{"Service":"survivor","State":"running"}]`)})
			if err := h.svc.Restore(t.Context(), archive, ids, ""); err != nil {
				t.Fatal(err)
			}
			settings, err := h.p.LoadSettings()
			if err != nil || settings["MEDIA_ROOT"] != root || settings["MEDIA_LAYOUT"] != "" {
				t.Fatal("existing Restore replaced current Host settings")
			}
			after := serviceFiles(t, h.p)
			if after["apps/romm/database/sentinel"].Data != "archived-database" || after["restore-startup.json"].Data != "" {
				t.Fatal("existing Restore lost selected data or retained startup intent")
			}
			expected := "current-unrelated-data"
			if whole {
				expected = "archived-unrelated-data"
			}
			if after["extra/survivor/config/db"].Data != expected || h.fake.requests != 0 {
				t.Fatal("Restore changed the wrong App or called account Setup")
			}
			requireServiceStarted(t, h)
			requireServiceLibraryPreserved(t, root)
		})
	}
}

type serviceLockedContext struct {
	context.Context
	p     *project.Project
	once  bool
	apply func()
}

func (c *serviceLockedContext) Err() error {
	if !c.once {
		claim, err := os.ReadFile(c.p.Path(".lock"))
		if err == nil && strings.HasPrefix(string(claim), "flock:") {
			c.once = true
			c.apply()
		}
	}
	return c.Context.Err()
}

func TestServiceRomMLockedContextDriftPreservesProject(t *testing.T) {
	for _, operation := range []string{"install", "restore"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s existing=%t", operation, existing), func(t *testing.T) {
				h := newServiceHarness(t, &fakeRomM{})
				root := filepath.Join(h.p.Owner.Home, "data", "media")
				prepareServiceLibrary(t, root)
				if existing {
					seedServiceProject(t, h, root, true)
				}
				archive := serviceArchive(t, h, root)
				missing := filepath.Join(t.TempDir(), "changed-root")
				var before map[string]serviceFile
				var calls int
				ctx := &serviceLockedContext{Context: t.Context(), p: h.p, apply: func() {
					seedServiceProject(t, h, missing, true)
					state, err := h.p.ReadHomeFile("state.json")
					if err != nil {
						t.Fatal(err)
					}
					if err := h.p.WriteHomeFile("state.json", append(state, []byte("\ndamaged tail")...), 0o600); err != nil {
						t.Fatal(err)
					}
					for name, content := range map[string]string{"state.json.corrupt": "keep-recovery-copy", project.NamesFolder + "/runtime.json": `{"start_action":"keep-pending-action"}`} {
						if err := h.p.WriteHomeFile(name, []byte(content), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					before, calls = serviceFiles(t, h.p), len(h.runner.Calls())
				}}
				var err error
				if operation == "install" {
					opts := h.options()
					opts.AdminPassword = "synthetic-proposed-replacement"
					err = h.svc.Install(ctx, opts)
				} else {
					err = h.svc.Restore(ctx, archive, nil, "")
				}
				requireManual(t, err)
				if !ctx.once || !reflect.DeepEqual(before, serviceFiles(t, h.p)) || len(h.runner.Calls()) != calls || h.fake.requests != 0 {
					t.Fatal("locked real-RomM refusal changed credentials, records, data, Compose, startup intent or runtime")
				}
				if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("locked refusal prepared the newly selected storage")
				}
				requireServiceLibraryPreserved(t, root)
			})
		}
	}
}
