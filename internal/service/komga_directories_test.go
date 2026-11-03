package service

import (
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/komga"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
	"golang.org/x/sys/unix"
)

type komgaDirectoryHTTP func(*http.Request) (*http.Response, error)

func (f komgaDirectoryHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func komgaDirectoryHarness(t *testing.T, a app.App) (*harness, *atomic.Int64) {
	t.Helper()
	h := newDirectoryHarness(t, a)
	requests := &atomic.Int64{}
	h.deps.HTTP = &http.Client{Transport: komgaDirectoryHTTP(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := `{"isClaimed":true}`
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/claim" && r.URL.Path != "/api/v2/users/me" {
			t.Errorf("directory preparation authorized an account/provider action: %s %s", r.Method, r.URL.Path)
			return nil, errors.New("unexpected synthetic Komga request")
		}
		if r.URL.Path == "/api/v2/users/me" {
			user, password, ok := r.BasicAuth()
			if !ok || user != "admin@selfhost.local" || password != "synthetic-admin-password" {
				t.Error("production Setup lost its intended administrator credentials")
				return nil, errors.New("unexpected synthetic Komga credentials")
			}
			body = `{"id":"synthetic-admin","email":"admin@selfhost.local","roles":["USER","ADMIN"]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	return h, requests
}

func komgaDirectoryInstall(t *testing.T, h *harness) error {
	t.Helper()
	owner, err := h.deps.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	return h.svc.Install(t.Context(), InstallOptions{
		Targets: []string{"komga"}, AdminUser: "admin", AdminPassword: "synthetic-admin-password", Exposure: "local",
		MediaRoot: filepath.Join(owner.Home, "media"), DownloadsRoot: filepath.Join(owner.Home, "downloads"),
	})
}

func komgaDirectoryContent(t *testing.T, dir string) os.FileInfo {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"database.sqlite": "synthetic existing database", ".keep": "existing marker"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func komgaDirectoryPreserved(t *testing.T, dir string, before os.FileInfo) {
	t.Helper()
	after, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	old, current := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || old.Uid != current.Uid || old.Gid != current.Gid {
		t.Fatal("existing configuration identity, mode or ownership changed")
	}
	if !before.IsDir() {
		return
	}
	for name, content := range map[string]string{"database.sqlite": "synthetic existing database", ".keep": "existing marker"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || string(data) != content || info.Mode().Perm() != 0o400 {
			t.Fatalf("existing configuration data/marker or child permissions changed: %s", name)
		}
	}
}

func TestKomgaDirectoryInstallPreparesBeforeFirstUpOnNativeLinux(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	h, requests := komgaDirectoryHarness(t, komga.App{})
	starts := 0
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if !strings.Contains(systemtest.Line(cmd), "up -d") {
			return system.Result{}
		}
		starts++
		info, err := os.Stat(filepath.Join(h.root(), "apps", "komga", "config"))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Error("Komga reached Up without a prepared private configuration directory")
			return system.Result{}
		}
		stat := info.Sys().(*syscall.Stat_t)
		if stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
			t.Error("Komga configuration does not match the selected runtime owner")
		}
		return system.Result{}
	})
	if err := komgaDirectoryInstall(t, h); err != nil || starts != 1 || requests.Load() == 0 {
		t.Fatalf("production Install did not prepare then start and verify Komga: %v, starts=%d", err, starts)
	}
	if result := h.rec.find("komga", "directories"); result == nil || result.Event != events.StepOK {
		t.Fatalf("production directory action was not recorded: %+v", result)
	}
	owner, _ := h.deps.CurrentUser()
	data, err := project.Open(owner).ReadFragment("komga")
	if err != nil || !strings.Contains(string(data), "create_host_path: false") || !strings.Contains(string(data), "gotson/komga:1.x") {
		t.Fatal("production Compose lost the strict bind or approved image track")
	}
}

func TestKomgaDirectoryInstallPreservesCompatibleExistingConfigOnNativeLinux(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	h, _ := komgaDirectoryHarness(t, komga.App{})
	dir := filepath.Join(h.root(), "apps", "komga", "config")
	before := komgaDirectoryContent(t, dir)
	if err := komgaDirectoryInstall(t, h); err != nil {
		t.Fatal(err)
	}
	if err := komgaDirectoryInstall(t, h); err != nil {
		t.Fatal(err)
	}
	komgaDirectoryPreserved(t, dir, before)
	for _, call := range h.runner.Calls() {
		if strings.Contains(call, "chown") || strings.Contains(call, " run ") {
			t.Fatalf("compatible storage triggered recursive/helper repair: %s", call)
		}
	}
}

type komgaDirectoryVersion struct {
	komga.App
	legacy bool
}

func (a *komgaDirectoryVersion) Directories(rc app.RenderContext) []app.Directory {
	if a.legacy {
		return nil
	}
	return a.App.Directories(rc)
}

func (a *komgaDirectoryVersion) Compose(rc app.RenderContext) string {
	current := a.App.Compose(rc)
	if a.legacy {
		return strings.Replace(current, "      - type: bind\n        source: ./config\n        target: /config\n        bind:\n          create_host_path: false", "      - ./config:/config", 1)
	}
	return current
}

func TestKomgaDirectoryGeneratedRefreshPreparesBeforeUpOnNativeLinux(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, existing := range []bool{false, true} {
		t.Run(fmtKomgaDirectoryCase(true, existing), func(t *testing.T) {
			a := &komgaDirectoryVersion{legacy: true}
			h, _ := komgaDirectoryHarness(t, a)
			if err := komgaDirectoryInstall(t, h); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(h.root(), "apps", "komga", "config")
			var before os.FileInfo
			if existing {
				before = komgaDirectoryContent(t, dir)
			}
			a.legacy = false
			starts := 0
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), "up -d") {
					starts++
					info, err := os.Stat(dir)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Error("generated refresh reached Up without prepared Komga configuration")
						return system.Result{}
					}
					stat := info.Sys().(*syscall.Stat_t)
					if stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
						t.Error("generated refresh configuration differs from the selected runtime owner")
					}
				}
				return system.Result{}
			})
			calls := len(h.runner.Calls())
			if err := h.svc.Update(t.Context(), []string{"komga"}); err != nil || starts != 1 {
				t.Fatalf("generated refresh did not prepare Komga before Up: %v", err)
			}
			if result := h.rec.find("komga", "directories"); result == nil || result.Event != events.StepOK {
				t.Fatalf("generated refresh did not run the production directory action: %+v", result)
			}
			for _, call := range h.runner.Calls()[calls:] {
				if strings.Contains(call, "chown") || strings.Contains(call, " run ") {
					t.Fatalf("generated refresh triggered recursive/helper repair: %s", call)
				}
			}
			owner, _ := h.deps.CurrentUser()
			data, err := project.Open(owner).ReadFragment("komga")
			if err != nil || !strings.Contains(string(data), "create_host_path: false") {
				t.Fatal("generated refresh did not publish the strict configuration bind")
			}
			if existing {
				komgaDirectoryPreserved(t, dir, before)
			}
		})
	}
}

func TestKomgaDirectoryNativeUnknownACLModelHoldsInstallAndRefresh(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this case requires an actually unsupported native filesystem/ACL model")
	}
	for _, refresh := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(fmtKomgaDirectoryCase(refresh, existing), func(t *testing.T) {
				a := &komgaDirectoryVersion{legacy: refresh}
				h, requests := komgaDirectoryHarness(t, a)
				if refresh {
					if err := komgaDirectoryInstall(t, h); err != nil {
						t.Fatal(err)
					}
					a.legacy = false
				}
				dir := filepath.Join(h.root(), "apps", "komga", "config")
				var before os.FileInfo
				if existing {
					before = komgaDirectoryContent(t, dir)
				}
				calls, httpCalls := len(h.runner.Calls()), requests.Load()
				var err error
				if refresh {
					err = h.svc.Update(t.Context(), []string{"komga"})
				} else {
					err = komgaDirectoryInstall(t, h)
				}
				if !errors.Is(err, helpers.ErrManual) || requests.Load() != httpCalls {
					t.Fatalf("unsupported native semantics reached Setup/Check: %v", err)
				}
				assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
				result := h.rec.find("komga", "directories")
				if result == nil || result.Event != events.StepManual || !strings.Contains(result.Hint, "ACL") || !strings.Contains(result.Hint, "Preserve") {
					t.Fatalf("unsupported native semantics did not record actionable Manual: %+v", result)
				}
				if existing {
					komgaDirectoryPreserved(t, dir, before)
				} else if _, err := os.Lstat(dir); !os.IsNotExist(err) {
					t.Fatal("fake Linux daemon metadata created configuration on an unsupported native host")
				}
			})
		}
	}
}

func fmtKomgaDirectoryCase(refresh, existing bool) string {
	name := "install"
	if refresh {
		name = "refresh"
	}
	if existing {
		return name + "/existing"
	}
	return name + "/missing"
}

func TestKomgaDirectoryConflictsPreserveDataAndStopInstallAndRefresh(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		for _, conflict := range []string{"file", "symlink", "broad-mode", "owner", "ACL"} {
			t.Run(fmtKomgaDirectoryCase(refresh, true)+"/"+conflict, func(t *testing.T) {
				if conflict == "owner" || conflict == "ACL" || conflict == "broad-mode" {
					requireNativeDirectoryFilesystem(t)
				}
				if conflict == "owner" && os.Geteuid() != 0 {
					t.Skip("different existing owner fixture requires a native privileged caller")
				}
				a := &komgaDirectoryVersion{legacy: refresh}
				h, requests := komgaDirectoryHarness(t, a)
				if refresh {
					if err := komgaDirectoryInstall(t, h); err != nil {
						t.Fatal(err)
					}
					a.legacy = false
				}
				dir := filepath.Join(h.root(), "apps", "komga", "config")
				if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
					t.Fatal(err)
				}
				var targetBefore os.FileInfo
				target := filepath.Join(filepath.Dir(dir), "person-data")
				switch conflict {
				case "file":
					if err := os.WriteFile(dir, []byte("existing configuration file"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					targetBefore = komgaDirectoryContent(t, target)
					if err := os.Symlink(target, dir); err != nil {
						t.Fatal(err)
					}
				default:
					komgaDirectoryContent(t, dir)
					if conflict == "broad-mode" {
						if err := os.Chmod(dir, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					if conflict == "owner" {
						if err := os.Chown(dir, 1, 1); err != nil {
							t.Fatal(err)
						}
					}
				}
				var acl []byte
				if conflict == "ACL" {
					acl = make([]byte, 44)
					binary.LittleEndian.PutUint32(acl, 2)
					for index, entry := range []struct {
						tag, permission uint16
						id              uint32
					}{{1, 7, 0xffffffff}, {2, 4, uint32(os.Geteuid() + 1)}, {4, 0, 0xffffffff}, {16, 0, 0xffffffff}, {32, 0, 0xffffffff}} {
						binary.LittleEndian.PutUint16(acl[4+index*8:], entry.tag)
						binary.LittleEndian.PutUint16(acl[6+index*8:], entry.permission)
						binary.LittleEndian.PutUint32(acl[8+index*8:], entry.id)
					}
					if err := unix.Setxattr(dir, "system.posix_acl_access", acl, 0); err != nil {
						t.Skipf("actual native ACL fixture unavailable: %v", err)
					}
				}
				before, err := os.Lstat(dir)
				if err != nil {
					t.Fatal(err)
				}
				calls, httpCalls := len(h.runner.Calls()), requests.Load()
				if refresh {
					err = h.svc.Update(t.Context(), []string{"komga"})
				} else {
					err = komgaDirectoryInstall(t, h)
				}
				if !errors.Is(err, helpers.ErrManual) || requests.Load() != httpCalls {
					t.Fatalf("incompatible configuration did not remain Manual: %v", err)
				}
				assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
				komgaDirectoryPreserved(t, dir, before)
				if conflict == "file" {
					data, err := os.ReadFile(dir)
					if err != nil || string(data) != "existing configuration file" {
						t.Fatal("occupying configuration file was changed")
					}
				}
				if conflict == "symlink" {
					actual, err := os.Readlink(dir)
					if err != nil || actual != target {
						t.Fatal("existing configuration symlink was changed")
					}
					komgaDirectoryPreserved(t, target, targetBefore)
				}
				if conflict == "ACL" {
					actual := make([]byte, len(acl))
					n, err := unix.Getxattr(dir, "system.posix_acl_access", actual)
					if err != nil || n != len(acl) || string(actual) != string(acl) {
						t.Fatal("existing native ACL was reset or changed")
					}
				}
			})
		}
	}
}
