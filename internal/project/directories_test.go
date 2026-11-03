package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/system"
	"golang.org/x/sys/unix"
)

func directoryProject(t *testing.T) *Project {
	t.Helper()
	p := &Project{Root: filepath.Join(trustedDirectoryFixture(t), "project"), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
	unlock, err := p.Lock(os.Getpid(), func(int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	return p
}

func trustedDirectoryFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".directory-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func prepareTestAppDirectory(p *Project, id, rel string, mode os.FileMode, uid, gid int) error {
	return p.prepareAppDirectories(id, []AppDirectory{{rel, mode, uid, gid}}, func(*os.File) error { return nil }, setAppDirectoryMetadata)
}

func TestAppDirectoryCreatesOwnedPrivateOrPublicLeafAndPreservesRerun(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			p := directoryProject(t)
			old := syscall.Umask(0o077)
			err := prepareTestAppDirectory(p, "sample", "config", mode, os.Geteuid(), os.Getegid())
			syscall.Umask(old)
			if err != nil {
				t.Fatal(err)
			}
			dir := p.Path("apps", "sample", "config")
			before, err := os.Stat(dir)
			if err != nil || before.Mode().Perm() != mode {
				t.Fatalf("new directory mode: %v, %v", before, err)
			}
			stat := before.Sys().(*syscall.Stat_t)
			if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
				t.Fatal("new directory does not belong to the selected runtime owner")
			}
			if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("preserved"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := prepareTestAppDirectory(p, "sample", "config", mode, os.Geteuid(), os.Getegid()); err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(dir)
			data, _ := os.ReadFile(filepath.Join(dir, "keep"))
			if !os.SameFile(before, after) || after.Mode() != before.Mode() || string(data) != "preserved" {
				t.Fatal("rerun changed an existing directory or its data")
			}
		})
	}
}

func TestAppDirectoryPreservesCompatibleModesAndRejectsConflicts(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		mode, want os.FileMode
		uid, gid   int
		accepted   bool
	}{
		{"private", 0o700, 0o700, os.Geteuid(), os.Getegid(), true},
		{"tighter", 0o700, 0o755, os.Geteuid(), os.Getegid(), true},
		{"group-readable", 0o750, 0o755, os.Geteuid(), os.Getegid(), true},
		{"not-private", 0o755, 0o700, os.Geteuid(), os.Getegid(), false},
		{"unwritable", 0o500, 0o755, os.Geteuid(), os.Getegid(), false},
		{"different-owner", 0o700, 0o700, os.Geteuid() + 1, os.Getegid(), false},
		{"different-group", 0o700, 0o700, os.Geteuid(), os.Getegid() + 1, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := directoryProject(t)
			dir := p.Path("apps", "sample", "data")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, scenario.mode); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(dir)
			err := prepareTestAppDirectory(p, "sample", "data", scenario.want, scenario.uid, scenario.gid)
			if (err == nil) != scenario.accepted {
				t.Fatalf("acceptance = %v, want %v: %v", err == nil, scenario.accepted, err)
			}
			after, _ := os.Stat(dir)
			if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.ModTime() != after.ModTime() {
				t.Fatal("existing metadata was changed")
			}
		})
	}
}

func TestAppDirectoryRefusesEverySymlinkBoundaryAndExistingFiles(t *testing.T) {
	for _, component := range []string{"root", "apps", "sample", "config"} {
		for _, kind := range []string{"symlink", "dangling", "file"} {
			t.Run(component+"/"+kind, func(t *testing.T) {
				p := directoryProject(t)
				parent := filepath.Dir(p.Root)
				name := filepath.Base(p.Root)
				if component != "root" {
					parent = p.Root
					for _, part := range []string{"apps", "sample", "config"} {
						name = part
						if part == component {
							break
						}
						parent = filepath.Join(parent, part)
						if err := os.Mkdir(parent, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				} else if err := os.Rename(p.Root, p.Root+"-preserved"); err != nil {
					t.Fatal(err)
				}
				conflict := filepath.Join(parent, name)
				target := t.TempDir()
				if kind == "file" {
					if err := os.WriteFile(conflict, []byte("preserve file"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					if kind == "dangling" {
						target = filepath.Join(target, "missing")
					}
					if err := os.Symlink(target, conflict); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.Lstat(conflict)
				if err := prepareTestAppDirectory(p, "sample", "config", 0o700, os.Geteuid(), os.Getegid()); err == nil {
					t.Fatal("non-directory or symlink was accepted")
				}
				after, _ := os.Lstat(conflict)
				if !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatal("conflicting path was changed")
				}
				if kind == "symlink" {
					entries, _ := os.ReadDir(target)
					if len(entries) != 0 {
						t.Fatal("symlink target was written")
					}
				}
			})
		}
	}
}

func TestAppDirectoryInvalidDeclarationsDoNotCreateAppNamespace(t *testing.T) {
	for _, rel := range []string{"", ".", "..", "../outside", "/absolute", "config/nested", `config\nested`} {
		p := directoryProject(t)
		if err := prepareTestAppDirectory(p, "sample", rel, 0o700, os.Geteuid(), os.Getegid()); err == nil {
			t.Fatalf("invalid directory accepted: %q", rel)
		}
		if _, err := os.Stat(p.Path("apps")); !os.IsNotExist(err) {
			t.Fatal("invalid declaration created directories")
		}
	}
}

func TestAppDirectoryMetadataFailureStartsPrivateAndPreservesEvenEmptyLeaf(t *testing.T) {
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	want := errors.New("metadata denied")
	_, err = createAppDirectoryAt(parent, AppDirectory{"data", 0o755, os.Geteuid(), os.Getegid()}, func(*os.File) error { return nil }, func(file *os.File, uid, gid int, mode os.FileMode) error {
		info, err := file.Stat()
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("new directory did not start private")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("metadata failure was lost: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(parent.Name(), "data")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("failed new directory was removed or lost its private mode")
	}
}

func TestAppDirectoryMetadataFailurePreservesNonemptyAndExistingDirectories(t *testing.T) {
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	_, err = createAppDirectoryAt(parent, AppDirectory{"data", 0o700, os.Geteuid(), os.Getegid()}, func(*os.File) error { return nil }, func(file *os.File, _, _ int, _ os.FileMode) error {
		if err := os.WriteFile(filepath.Join(parent.Name(), "data", "keep"), []byte("concurrent data"), 0o600); err != nil {
			t.Fatal(err)
		}
		return os.ErrPermission
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("metadata failure was hidden: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(parent.Name(), "data", "keep"))
	if err != nil || string(data) != "concurrent data" {
		t.Fatal("failed preparation deleted nonempty data")
	}
	_, err = createAppDirectoryAt(parent, AppDirectory{"data", 0o700, os.Geteuid(), os.Getegid()}, func(*os.File) error { return nil }, func(*os.File, int, int, os.FileMode) error {
		t.Fatal("existing directory reached metadata mutation")
		return nil
	})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("unexpected existing path was not held without replacement: %v", err)
	}
}

func TestAppDirectoryPreservesExistingParentModesAndChildFiles(t *testing.T) {
	p := directoryProject(t)
	parent := p.Path("apps", "sample")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(parent)
	if err := prepareTestAppDirectory(p, "sample", "config", 0o755, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(parent)
	if before.Mode() != after.Mode() || !os.SameFile(before, after) {
		t.Fatal("existing App namespace was changed")
	}
	for _, mode := range []os.FileMode{0, 0o600, 0o1700, os.ModeSetgid | 0o700} {
		if err := prepareTestAppDirectory(p, "sample", "bad", mode, os.Geteuid(), os.Getegid()); err == nil {
			t.Fatalf("invalid creation mode accepted: %v", mode)
		}
	}
}

func TestAppDirectoryNewDifferentRuntimeOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires host permission to set a different numeric owner")
	}
	p := directoryProject(t)
	if err := prepareTestAppDirectory(p, "sample", "config", 0o700, 23001, 23002); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p.Path("apps", "sample", "config"))
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 23001 || stat.Gid != 23002 || info.Mode().Perm() != 0o700 {
		t.Fatal("new directory was not assigned the selected numeric runtime owner")
	}
}

func TestAppDirectoryBatchAdmissionCreatesNothingForLaterFailure(t *testing.T) {
	for _, conflict := range []string{"mode", "file", "symlink", "ownership", "acl"} {
		t.Run(conflict, func(t *testing.T) {
			p := directoryProject(t)
			parent := p.Path("apps", "sample")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			later := AppDirectory{"later", 0o700, os.Geteuid(), os.Getegid()}
			inspect := func(*os.File) error { return nil }
			switch conflict {
			case "file":
				if err := os.WriteFile(filepath.Join(parent, "later"), []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(parent, "later")); err != nil {
					t.Fatal(err)
				}
			case "ownership":
				if os.Geteuid() == 0 {
					t.Skip("ordinary ownership denial requires an unprivileged caller")
				}
				later.UID++
			default:
				if err := os.Mkdir(filepath.Join(parent, "later"), 0o755); err != nil {
					t.Fatal(err)
				}
				if conflict == "acl" {
					later.Mode = 0o755
					inspect = func(file *os.File) error {
						if file.Name() == "later" {
							return os.ErrPermission
						}
						return nil
					}
				}
			}
			dirs := []AppDirectory{{"first", 0o700, os.Geteuid(), os.Getegid()}, later}
			err := p.prepareAppDirectories("sample", dirs, inspect, func(*os.File, int, int, os.FileMode) error {
				t.Fatal("admission failure reached metadata mutation")
				return nil
			})
			if err == nil {
				t.Fatal("later incompatible directory passed batch admission")
			}
			if _, err := os.Lstat(filepath.Join(parent, "first")); !os.IsNotExist(err) {
				t.Fatal("earlier missing directory was created before whole-batch admission")
			}
		})
	}
}

func TestAppDirectoryRejectsSharedParentsAndRequiresHeldLock(t *testing.T) {
	for _, component := range []string{"anchor", "root", "apps", "sample"} {
		t.Run(component, func(t *testing.T) {
			p := directoryProject(t)
			parent := p.Path("apps", "sample")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			path := map[string]string{"anchor": filepath.Dir(p.Root), "root": p.Root, "apps": p.Path("apps"), "sample": parent}[component]
			if err := os.Chmod(path, 0o775); err != nil {
				t.Fatal(err)
			}
			if err := prepareTestAppDirectory(p, "sample", "config", 0o700, os.Geteuid(), os.Getegid()); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("shared-writable parent was admitted: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(parent, "config")); !os.IsNotExist(err) {
				t.Fatal("shared namespace reached creation")
			}
		})
	}
	p := &Project{Root: t.TempDir(), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
	if err := prepareTestAppDirectory(p, "sample", "config", 0o700, os.Geteuid(), os.Getegid()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unlocked Project was admitted: %v", err)
	}
}

func TestAppDirectoryACLAdmissionRequiresKnownFilesystemAndExplicitAbsence(t *testing.T) {
	for _, kind := range []int64{0xef53, 0x58465342, 0x01021994} {
		if err := admitAppDirectoryFilesystem(kind, func(string) error { return unix.ENODATA }); err != nil {
			t.Fatalf("supported absent-ACL filesystem was rejected: %v", err)
		}
	}
	for _, attrErr := range []error{nil, unix.ENOTSUP, unix.EACCES, unix.EIO} {
		if err := admitAppDirectoryFilesystem(0xef53, func(string) error { return attrErr }); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("present or uncertain ACL was admitted: %v", err)
		}
	}
	if err := admitAppDirectoryFilesystem(0xef53, func(attribute string) error {
		if attribute == "system.posix_acl_default" {
			return nil
		}
		return unix.ENODATA
	}); !errors.Is(err, os.ErrPermission) {
		t.Fatal("present default ACL was accepted")
	}
	if err := admitAppDirectoryFilesystem(0x794c7630, func(string) error { t.Fatal("unknown ACL model was probed as POSIX"); return nil }); !errors.Is(err, os.ErrPermission) {
		t.Fatal("unknown filesystem ACL semantics were accepted")
	}
}

func TestAppDirectoryBatchOwnershipAdmissionDoesNotCreateNamespace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("ordinary ownership denial requires an unprivileged caller")
	}
	p := directoryProject(t)
	dirs := []AppDirectory{{"first", 0o700, os.Geteuid(), os.Getegid()}, {"later", 0o700, os.Geteuid() + 1, os.Getegid()}}
	if err := p.prepareAppDirectories("sample", dirs, func(*os.File) error { return nil }, setAppDirectoryMetadata); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("later ownership-setting failure was not held: %v", err)
	}
	if _, err := os.Lstat(p.Path("apps")); !os.IsNotExist(err) {
		t.Fatal("batch ownership admission created namespace directories")
	}
}

func TestAppDirectoryMetadataFailureNeverCleansReplacementPath(t *testing.T) {
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	_, err = createAppDirectoryAt(parent, AppDirectory{"data", 0o755, os.Geteuid(), os.Getegid()}, func(*os.File) error { return nil }, func(*os.File, int, int, os.FileMode) error {
		if err := os.Rename(filepath.Join(parent.Name(), "data"), filepath.Join(parent.Name(), "preserved-original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(parent.Name(), "data"), 0o750); err != nil {
			t.Fatal(err)
		}
		return os.ErrPermission
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("metadata error was lost: %v", err)
	}
	for name, mode := range map[string]os.FileMode{"data": 0o750, "preserved-original": 0o700} {
		info, err := os.Stat(filepath.Join(parent.Name(), name))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("error recovery cleaned or modified %s: %v", name, err)
		}
	}
}

func TestAppDirectoryForeignParentCannotReachMetadata(t *testing.T) {
	p := directoryProject(t)
	parent := p.Path("apps")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(parent, 23001, 23002); err != nil {
			t.Fatal(err)
		}
	} else {
		p.Owner.UID++
	}
	err := p.prepareAppDirectories("sample", []AppDirectory{{"config", 0o700, os.Geteuid(), os.Getegid()}}, func(*os.File) error { return nil }, func(*os.File, int, int, os.FileMode) error {
		t.Fatal("foreign-controlled namespace reached metadata mutation")
		return nil
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("foreign-controlled parent was accepted: %v", err)
	}
	if _, err := os.Lstat(p.Path("apps", "sample")); !os.IsNotExist(err) {
		t.Fatal("foreign namespace was written")
	}
}

func TestAppDirectoryRequiresKernelLockAsWellAsProcessGate(t *testing.T) {
	p := &Project{Root: filepath.Join(trustedDirectoryFixture(t), "project"), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
	if err := os.Mkdir(p.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path(".lock"), []byte("unlocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	projectLocks.Store(p.Root, gate)
	t.Cleanup(func() { projectLocks.Delete(p.Root) })
	if err := prepareTestAppDirectory(p, "sample", "config", 0o700, os.Geteuid(), os.Getegid()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("an occupied local gate without a kernel lock was accepted: %v", err)
	}
	if _, err := os.Lstat(p.Path("apps")); !os.IsNotExist(err) {
		t.Fatal("unlocked namespace was written")
	}
}

func TestAppDirectoryPublicBatchSupportsInspectedLinuxFilesystem(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("public ACL admission supports Linux only")
	}
	p := directoryProject(t)
	for path := p.Root; ; path = filepath.Dir(path) {
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
			t.Skip("native fixture has an unsupported filesystem prefix")
		}
		for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
			if _, err := unix.Fgetxattr(int(file.Fd()), name, nil); !errors.Is(err, unix.ENODATA) {
				file.Close()
				t.Skip("native fixture cannot independently establish ACL absence")
			}
		}
		file.Close()
		if filepath.Dir(path) == path {
			break
		}
	}
	if err := p.PrepareAppDirectories("sample", []AppDirectory{{"config", 0o700, os.Geteuid(), os.Getegid()}}); err != nil {
		t.Fatalf("supported public batch failed: %v", err)
	}
	if err := p.ValidateAppDirectories("sample", []AppDirectory{{"config", 0o700, os.Geteuid(), os.Getegid()}}); err != nil {
		t.Fatalf("supported public validation failed: %v", err)
	}
}

func TestAppDirectoryValidationRequiresEveryExistingComponentWithoutCreation(t *testing.T) {
	for _, depth := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+depth)), func(t *testing.T) {
			p := &Project{Root: filepath.Join(trustedDirectoryFixture(t), "project"), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
			path := filepath.Dir(p.Root)
			components := []string{"project", "apps", "sample", "config"}
			for _, component := range components[:depth] {
				path = filepath.Join(path, component)
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			missing := filepath.Join(path, components[depth])
			err := p.validateAppDirectories("sample", []AppDirectory{{"config", 0o700, os.Geteuid(), os.Getegid()}}, func(*os.File) error { return nil })
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing existing component passed validation: %v", err)
			}
			if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("validation created a missing component")
			}
		})
	}
}

func TestAppDirectoryValidationSharesCompatibilityAndNeverCreatesLockOrRepairs(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o750, 0o755} {
		for _, conflict := range []string{"none", "owner", "private", "acl", "parent", "symlink"} {
			t.Run(mode.String()+"/"+conflict, func(t *testing.T) {
				p := &Project{Root: trustedDirectoryFixture(t), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
				leaf := p.Path("apps", "sample", "config")
				if err := os.MkdirAll(leaf, mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(leaf, mode); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(leaf, "keep")
				if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
				dir := AppDirectory{"config", 0o755, os.Geteuid(), os.Getegid()}
				inspect := func(*os.File) error { return nil }
				accepted := conflict == "none" || conflict == "private" && mode == 0o700
				switch conflict {
				case "owner":
					dir.UID++
				case "private":
					dir.Mode = 0o700
				case "acl":
					inspect = func(file *os.File) error {
						if file.Name() == "config" {
							return os.ErrPermission
						}
						return nil
					}
				case "parent":
					if err := os.Chmod(p.Path("apps"), 0o775); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Rename(leaf, leaf+"-preserved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("config-preserved", leaf); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.Lstat(leaf)
				err := p.validateAppDirectories("sample", []AppDirectory{dir}, inspect)
				if (err == nil) != accepted {
					t.Fatalf("compatibility = %v, want %v: %v", err == nil, accepted, err)
				}
				after, _ := os.Lstat(leaf)
				data, readErr := os.ReadFile(marker)
				if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.ModTime() != after.ModTime() || readErr != nil || string(data) != "preserve" {
					t.Fatal("read-only validation changed existing storage")
				}
				if _, err := os.Lstat(p.Path(".lock")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("validation created a lock in the read-only view")
				}
			})
		}
	}
}

func TestAppDirectoryValidationRejectsInvalidBatchBeforeInspection(t *testing.T) {
	for _, invalid := range []AppDirectory{
		{"../escape", 0o700, os.Geteuid(), os.Getegid()},
		{"config/nested", 0o700, os.Geteuid(), os.Getegid()},
		{"config", 0o755, os.Geteuid(), os.Getegid()},
		{"later", 0o600, os.Geteuid(), os.Getegid()},
		{"later", os.ModeSetgid | 0o700, os.Geteuid(), os.Getegid()},
		{"later", 0o700, -1, os.Getegid()},
	} {
		p := &Project{Root: trustedDirectoryFixture(t), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
		dirs := []AppDirectory{{"config", 0o700, os.Geteuid(), os.Getegid()}, invalid}
		if err := p.validateAppDirectories("sample", dirs, func(*os.File) error {
			t.Fatal("invalid declaration reached filesystem inspection")
			return nil
		}); err == nil {
			t.Fatalf("invalid validation batch accepted: %+v", invalid)
		}
		if _, err := os.Lstat(p.Path("apps")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid validation batch created storage")
		}
	}
}
