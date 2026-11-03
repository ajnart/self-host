package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/autobrr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type autobrrDirectoryTransport struct{ requests int }

func (tr *autobrrDirectoryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.requests++
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	body := ""
	switch req.Method + " " + req.URL.Path {
	case "GET /api/healthz/readiness":
		body = `{"status":"ready"}`
	case "POST /api/auth/login":
		response.StatusCode = http.StatusNoContent
		response.Header.Set("Set-Cookie", "autobrr_user_session=synthetic-session; Path=/")
	case "GET /api/auth/validate":
		body = `{"username":"admin","auth_method":"password"}`
	default:
		return nil, fmt.Errorf("unexpected synthetic Autobrr directory request: %s %s", req.Method, req.URL.Path)
	}
	response.Body = io.NopCloser(strings.NewReader(body))
	return response, nil
}

func newAutobrrDirectoryHarness(t *testing.T, a app.App) (*harness, *autobrrDirectoryTransport) {
	t.Helper()
	h := newDirectoryHarness(t, a)
	transport := &autobrrDirectoryTransport{}
	h.deps.HTTP = &http.Client{Transport: transport}
	return h, transport
}

type autobrrLegacyDirectoryApp struct{ autobrr.App }

func (autobrrLegacyDirectoryApp) Directories(app.RenderContext) []app.Directory { return nil }

func (a autobrrLegacyDirectoryApp) Compose(rc app.RenderContext) string {
	return strings.Replace(a.App.Compose(rc), "      - type: bind\n        source: ./config\n        target: /config\n        bind:\n          create_host_path: false", "      - ./config:/config", 1)
}

func TestAutobrrDirectoryInstallRejectsUnsupportedNativeFilesystemBeforeStart(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this regression requires an unsupported native filesystem/ACL model")
	}
	h, transport := newAutobrrDirectoryHarness(t, autobrr.App{})
	if err := h.install("autobrr"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unsupported native storage did not remain Manual: %v", err)
	}
	if e := h.rec.find("autobrr", "directories"); e == nil || e.Event != events.StepManual || !strings.Contains(e.Hint, "ACL") {
		t.Fatalf("Install did not reach the reviewed directory admission boundary: %+v", e)
	}
	if _, err := os.Lstat(filepath.Join(h.root(), "apps", "autobrr", "config")); !os.IsNotExist(err) {
		t.Fatal("unsupported native storage created a bind directory")
	}
	owner, err := h.deps.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	state, err := project.Open(owner).LoadState()
	if err != nil || state.Apps["autobrr"].Steps["directories"].Status != stepstatus.StepManual {
		t.Fatalf("directory Manual hint was not persisted: %v", err)
	}
	if err := h.svc.Start(t.Context(), []string{"autobrr"}); err == nil {
		t.Fatal("Start bypassed an Install that never admitted the generated definition")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
	if transport.requests != 0 {
		t.Fatal("unsupported directory admission reached App authentication")
	}
}

func TestAutobrrDirectoryInstallRequiresSupportedRuntimeMapping(t *testing.T) {
	for _, scenario := range []string{"platform", "remote", "userns", "rootless"} {
		t.Run(scenario, func(t *testing.T) {
			h, transport := newAutobrrDirectoryHarness(t, autobrr.App{})
			switch scenario {
			case "platform":
				h.deps.GOOS = "darwin"
			case "remote":
				systemtest.SetEnv(h.deps, map[string]string{"DOCKER_HOST": "ssh://synthetic-host.invalid"})
			case "userns":
				h.runner.On("docker info", system.Result{Stdout: []byte(`["name=userns"]`)})
			case "rootless":
				h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			}
			if err := h.install("autobrr"); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unverified runtime mapping did not hold: %v", err)
			}
			if e := h.rec.find("autobrr", "directories"); e == nil || e.Event != events.StepManual || !strings.Contains(e.Hint, "mapping") {
				t.Fatalf("mapping uncertainty was not the directory admission hold: %+v", e)
			}
			if _, err := os.Lstat(filepath.Join(h.root(), "apps", "autobrr", "config")); !os.IsNotExist(err) {
				t.Fatal("mapping uncertainty created configuration storage")
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
			if transport.requests != 0 {
				t.Fatal("mapping uncertainty authorized App authentication")
			}
		})
	}
}

func TestAutobrrDirectoryInstallPreparesOrPreservesPrivateConfigBeforeUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			h, _ := newAutobrrDirectoryHarness(t, autobrr.App{})
			dir := filepath.Join(h.root(), "apps", "autobrr", "config")
			var before os.FileInfo
			preserved := map[string]os.FileInfo{}
			if existing {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{".keep", "config.toml"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("person-owned "+name), 0o600); err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(filepath.Join(dir, name))
					if err != nil {
						t.Fatal(err)
					}
					preserved[name] = info
				}
				var err error
				before, err = os.Stat(dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			starts := 0
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if !strings.Contains(systemtest.Line(cmd), "up -d") {
					return system.Result{}
				}
				starts++
				info, err := os.Stat(dir)
				if err != nil {
					t.Errorf("Autobrr reached Up without its configuration directory: %v", err)
					return system.Result{}
				}
				stat := info.Sys().(*syscall.Stat_t)
				if info.Mode().Perm() != 0o700 || int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
					t.Error("Autobrr reached Up before private runtime-owner preparation")
				}
				data, err := os.ReadFile(filepath.Join(h.root(), "compose.yml"))
				if err != nil || !strings.Contains(string(data), "create_host_path: false") || strings.Contains(string(data), "./config:/config") {
					t.Error("generated Compose allowed implicit creation of a missing bind source")
				}
				return system.Result{}
			})
			for range 2 {
				if err := h.install("autobrr"); err != nil {
					t.Fatal(err)
				}
			}
			if starts != 2 {
				t.Fatalf("Install/rerun did not both pass preparation before Up: %d", starts)
			}
			if existing {
				after, err := os.Stat(dir)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
					t.Fatal("compatible configuration-directory metadata was changed")
				}
				for name, original := range preserved {
					path := filepath.Join(dir, name)
					data, err := os.ReadFile(path)
					if err != nil || string(data) != "person-owned "+name {
						t.Fatal("existing configuration or marker was changed or removed")
					}
					info, err := os.Stat(path)
					if err != nil || !os.SameFile(original, info) || original.Mode() != info.Mode() || original.Sys().(*syscall.Stat_t).Uid != info.Sys().(*syscall.Stat_t).Uid || original.Sys().(*syscall.Stat_t).Gid != info.Sys().(*syscall.Stat_t).Gid {
						t.Fatal("existing child file metadata was changed")
					}
				}
			}
			for _, call := range h.runner.Calls() {
				if strings.Contains(call, "chown") || strings.Contains(call, " run ") {
					t.Fatal("configuration-directory preparation used recursive repair or a helper container")
				}
			}
		})
	}
}

func TestAutobrrDirectoryInstallPreservesIncompatibleConfigBeforeUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, conflict := range []string{"mode", "uid", "gid", "file", "symlink"} {
		t.Run(conflict, func(t *testing.T) {
			h, transport := newAutobrrDirectoryHarness(t, autobrr.App{})
			dir := filepath.Join(h.root(), "apps", "autobrr", "config")
			if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
				t.Fatal(err)
			}
			settings := map[string]string{}
			switch conflict {
			case "file":
				if err := os.WriteFile(dir, []byte("preserve occupying file"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".keep"), []byte("preserve existing marker"), 0o600); err != nil {
					t.Fatal(err)
				}
				switch conflict {
				case "mode":
					if err := os.Chmod(dir, 0o755); err != nil {
						t.Fatal(err)
					}
				case "uid":
					settings["PUID"] = strconv.Itoa(os.Geteuid() + 1)
				case "gid":
					settings["PGID"] = strconv.Itoa(os.Getegid() + 1)
				}
			}
			before, err := os.Lstat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.svc.Install(t.Context(), InstallOptions{Targets: []string{"autobrr"}, Settings: settings}); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("incompatible configuration storage did not hold: %v", err)
			}
			after, err := os.Lstat(dir)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
				t.Fatal("incompatible configuration storage was repaired or replaced")
			}
			if before.IsDir() {
				data, err := os.ReadFile(filepath.Join(dir, ".keep"))
				if err != nil || string(data) != "preserve existing marker" {
					t.Fatal("directory conflict changed a marker")
				}
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
			if transport.requests != 0 {
				t.Fatal("incompatible storage reached App authentication")
			}
		})
	}
}

func TestAutobrrDirectoryGeneratedRefreshPreservesUnsupportedStorageAsManual(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this regression requires an unsupported native filesystem/ACL model")
	}
	h, transport := newAutobrrDirectoryHarness(t, autobrrLegacyDirectoryApp{})
	if err := h.install("autobrr"); err != nil {
		t.Fatal(err)
	}
	h.svc.Catalog = catalog.New([]app.App{autobrr.App{}}, nil)
	calls, requests := len(h.runner.Calls()), transport.requests
	if err := h.svc.Update(t.Context(), []string{"autobrr"}); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("generated refresh bypassed native directory admission: %v", err)
	}
	if e := h.rec.find("autobrr", "directories"); e == nil || e.Event != events.StepManual || !strings.Contains(e.Hint, "ACL") {
		t.Fatalf("generated refresh did not reach directory preparation: %+v", e)
	}
	if _, err := os.Lstat(filepath.Join(h.root(), "apps", "autobrr", "config")); !os.IsNotExist(err) {
		t.Fatal("unsupported generated refresh created a configuration directory")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
	if transport.requests != requests {
		t.Fatal("unsupported refresh reached App authentication")
	}
}

func TestAutobrrDirectoryGeneratedRefreshPreparesOrPreservesBeforeUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, mode := range []os.FileMode{0, 0o700, 0o755} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			h, _ := newAutobrrDirectoryHarness(t, autobrrLegacyDirectoryApp{})
			if err := h.install("autobrr"); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(h.root(), "apps", "autobrr", "config")
			var before os.FileInfo
			if mode != 0 {
				if err := os.Mkdir(dir, mode); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".keep"), []byte("preserve marker and configuration"), 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = os.Stat(dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			h.svc.Catalog = catalog.New([]app.App{autobrr.App{}}, nil)
			calls := len(h.runner.Calls())
			starts := 0
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), "up -d") {
					starts++
					info, err := os.Stat(dir)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Errorf("refresh reached Up before private config admission: %v", err)
					}
				}
				return system.Result{}
			})
			err := h.svc.Update(t.Context(), []string{"autobrr"})
			if mode == 0o755 {
				if !errors.Is(err, helpers.ErrManual) || starts != 0 {
					t.Fatalf("incompatible existing configuration was repaired or started: %v", err)
				}
				assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
			} else if err != nil || starts != 1 {
				t.Fatalf("generated refresh did not prepare before Up: %v, starts=%d", err, starts)
			}
			if before != nil {
				after, err := os.Stat(dir)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
					t.Fatal("refresh changed existing configuration-directory metadata")
				}
				marker, err := os.ReadFile(filepath.Join(dir, ".keep"))
				if err != nil || string(marker) != "preserve marker and configuration" {
					t.Fatal("refresh changed an existing marker")
				}
			}
		})
	}
}
