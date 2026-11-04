package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/sportarr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type sportarrDirectoryApp struct {
	sportarr.App
	setups int
	checks int
}

func (a *sportarrDirectoryApp) Setup(context.Context, *helpers.H) error {
	a.setups++
	return nil
}

func (a *sportarrDirectoryApp) Check(context.Context, *helpers.H) error {
	a.checks++
	return nil
}

type sportarrLegacyDirectoryApp struct{ app.App }

func (a sportarrLegacyDirectoryApp) Compose(rc app.RenderContext) string {
	return strings.Replace(a.App.Compose(rc), "      - type: bind\n        source: ./config\n        target: /config\n        bind:\n          create_host_path: false", "      - ./config:/config", 1)
}

func sportarrDirectoryHarness(t *testing.T, operation string) (*harness, *sportarrDirectoryApp, func() error) {
	t.Helper()
	a := &sportarrDirectoryApp{}
	h := newDirectoryHarness(t, a)
	if operation == "refresh" {
		h.svc.Catalog = catalog.New([]app.App{sportarrLegacyDirectoryApp{App: a}}, nil)
		if err := h.install("sportarr"); err != nil {
			t.Fatal(err)
		}
		h.svc.Catalog = catalog.New([]app.App{a}, nil)
		return h, a, func() error { return h.svc.Update(context.Background(), []string{"sportarr"}) }
	}
	return h, a, func() error { return h.install("sportarr") }
}

type sportarrDirectoryEntry struct {
	mode os.FileMode
	uid  uint32
	gid  uint32
	ino  uint64
	data string
}

func sportarrDirectorySnapshot(t *testing.T, path string) map[string]sportarrDirectoryEntry {
	t.Helper()
	entries := map[string]sportarrDirectoryEntry{}
	err := filepath.WalkDir(path, func(path string, _ fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		entry := sportarrDirectoryEntry{mode: info.Mode(), uid: stat.Uid, gid: stat.Gid, ino: stat.Ino}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.data = string(data)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.data, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		entries[path] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func sportarrDirectoryExistingData(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".keep", "sportarr.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("preserved synthetic "+name), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func sportarrDirectoryAssertManual(t *testing.T, h *harness, a *sportarrDirectoryApp, run func() error) {
	t.Helper()
	dir := filepath.Join(h.root(), "apps", "sportarr", "config")
	before := sportarrDirectorySnapshot(t, dir)
	compose, composeErr := os.ReadFile(filepath.Join(h.root(), "compose.yml"))
	calls, setups, checks := len(h.runner.Calls()), a.setups, a.checks
	if err := run(); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Sportarr storage did not stop as Manual: %v", err)
	}
	if a.setups != setups || a.checks != checks {
		t.Fatal("directory hold reached App Setup or Check")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
	if !reflect.DeepEqual(before, sportarrDirectorySnapshot(t, dir)) {
		t.Fatal("Sportarr storage contents, marker, identity or metadata changed")
	}
	after, afterErr := os.ReadFile(filepath.Join(h.root(), "compose.yml"))
	if composeErr == nil {
		if afterErr != nil || string(compose) != string(after) {
			t.Fatal("directory hold changed the existing Compose definition")
		}
	} else {
		if !errors.Is(composeErr, os.ErrNotExist) || afterErr != nil && !errors.Is(afterErr, os.ErrNotExist) {
			t.Fatalf("could not inspect Compose before/after the hold: %v, %v", composeErr, afterErr)
		}
		if strings.Contains(string(after), "sportarr:") {
			t.Fatal("directory hold published Sportarr in the new Project Compose")
		}
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	state, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	step := state.Apps["sportarr"].Steps["directories"]
	if step.Status != stepstatus.StepManual || !strings.Contains(step.Hint, "Preserve") || !strings.Contains(step.Hint, "self-host install sportarr") {
		t.Fatalf("directory hold lost its actionable persisted Manual: %+v", step)
	}
}

func TestSportarrDirectoryInstallAndGeneratedRefreshPrepareBeforeUp(t *testing.T) {
	for _, operation := range []string{"install", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			requireNativeDirectoryFilesystem(t)
			h, a, run := sportarrDirectoryHarness(t, operation)
			starts, wantSetups := 0, a.setups
			if operation == "install" {
				wantSetups++
			}
			dir := filepath.Join(h.root(), "apps", "sportarr", "config")
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fixture already has configuration storage")
			}
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), "up -d") {
					starts++
					info, err := os.Stat(dir)
					if err != nil {
						t.Errorf("Sportarr reached Up before preparing /config: %v", err)
						return system.Result{}
					}
					stat := info.Sys().(*syscall.Stat_t)
					if !info.IsDir() || info.Mode().Perm() != 0o755 || int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
						t.Error("Sportarr config was not prepared for the selected decimal runtime owner/mode")
					}
					data, err := os.ReadFile(filepath.Join(h.root(), "compose.yml"))
					if err != nil || !strings.Contains(string(data), "create_host_path: false") || !strings.Contains(string(data), "target: /config") {
						t.Error("Sportarr reached Up without its guarded configuration bind")
					}
				}
				return system.Result{}
			})
			if err := run(); err != nil || starts != 1 || a.setups != wantSetups {
				t.Fatalf("Sportarr %s: %v, starts=%d", operation, err, starts)
			}
			if e := h.rec.find("sportarr", "directories"); e == nil || e.Event != events.StepOK {
				t.Fatal("Sportarr never reached the shared directory preparation action")
			}
			if _, err := os.Lstat(filepath.Join(dir, ".keep")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("directory-only preparation created a sentinel")
			}
		})
	}
}

func TestSportarrDirectoryInstallAndGeneratedRefreshPreserveCompatibleData(t *testing.T) {
	for _, operation := range []string{"install", "refresh"} {
		for _, mode := range []os.FileMode{0o700, 0o750, 0o755} {
			t.Run(operation+"/"+mode.String(), func(t *testing.T) {
				requireNativeDirectoryFilesystem(t)
				h, _, run := sportarrDirectoryHarness(t, operation)
				dir := filepath.Join(h.root(), "apps", "sportarr", "config")
				sportarrDirectoryExistingData(t, dir, mode)
				before := sportarrDirectorySnapshot(t, dir)
				calls := len(h.runner.Calls())
				for range 2 {
					if err := run(); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, sportarrDirectorySnapshot(t, dir)) {
						t.Fatal("compatible Sportarr data, old marker, UID/GID or tighter mode was changed")
					}
				}
				for _, call := range h.runner.Calls()[calls:] {
					if strings.Contains(call, "chown") || strings.Contains(call, " run ") {
						t.Fatal("compatible Sportarr storage triggered ownership repair or a helper container")
					}
				}
			})
		}
	}
}

func TestSportarrDirectoryUnsupportedMappingStopsInstallAndRefresh(t *testing.T) {
	for _, operation := range []string{"install", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			h, a, run := sportarrDirectoryHarness(t, operation)
			h.deps.GOOS = "darwin"
			sportarrDirectoryAssertManual(t, h, a, run)
			if e := h.rec.find("sportarr", "directories"); e == nil || !strings.Contains(e.Hint, "mapping") {
				t.Fatal("unsupported runtime mapping did not hold at directory preparation")
			}
		})
	}
}

func TestSportarrDirectoryUnsupportedNativeFilesystemStopsInstallAndRefresh(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this regression requires an unsupported native filesystem/ACL platform")
	}
	for _, operation := range []string{"install", "refresh"} {
		for _, existing := range []bool{false, true} {
			name := "missing"
			if existing {
				name = "existing"
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				h, a, run := sportarrDirectoryHarness(t, operation)
				if existing {
					sportarrDirectoryExistingData(t, filepath.Join(h.root(), "apps", "sportarr", "config"), 0o750)
				}
				sportarrDirectoryAssertManual(t, h, a, run)
				if e := h.rec.find("sportarr", "directories"); e == nil || !strings.Contains(e.Hint, "ACL") {
					t.Fatal("fake Linux daemon metadata bypassed real native filesystem/ACL admission")
				}
			})
		}
	}
}

func TestSportarrDirectoryConflictsStopInstallAndGeneratedRefresh(t *testing.T) {
	for _, operation := range []string{"install", "refresh"} {
		for _, conflict := range []string{"file", "symlink", "unwritable", "shared-mode", "foreign-owner", "foreign-group"} {
			t.Run(operation+"/"+conflict, func(t *testing.T) {
				requireNativeDirectoryFilesystem(t)
				if strings.HasPrefix(conflict, "foreign-") && os.Geteuid() != 0 {
					t.Skip("real different-owner/group fixtures require a native Linux root caller")
				}
				h, a, run := sportarrDirectoryHarness(t, operation)
				dir := filepath.Join(h.root(), "apps", "sportarr", "config")
				if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
					t.Fatal(err)
				}
				switch conflict {
				case "file":
					if err := os.WriteFile(dir, []byte("preserved unknown data"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					target := filepath.Join(filepath.Dir(dir), "person-owned")
					sportarrDirectoryExistingData(t, target, 0o700)
					before := sportarrDirectorySnapshot(t, target)
					if err := os.Symlink(target, dir); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if !reflect.DeepEqual(before, sportarrDirectorySnapshot(t, target)) {
							t.Error("symlink target was changed")
						}
					})
				default:
					sportarrDirectoryExistingData(t, dir, 0o750)
					switch conflict {
					case "unwritable":
						if err := os.Chmod(dir, 0o500); err != nil {
							t.Fatal(err)
						}
					case "shared-mode":
						if err := os.Chmod(dir, 0o777); err != nil {
							t.Fatal(err)
						}
					case "foreign-owner":
						if err := os.Chown(dir, os.Geteuid()+1, os.Getegid()); err != nil {
							t.Fatal(err)
						}
					case "foreign-group":
						if err := os.Chown(dir, os.Geteuid(), os.Getegid()+1); err != nil {
							t.Fatal(err)
						}
					}
				}
				sportarrDirectoryAssertManual(t, h, a, run)
			})
		}
	}
}
