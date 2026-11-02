package project

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/system"
)

func TestMediaPreparationPreservesLegacyFilesAndPermissions(t *testing.T) {
	home := t.TempDir()
	p := Open(system.User{Home: home, UID: os.Geteuid(), GID: os.Getegid()})
	media := filepath.Join(home, "library")
	if err := os.Mkdir(media, 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(media, "keep.txt")
	if err := os.WriteFile(data, []byte("existing media"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"MEDIA_ROOT": media, "DOWNLOADS_ROOT": filepath.Join(home, "downloads"), "PGID": strconv.Itoa(os.Getegid())}
	if err := p.PrepareMediaDirectories(settings, nil, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(data)
	if err != nil || string(got) != "existing media" {
		t.Fatalf("existing data changed: %q, %v", got, err)
	}
	info, _ := os.Stat(media)
	if info.Mode().Perm() != 0o700 {
		t.Fatal("existing directory permissions changed")
	}
}

func TestSharedMediaCreatesAllPathsAndPrivateConfigWithoutChangingExistingDirs(t *testing.T) {
	home := t.TempDir()
	p := Open(system.User{Home: home, UID: os.Geteuid(), GID: os.Getegid()})
	root := filepath.Join(home, "data")
	settings := map[string]string{"MEDIA_LAYOUT": "shared", "DATA_ROOT": root, "MEDIA_SEPARATE_USERS": "on", "PGID": strconv.Itoa(os.Getegid())}
	if err := p.PrepareMediaDirectories(settings, map[string]int{"sonarr": os.Geteuid()}, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"media/movies", "media/tv", "media/music", "torrents/movies", "usenet/complete", "usenet/incomplete"} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil || info.Mode().Perm() != 0o775 || info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("shared directory %s: %v, %v", rel, info, err)
		}
	}
	config, err := os.Stat(p.Path("apps", "sonarr", "config"))
	if err != nil || config.Mode().Perm() != 0o700 {
		t.Fatalf("configuration must stay private: %v, %v", config, err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.PrepareMediaDirectories(settings, nil, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("incompatible existing permissions should require migration")
	}
	info, _ := os.Stat(root)
	if info.Mode().Perm() != 0o700 {
		t.Fatal("existing permissions were silently changed")
	}
}

func TestMediaPreparationUsesExplicitHostOwnershipOnlyForNewDirectories(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing to another host file owner requires root")
	}
	home := t.TempDir()
	p := Open(system.User{Home: home, UID: os.Geteuid(), GID: os.Getegid()})
	root := filepath.Join(home, "data")
	settings := map[string]string{"MEDIA_LAYOUT": "shared", "DATA_ROOT": root, "MEDIA_SEPARATE_USERS": "off"}
	if err := p.PrepareMediaDirectories(settings, nil, 2201, 2202); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", "media", "torrents", "usenet/complete"} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != 2201 || int(stat.Gid) != 2202 {
			t.Fatalf("new storage %s uses %d:%d", rel, stat.Uid, stat.Gid)
		}
	}
	existing := filepath.Join(root, "media")
	if err := os.Chmod(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.PrepareMediaDirectories(settings, nil, 3301, 3302); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 2201 || stat.Gid != 2202 || info.Mode().Perm() != 0o700 {
		t.Fatal("ownership or mode of existing storage changed")
	}
}
