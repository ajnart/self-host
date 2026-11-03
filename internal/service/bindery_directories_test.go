package service

import (
	"errors"
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
	"github.com/ajnart/self-host/internal/apps/bindery"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type binderyDirectoryHTTP func(*http.Request) (*http.Response, error)

func (f binderyDirectoryHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newBinderyDirectoryHarness(t *testing.T, refresh bool) *harness {
	t.Helper()
	h := newDirectoryHarness(t, bindery.App{}, &testApp{id: "seed", done: map[string]bool{}})
	h.deps.HTTP = &http.Client{Transport: binderyDirectoryHTTP(func(r *http.Request) (*http.Response, error) {
		if e := h.rec.find("bindery", "directories"); e == nil || e.Event != events.StepOK {
			t.Error("Bindery reached HTTP before successful directory preparation")
		}
		body := `{"status":"ok"}`
		switch r.URL.Path {
		case "/api/v1/health":
		case "/api/v1/auth/status":
			body = `{"authenticated":false,"setupRequired":true,"mode":"enabled","localAuthEnabled":true}`
		default:
			t.Errorf("directory preparation reached an unexpected Bindery endpoint: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Error("directory preparation attempted a Bindery mutation")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if !refresh {
		return h
	}
	if err := h.install("seed"); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	fragment := (bindery.App{}).Compose(app.RenderContext{Port: 8787, Settings: settings})
	fragment = strings.Replace(fragment, "      - type: bind\n        source: ./config\n        target: /config\n        bind:\n          create_host_path: false", "      - ./config:/config", 1)
	if err := p.WriteFragment("bindery", fragment); err != nil {
		t.Fatal(err)
	}
	data, err := p.ReadFragment("bindery")
	if err != nil {
		t.Fatal(err)
	}
	state, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.Apps["bindery"] = &project.AppState{Port: 8787, Setup: stepstatus.SetupManual, FragmentSHA256: fragmentSum(data)}
	if err := p.SaveState(state); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteRoot(); err != nil {
		t.Fatal(err)
	}
	return h
}

func binderyDirectoryAction(t *testing.T, h *harness, refresh bool) error {
	t.Helper()
	if refresh {
		return h.svc.Update(t.Context(), []string{"bindery"})
	}
	return h.install("bindery")
}

func binderyDirectorySnapshot(t *testing.T, paths ...string) func() {
	t.Helper()
	infos := map[string]os.FileInfo{}
	contents := map[string]string{}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		infos[path] = info
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			contents[path] = string(data)
		}
	}
	return func() {
		t.Helper()
		for path, before := range infos {
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			oldOwner, newOwner := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
			if !os.SameFile(before, after) || before.Mode() != after.Mode() || oldOwner.Uid != newOwner.Uid || oldOwner.Gid != newOwner.Gid || !before.ModTime().Equal(after.ModTime()) {
				t.Errorf("Bindery preparation changed existing object metadata: %s", path)
			}
			if content, found := contents[path]; found {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != content {
					t.Errorf("Bindery preparation changed existing marker or data: %s (%v)", path, err)
				}
			}
		}
	}
}

func binderyExistingDirectory(t *testing.T, dir string, mode os.FileMode) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{dir, filepath.Join(dir, "cache")}
	for _, name := range []string{".keep", "bindery.db", "cache/cover.dat"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("preserve existing "+name), 0o640); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return binderyDirectorySnapshot(t, paths...)
}

func assertBinderyDirectoryHold(t *testing.T, h *harness, refresh bool, before int) {
	t.Helper()
	if err := binderyDirectoryAction(t, h, refresh); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Bindery directory hold did not remain Manual: %v", err)
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[before:])
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	state, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	step := state.Apps["bindery"].Steps["directories"]
	if step.Status != stepstatus.StepManual || !strings.Contains(step.Hint, "Preserve apps/bindery") || !strings.Contains(step.Hint, "config") {
		t.Fatalf("Bindery did not persist its actionable directory hold: %+v", step)
	}
}

func TestBinderyDirectoryUnsupportedNativeStorageHoldsInstallAndRefresh(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("unsupported native OS regression requires a non-Linux host")
	}
	for _, operation := range []string{"install", "refresh"} {
		for _, storage := range []string{"missing", "existing"} {
			t.Run(operation+"/"+storage, func(t *testing.T) {
				refresh := operation == "refresh"
				h := newBinderyDirectoryHarness(t, refresh)
				dir := filepath.Join(h.root(), "apps", "bindery", "config")
				if storage == "existing" {
					preserved := binderyExistingDirectory(t, dir, 0o700)
					defer preserved()
				}
				assertBinderyDirectoryHold(t, h, refresh, len(h.runner.Calls()))
				if storage == "missing" {
					if _, err := os.Lstat(dir); !os.IsNotExist(err) {
						t.Fatalf("fake Linux daemon metadata bypassed native filesystem admission: %v", err)
					}
				}
			})
		}
	}
}

func TestBinderyDirectoryInstallAndRefreshPrepareBeforeUpAndPreserveData(t *testing.T) {
	for _, operation := range []string{"install", "refresh"} {
		for _, mode := range []os.FileMode{0, 0o755, 0o750, 0o700} {
			t.Run(operation+"/"+strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
				requireNativeDirectoryFilesystem(t)
				refresh := operation == "refresh"
				h := newBinderyDirectoryHarness(t, refresh)
				dir := filepath.Join(h.root(), "apps", "bindery", "config")
				expectedMode := mode
				preserved := func() {}
				if mode == 0 {
					expectedMode = 0o755
				} else {
					preserved = binderyExistingDirectory(t, dir, mode)
				}
				starts := 0
				h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
					if !strings.Contains(systemtest.Line(cmd), "up -d") {
						return system.Result{}
					}
					starts++
					info, err := os.Stat(dir)
					if err != nil {
						t.Fatalf("Bindery reached Up without configuration preparation: %v", err)
					}
					owner := info.Sys().(*syscall.Stat_t)
					if info.Mode().Perm() != expectedMode || int(owner.Uid) != os.Geteuid() || int(owner.Gid) != os.Getegid() {
						t.Error("Bindery configuration owner/mode disagrees with the selected runtime before Up")
					}
					preserved()
					if mode == 0 {
						if _, err := os.Lstat(filepath.Join(dir, ".keep")); !os.IsNotExist(err) {
							t.Error("Bindery preparation wrote a sentinel")
						}
					}
					data, err := os.ReadFile(filepath.Join(h.root(), "compose.yml"))
					if err != nil || !strings.Contains(string(data), "create_host_path: false") || !strings.Contains(string(data), "source: ./apps/bindery/config") {
						t.Errorf("Bindery startup lost the guarded configuration bind: %v", err)
					}
					return system.Result{}
				})
				before := len(h.runner.Calls())
				if err := binderyDirectoryAction(t, h, refresh); !errors.Is(err, ErrManual) || starts != 1 {
					t.Fatalf("directory preparation must reach Up then retain first-administrator Manual: %v, starts=%d", err, starts)
				}
				if e := h.rec.find("bindery", "directories"); e == nil || e.Event != events.StepOK {
					t.Fatal("Bindery did not record successful preparation")
				}
				for _, call := range h.runner.Calls()[before:] {
					if strings.Contains(call, "chown") || strings.Contains(call, " run ") {
						t.Errorf("Bindery preparation invoked ownership repair: %s", call)
					}
				}
				preserved()
			})
		}
	}
}

func TestBinderyDirectoryIncompatibleStorageHoldsInstallAndRefresh(t *testing.T) {
	for _, operation := range []string{"install", "refresh"} {
		for _, conflict := range []string{"mode", "owner", "file", "symlink"} {
			t.Run(operation+"/"+conflict, func(t *testing.T) {
				requireNativeDirectoryFilesystem(t)
				if conflict == "owner" && os.Geteuid() != 0 {
					t.Skip("different-owner native storage fixture requires root")
				}
				refresh := operation == "refresh"
				h := newBinderyDirectoryHarness(t, refresh)
				dir := filepath.Join(h.root(), "apps", "bindery", "config")
				if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
					t.Fatal(err)
				}
				var preserved func()
				switch conflict {
				case "file":
					if err := os.WriteFile(dir, []byte("preserve occupying configuration file"), 0o600); err != nil {
						t.Fatal(err)
					}
					preserved = binderyDirectorySnapshot(t, dir)
				case "symlink":
					target := filepath.Join(h.root(), "preserved-config")
					dataPreserved := binderyExistingDirectory(t, target, 0o700)
					if err := os.Symlink(target, dir); err != nil {
						t.Fatal(err)
					}
					linkPreserved := binderyDirectorySnapshot(t, dir)
					preserved = func() { dataPreserved(); linkPreserved() }
				default:
					preserved = binderyExistingDirectory(t, dir, 0o770)
					if conflict == "owner" {
						if err := os.Chmod(dir, 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.Chown(dir, os.Geteuid()+1, os.Getegid()+1); err != nil {
							t.Fatal(err)
						}
						preserved = binderyDirectorySnapshot(t, dir, filepath.Join(dir, ".keep"), filepath.Join(dir, "bindery.db"), filepath.Join(dir, "cache"), filepath.Join(dir, "cache", "cover.dat"))
					}
				}
				assertBinderyDirectoryHold(t, h, refresh, len(h.runner.Calls()))
				preserved()
			})
		}
	}
}
