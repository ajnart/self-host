package project

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ajnart/self-host/internal/system"
)

func TestMediaPreparationPreservesExistingDirectorySymlinks(t *testing.T) {
	home := t.TempDir()
	p := Open(system.User{Home: home, UID: os.Geteuid(), GID: os.Getegid()})
	target := filepath.Join(home, "mounted-library")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("existing media"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "media")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"MEDIA_ROOT": link, "MEDIA_SEPARATE_USERS": "off"}
	if err := p.PrepareMediaDirectories(settings, nil, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatalf("existing mounted-library link should remain usable: %v", err)
	}
	resolved, err := os.Readlink(link)
	if err != nil || resolved != target {
		t.Fatalf("existing link changed: %q, %v", resolved, err)
	}
	data, err := os.ReadFile(filepath.Join(target, "keep"))
	if err != nil || string(data) != "existing media" {
		t.Fatalf("existing target data changed: %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("existing target permissions changed: %v, %v", info, err)
	}
	settings["MEDIA_SEPARATE_USERS"] = "on"
	settings["PGID"] = strconv.Itoa(os.Getegid())
	if err := p.PrepareMediaDirectories(settings, nil, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("separate media users must retain strict symlink safety")
	}
}

func TestMediaPreparationRejectsInvalidExistingSymlinks(t *testing.T) {
	for _, targetKind := range []string{"file", "missing"} {
		t.Run(targetKind, func(t *testing.T) {
			home := t.TempDir()
			p := Open(system.User{Home: home, UID: os.Geteuid(), GID: os.Getegid()})
			target := filepath.Join(home, "target")
			if targetKind == "file" {
				if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(home, "media")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			settings := map[string]string{"MEDIA_ROOT": link}
			if err := p.PrepareMediaDirectories(settings, nil, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("invalid media symlink was accepted")
			}
			if _, err := os.Readlink(link); err != nil {
				t.Fatalf("invalid existing symlink was changed: %v", err)
			}
		})
	}
}

func TestMediaOwnershipAllowsSupplementaryGroupsWithoutChangingUsers(t *testing.T) {
	for _, test := range []struct {
		name       string
		uid, gid   int
		euid, egid int
		groups     []int
		allowed    bool
	}{
		{"effective group", 1000, 1000, 1000, 1000, nil, true},
		{"supplementary media group", 1000, 2200, 1000, 1000, []int{1000, 2200}, true},
		{"unowned group", 1000, 3300, 1000, 1000, []int{1000, 2200}, false},
		{"other file owner", 2000, 2200, 1000, 1000, []int{1000, 2200}, false},
		{"root provisions separate owner", 2000, 2200, 0, 0, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mediaOwnershipAllowed(test.uid, test.gid, test.euid, test.egid, test.groups); got != test.allowed {
				t.Fatalf("ownership permission = %t, want %t", got, test.allowed)
			}
		})
	}
}
