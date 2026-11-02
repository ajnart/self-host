package project

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/system"
)

func directoryProject(t *testing.T) *Project {
	t.Helper()
	return &Project{Root: t.TempDir(), Owner: system.User{UID: os.Geteuid(), GID: os.Getegid()}}
}

func TestAppDirectoryCreatesOwnedPrivateOrPublicLeafAndPreservesRerun(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			p := directoryProject(t)
			old := syscall.Umask(0o077)
			err := p.PrepareAppDirectory("sample", "config", mode, os.Geteuid(), os.Getegid())
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
			if err := p.PrepareAppDirectory("sample", "config", mode, os.Geteuid(), os.Getegid()); err != nil {
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
			err := p.PrepareAppDirectory("sample", "data", scenario.want, scenario.uid, scenario.gid)
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
				} else if err := os.Remove(p.Root); err != nil {
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
				if err := p.PrepareAppDirectory("sample", "config", 0o700, os.Geteuid(), os.Getegid()); err == nil {
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
		if err := p.PrepareAppDirectory("sample", rel, 0o700, os.Geteuid(), os.Getegid()); err == nil {
			t.Fatalf("invalid directory accepted: %q", rel)
		}
		if _, err := os.Stat(p.Path("apps")); !os.IsNotExist(err) {
			t.Fatal("invalid declaration created directories")
		}
	}
}

func TestAppDirectoryMetadataFailureStartsPrivateAndRemovesOnlyNewEmptyLeaf(t *testing.T) {
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	want := errors.New("metadata denied")
	err = prepareAppDirectoryAt(parent, "data", 0o755, os.Geteuid(), os.Getegid(), func(file *os.File, uid, gid int, mode os.FileMode) error {
		info, err := file.Stat()
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("new directory did not start private")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("metadata failure was lost: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent.Name(), "data")); !os.IsNotExist(err) {
		t.Fatal("failed empty new directory was left behind")
	}
}

func TestAppDirectoryMetadataFailurePreservesNonemptyAndExistingDirectories(t *testing.T) {
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	err = prepareAppDirectoryAt(parent, "data", 0o700, os.Geteuid(), os.Getegid(), func(file *os.File, _, _ int, _ os.FileMode) error {
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
	err = prepareAppDirectoryAt(parent, "data", 0o700, os.Geteuid(), os.Getegid(), func(*os.File, int, int, os.FileMode) error {
		t.Fatal("existing directory reached metadata mutation")
		return nil
	})
	if err != nil {
		t.Fatalf("compatible existing directory was not preserved on retry: %v", err)
	}
}

func TestAppDirectoryPreservesExistingParentModesAndChildFiles(t *testing.T) {
	p := directoryProject(t)
	parent := p.Path("apps", "sample")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(parent)
	if err := p.PrepareAppDirectory("sample", "config", 0o755, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(parent)
	if before.Mode() != after.Mode() || !os.SameFile(before, after) {
		t.Fatal("existing App namespace was changed")
	}
	for _, mode := range []os.FileMode{0, 0o600, 0o1700, os.ModeSetgid | 0o700} {
		if err := p.PrepareAppDirectory("sample", "bad", mode, os.Geteuid(), os.Getegid()); err == nil {
			t.Fatalf("invalid creation mode accepted: %v", mode)
		}
	}
}

func TestAppDirectoryNewDifferentRuntimeOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires host permission to set a different numeric owner")
	}
	p := directoryProject(t)
	if err := p.PrepareAppDirectory("sample", "config", 0o700, 23001, 23002); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p.Path("apps", "sample", "config"))
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 23001 || stat.Gid != 23002 || info.Mode().Perm() != 0o700 {
		t.Fatal("new directory was not assigned the selected numeric runtime owner")
	}
}
