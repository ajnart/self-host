package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
	"golang.org/x/sys/unix"
)

type directoryTestApp struct {
	*testApp
	directories []app.Directory
	preflight   bool
}

func newDirectoryTestApp(id string, mode uint32) *directoryTestApp {
	return &directoryTestApp{
		testApp: &testApp{id: id, done: map[string]bool{}},
		directories: []app.Directory{{Path: "config", Mode: mode,
			UID: strconv.Itoa(os.Geteuid()), GID: strconv.Itoa(os.Getegid())}},
	}
}

func (a *directoryTestApp) Directories(app.RenderContext) []app.Directory { return a.directories }

func (a *directoryTestApp) Compose(app.RenderContext) string {
	compose := "services:\n  " + a.id + ":\n    image: " + a.Info().Image + "\n"
	if len(a.directories) != 0 {
		compose += "    user: '" + a.directories[0].UID + ":" + a.directories[0].GID + "'\n    volumes:\n"
		for _, dir := range a.directories {
			compose += "      - type: bind\n        source: ./" + dir.Path + "\n        target: /" + dir.Path + "\n        bind:\n          create_host_path: false\n"
		}
	}
	return compose
}

func (a *directoryTestApp) Preflight(_ context.Context, h *helpers.H) error {
	if !a.preflight {
		return nil
	}
	info, err := os.Lstat(filepath.Join(h.Dir, "config"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return &helpers.ManualError{Hint: "preserve existing private storage"}
	}
	return nil
}

type directoryFileTestApp struct {
	*directoryTestApp
	files []app.File
}

func (a *directoryFileTestApp) Files(app.RenderContext, func(string) string) []app.File {
	return a.files
}

func newDirectoryHarness(t *testing.T, apps ...app.App) *harness {
	t.Helper()
	h := newHarness(t, apps...)
	owner, err := h.deps.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	owner.UID, owner.GID = os.Geteuid(), os.Getegid()
	dir, err := os.MkdirTemp(".", ".directory-test-")
	if err != nil {
		t.Fatal(err)
	}
	owner.Home, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner.Home, err = filepath.EvalSymlinks(owner.Home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(owner.Home); err != nil {
			t.Error(err)
		}
	})
	h.deps.WithAccounts(systemtest.NewAccounts(owner))
	return h
}

func assertNoDirectoryRepairOrStartup(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(call, "chown") || strings.Contains(call, "up -d") || strings.Contains(call, " run ") {
			t.Errorf("directory conflict reached a repair or startup: %s", call)
		}
	}
}

func requireNativeDirectoryFilesystem(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("successful native filesystem/ACL admission requires Linux")
	}
	path, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	for {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t).Uid
		if owner != 0 && owner != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetgid|os.ModeSetuid|os.ModeSticky) != 0 {
			file.Close()
			t.Skip("native fixture has a shared or foreign-controlled path prefix")
		}
		var stat unix.Statfs_t
		if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if stat.Flags&1 != 0 || int64(stat.Type) != 0xef53 && int64(stat.Type) != 0x58465342 && int64(stat.Type) != 0x01021994 {
			file.Close()
			t.Skip("native test filesystem is outside the supported ACL model")
		}
		for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
			if _, err := unix.Fgetxattr(int(file.Fd()), name, nil); !errors.Is(err, unix.ENODATA) {
				file.Close()
				t.Skip("native test filesystem cannot establish ACL absence")
			}
		}
		file.Close()
		if filepath.Dir(path) == path {
			break
		}
		path = filepath.Dir(path)
	}
}

func TestDirectoryInstallPreparesTwoDirectoryOnlyAppsBeforeFirstUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	one, two := newDirectoryTestApp("one", 0o755), newDirectoryTestApp("two", 0o700)
	two.preflight = true
	h := newDirectoryHarness(t, one, two)
	starts := 0
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if !strings.Contains(systemtest.Line(cmd), "up -d") {
			return system.Result{}
		}
		starts++
		for _, a := range []*directoryTestApp{one, two} {
			info, err := os.Stat(filepath.Join(h.root(), "apps", a.id, "config"))
			if err != nil || info.Mode().Perm() != os.FileMode(a.directories[0].Mode) {
				t.Errorf("directory not prepared before first Up for %s: %v", a.id, err)
				continue
			}
			stat := info.Sys().(*syscall.Stat_t)
			if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
				t.Error("new directory belongs to a different runtime owner")
			}
		}
		return system.Result{}
	})
	if err := h.install("one", "two"); err != nil || starts != 1 {
		t.Fatalf("directory-only Install: %v, starts=%d", err, starts)
	}
	for _, a := range []*directoryTestApp{one, two} {
		if e := h.rec.find(a.id, "directories"); e == nil || e.Event != events.StepOK {
			t.Fatalf("directory preparation not reported: %+v", e)
		}
	}
}

func TestDirectoryInstallPreservesConflictsAsManualBeforeRecursiveRepairOrUp(t *testing.T) {
	for _, conflict := range []string{"mode", "owner", "file", "symlink", "overlap", "nested-alias"} {
		t.Run(conflict, func(t *testing.T) {
			base := newDirectoryTestApp("one", 0o700)
			var a app.App = base
			if conflict == "overlap" {
				a = &directoryFileTestApp{base, []app.File{{Path: "config/.keep", Keep: true}}}
			}
			if conflict == "nested-alias" {
				a = &directoryFileTestApp{base, []app.File{{Path: "alias/.keep", Keep: true}}}
			}
			h := newDirectoryHarness(t, a)
			dir := filepath.Join(h.root(), "apps", "one", "config")
			if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			switch conflict {
			case "file":
				if err := os.WriteFile(dir, []byte("preserve"), 0o600); err != nil {
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
				if conflict == "mode" {
					if err := os.Chmod(dir, 0o500); err != nil {
						t.Fatal(err)
					}
				}
				if conflict == "owner" {
					base.directories[0].UID = strconv.Itoa(os.Geteuid() + 1)
				}
				if conflict == "nested-alias" {
					if err := os.Symlink("config", filepath.Join(filepath.Dir(dir), "alias")); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := os.Lstat(dir)
			if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("directory conflict did not remain Manual: %v", err)
			}
			after, _ := os.Lstat(dir)
			if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid {
				t.Fatal("existing directory or file was changed")
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
			owner, _ := h.deps.CurrentUser()
			state, err := project.Open(owner).LoadState()
			if err != nil || state.Apps["one"].Steps["directories"].Status != stepstatus.StepManual || !strings.Contains(state.Apps["one"].Steps["directories"].Hint, "Preserve") {
				t.Fatalf("actionable directory Manual was not persisted: %v", err)
			}
		})
	}
}

func TestDirectoryInstallKeepsDisjointFilesAndValidatesAllDeclarationsFirst(t *testing.T) {
	t.Run("disjoint-file", func(t *testing.T) {
		requireNativeDirectoryFilesystem(t)
		base := newDirectoryTestApp("one", 0o700)
		a := &directoryFileTestApp{base, []app.File{{Path: "settings.json", Content: "preserve settings", Mode: 0o600}}}
		h := newDirectoryHarness(t, a)
		if err := h.install("one"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(h.root(), "apps", "one", "settings.json"))
		if err != nil || string(data) != "preserve settings" {
			t.Fatal("disjoint FileDeclarer was skipped")
		}
	})
	for _, invalid := range []app.Directory{
		{Path: "config/nested", Mode: 0o700, UID: "0", GID: "0"},
		{Path: "config", Mode: 0o700, UID: "", GID: "0"},
		{Path: "config", Mode: 0o700, UID: "-1", GID: "0"},
		{Path: "config", Mode: 0o700, UID: "4294967295", GID: "0"},
		{Path: "config", Mode: 0o7777, UID: "0", GID: "0"},
	} {
		base := newDirectoryTestApp("one", 0o700)
		base.directories = append(base.directories, invalid)
		h := newDirectoryHarness(t, base)
		if err := h.install("one"); err == nil {
			t.Fatalf("invalid declaration accepted: %+v", invalid)
		}
		if _, err := os.Lstat(filepath.Join(h.root(), "apps", "one", "config")); !os.IsNotExist(err) {
			t.Fatal("earlier declaration was created before validation finished")
		}
		assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
	}
}

func TestDirectoryGeneratedRefreshPreparesBeforeUpAndPreservesCustomCompose(t *testing.T) {
	for _, customized := range []bool{false, true} {
		t.Run(fmt.Sprint(customized), func(t *testing.T) {
			if !customized {
				requireNativeDirectoryFilesystem(t)
			}
			a := newDirectoryTestApp("one", 0o700)
			directories := a.directories
			a.directories = nil
			h := newDirectoryHarness(t, a)
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			if customized {
				path := filepath.Join(h.root(), "compose.yml")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.ReplaceAll(string(data), "example/test:latest", "example/person-selected:latest"))
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			a.directories = directories
			a.image = "example/test:next"
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if !customized && strings.Contains(systemtest.Line(cmd), "up -d") {
					if _, err := os.Stat(filepath.Join(h.root(), "apps", "one", "config")); err != nil {
						t.Error("generated refresh reached Up before directory preparation")
					}
				}
				return system.Result{}
			})
			before := len(h.runner.Calls())
			err := h.svc.Update(context.Background(), []string{"one"})
			if customized {
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("customized definition must not prepare missing declared storage: %v", err)
				}
				assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[before:])
				data, readErr := os.ReadFile(filepath.Join(h.root(), "compose.yml"))
				if readErr != nil || !strings.Contains(string(data), "example/person-selected:latest") {
					t.Fatal("runtime admission adopted the customized definition")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(h.root(), "apps", "one", "config"))
			if customized && !os.IsNotExist(err) || !customized && err != nil {
				t.Fatalf("refresh directory preparation did not respect generated ownership: %v", err)
			}
		})
	}
}

func TestDirectoryGeneratedRefreshConflictStopsBeforeUpAndPersistsManual(t *testing.T) {
	a := newDirectoryTestApp("one", 0o700)
	directories := a.directories
	a.directories = nil
	h := newDirectoryHarness(t, a)
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.root(), "apps", "one", "config")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	before := len(h.runner.Calls())
	a.directories = directories
	a.image = "example/test:next"
	if err := h.svc.Update(context.Background(), []string{"one"}); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("refresh conflict did not remain Manual: %v", err)
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[before:])
	owner, _ := h.deps.CurrentUser()
	state, err := project.Open(owner).LoadState()
	if err != nil || state.Apps["one"].Steps["directories"].Status != stepstatus.StepManual {
		t.Fatalf("refresh Manual was not persisted: %v", err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o755 {
		t.Fatal("refresh changed existing directory permissions")
	}
}

func TestDirectoryVerifiedRootlessMappingDistinguishesZeroAndSubordinateOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("subordinate ownership denial requires an unprivileged host caller")
	}
	for _, containerID := range []string{"0", "7"} {
		t.Run(containerID, func(t *testing.T) {
			if containerID == "0" {
				requireNativeDirectoryFilesystem(t)
			}
			a := newDirectoryTestApp("one", 0o700)
			a.directories[0].UID, a.directories[0].GID = containerID, containerID
			h := newDirectoryHarness(t, a)
			h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			h.deps.InspectDockerNamespace = func(context.Context, string) (*system.UserNamespace, error) {
				return &system.UserNamespace{
					PID: 123, Inode: 456, PeerUID: os.Geteuid(), PeerGID: os.Getegid(),
					UIDMap: []system.IDMap{{Inside: 0, Outside: uint64(os.Geteuid()), Length: 1}, {Inside: 1, Outside: 200000, Length: 65536}},
					GIDMap: []system.IDMap{{Inside: 0, Outside: uint64(os.Getegid()), Length: 1}, {Inside: 1, Outside: 300000, Length: 65536}},
				}, nil
			}
			h.deps.SetNamespaceMetadata = func(context.Context, *system.UserNamespace, string, int, int, os.FileMode) error {
				t.Fatal("directory preparation attempted namespace provisioning")
				return nil
			}
			err := h.install("one")
			if containerID == "0" {
				if err != nil {
					t.Fatalf("verified rootless zero identity did not map to the Project owner: %v", err)
				}
				info, _ := os.Stat(filepath.Join(h.root(), "apps", "one", "config"))
				if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
					t.Fatal("rootless zero was treated as host root")
				}
			} else {
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("subordinate ownership without host capability was accepted: %v", err)
				}
				if _, err := os.Lstat(filepath.Join(h.root(), "apps", "one", "config")); !os.IsNotExist(err) {
					t.Fatal("unsupported mapped ownership created a directory")
				}
				assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
			}
		})
	}
}

func TestDirectoryServiceBatchLaterFilesystemConflictCreatesNothing(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, conflict := range []string{"mode", "symlink", "ownership"} {
		t.Run(conflict, func(t *testing.T) {
			a := newDirectoryTestApp("one", 0o700)
			a.directories[0].Path = "first"
			later := a.directories[0]
			later.Path = "later"
			if conflict == "ownership" {
				if os.Geteuid() == 0 {
					t.Skip("ownership denial requires an unprivileged caller")
				}
				later.UID = strconv.Itoa(os.Geteuid() + 1)
			}
			a.directories = append(a.directories, later)
			h := newDirectoryHarness(t, a)
			parent := filepath.Join(h.root(), "apps", "one")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if conflict == "mode" {
				if err := os.Mkdir(filepath.Join(parent, "later"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else if conflict == "symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(parent, "later")); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("later path conflict was not Manual: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(parent, "first")); !os.IsNotExist(err) {
				t.Fatal("Service created an earlier directory before full filesystem admission")
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
		})
	}
}

func TestDirectoryServiceRejectsNativeUnknownACLModelWithoutCreation(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this regression exercises an unsupported native ACL model")
	}
	a := newDirectoryTestApp("one", 0o755)
	h := newDirectoryHarness(t, a)
	if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("fake Linux daemon metadata bypassed native ACL-model admission: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.root(), "apps", "one", "config")); !os.IsNotExist(err) {
		t.Fatal("unknown native ACL model created a directory")
	}
	if e := h.rec.find("one", "directories"); e == nil || !strings.Contains(e.Hint, "ACL") {
		t.Fatal("unknown ACL model did not persist an explicit Manual hint")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
}

func TestDirectoryServicePreservesPresentACLAndRejectsEntireBatch(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	a := newDirectoryTestApp("one", 0o755)
	dirs := a.directories
	a.directories = nil
	h := newDirectoryHarness(t, a)
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(h.root(), "apps", "one")
	leaf := filepath.Join(parent, "config")
	if err := os.Mkdir(leaf, 0o750); err != nil {
		t.Fatal(err)
	}
	acl := make([]byte, 44)
	binary.LittleEndian.PutUint32(acl, 2)
	entries := []struct {
		tag, permissions uint16
		id               uint32
	}{{1, 7, 0xffffffff}, {2, 4, uint32(os.Geteuid() + 1)}, {4, 0, 0xffffffff}, {16, 5, 0xffffffff}, {32, 0, 0xffffffff}}
	for i, entry := range entries {
		binary.LittleEndian.PutUint16(acl[4+i*8:], entry.tag)
		binary.LittleEndian.PutUint16(acl[6+i*8:], entry.permissions)
		binary.LittleEndian.PutUint32(acl[8+i*8:], entry.id)
	}
	if err := unix.Setxattr(leaf, "system.posix_acl_access", acl, 0); err != nil {
		t.Skipf("native ACL fixture cannot be installed: %v", err)
	}
	before, err := os.Stat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	first := dirs[0]
	first.Path = "first"
	a.directories = append([]app.Directory{first}, dirs...)
	callCount := len(h.runner.Calls())
	if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("present ACL was admitted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "first")); !os.IsNotExist(err) {
		t.Fatal("later ACL conflict allowed earlier directory creation")
	}
	after, _ := os.Stat(leaf)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("existing ACL-bearing directory was changed")
	}
	got := make([]byte, 128)
	n, err := unix.Getxattr(leaf, "system.posix_acl_access", got)
	if err != nil || string(got[:n]) != string(acl) {
		t.Fatal("ACL was reset or changed")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[callCount:])
	a.directories = dirs
	callCount = len(h.runner.Calls())
	if err := h.svc.Start(t.Context(), []string{"one"}); !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "system.posix_acl_access") {
		t.Fatalf("required-existing runtime validation admitted the ACL: %v", err)
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[callCount:])
}

func TestDirectoryRefreshAdmitsWholeBatchBeforeCreation(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	a := newDirectoryTestApp("one", 0o700)
	dirs := a.directories
	a.directories = nil
	h := newDirectoryHarness(t, a)
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(h.root(), "apps", "one")
	if err := os.Mkdir(filepath.Join(parent, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := dirs[0]
	first.Path = "first"
	a.directories = append([]app.Directory{first}, dirs...)
	a.image = "example/test:revised"
	before := len(h.runner.Calls())
	if err := h.svc.Update(context.Background(), []string{"one"}); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("refresh did not stop at later admission conflict: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "first")); !os.IsNotExist(err) {
		t.Fatal("refresh created the first path before admitting the entire batch")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[before:])
}

func TestDirectoryUnsupportedPlatformIsManualBeforePreparation(t *testing.T) {
	a := newDirectoryTestApp("one", 0o700)
	h := newDirectoryHarness(t, a)
	h.deps.GOOS = "darwin"
	if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unsupported mapping did not remain Manual: %v", err)
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
}

func TestDirectoryRemoteDaemonIsManualBeforePreparation(t *testing.T) {
	a := newDirectoryTestApp("one", 0o700)
	h := newDirectoryHarness(t, a)
	systemtest.SetEnv(h.deps, map[string]string{"DOCKER_HOST": "ssh://synthetic-host.invalid"})
	if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("remote ownership mapping did not remain Manual: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root(), "apps", "one", "config")); !os.IsNotExist(err) {
		t.Fatal("remote mapping created local storage")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
}

func TestDirectoryInstallRejectsUnverifiedMappingWithoutCreatingLeaf(t *testing.T) {
	for _, security := range []string{`["name=userns"]`, `["name=rootless"]`} {
		a := newDirectoryTestApp("one", 0o700)
		h := newDirectoryHarness(t, a)
		h.runner.On("docker info", system.Result{Stdout: []byte(security)})
		if err := h.install("one"); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("unverified mapping was accepted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(h.root(), "apps", "one", "config")); !os.IsNotExist(err) {
			t.Fatal("unverified mapping created a bind directory")
		}
		assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
	}
}
