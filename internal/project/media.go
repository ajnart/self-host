package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
)

// PrepareMediaDirectories creates only missing storage and configuration
// directories. It never changes ownership or modes on existing data.
// identities contains only Apps whose selected image supports PUID/PGID.
// A metadata setter provisions mapped host IDs inside a verified user namespace.
func (p *Project) PrepareMediaDirectories(settings map[string]string, identities map[string]int, uid, gid int, metadata ...func(string, int, int, os.FileMode) error) error {
	if uid < 0 || gid < 0 {
		return fmt.Errorf("media host ownership must use non-negative IDs")
	}
	var setMetadata func(string, int, int, os.FileMode) error
	if len(metadata) > 0 {
		setMetadata = metadata[0]
	}
	separate := settings["MEDIA_SEPARATE_USERS"] == "on"
	// The caller resolves container IDs to host ownership. Rootless container
	// 0:0 maps to the Project owner; ordinary rootful Docker uses PUID/PGID.
	mode := os.FileMode(0o755)
	if separate {
		uid = p.Owner.UID
		if setMetadata == nil {
			var err error
			gid, err = strconv.Atoi(settings["PGID"])
			if err != nil || gid < 0 {
				return fmt.Errorf("PGID must be a non-negative integer")
			}
		}
		mode = 0o775 | os.ModeSetgid
	}
	var dirs []string
	if settings["MEDIA_LAYOUT"] == "shared" {
		root := settings["DATA_ROOT"]
		for _, rel := range []string{"", "media", "media/movies", "media/tv", "media/music", "torrents", "torrents/movies", "torrents/tv", "torrents/music", "usenet", "usenet/incomplete", "usenet/complete", "usenet/complete/movies", "usenet/complete/tv", "usenet/complete/music"} {
			dirs = append(dirs, filepath.Join(root, rel))
		}
	} else {
		for _, key := range []string{"MEDIA_ROOT", "DOWNLOADS_ROOT"} {
			if settings[key] != "" {
				dirs = append(dirs, settings[key])
			}
		}
	}
	for _, dir := range dirs {
		if err := p.prepareMediaDirectory(dir, uid, gid, mode, separate, setMetadata); err != nil {
			return err
		}
	}
	if !separate {
		return nil
	}
	for id, uid := range identities {
		if err := p.MkdirAll("apps", id); err != nil {
			return err
		}
		if err := p.prepareMediaDirectory(p.Path("apps", id, "config"), uid, gid, 0o700, true, setMetadata); err != nil {
			return err
		}
	}
	return nil
}

func (p *Project) prepareMediaDirectory(dir string, uid, gid int, mode os.FileMode, checkExisting bool, setMetadata func(string, int, int, os.FileMode) error) error {
	info, err := os.Lstat(dir)
	if err == nil {
		if !checkExisting && info.Mode()&os.ModeSymlink != 0 {
			// Existing media locations may point at mounted storage. The
			// ordinary ownership mode preserves both the link and its target.
			info, err = os.Stat(dir)
			if err != nil {
				return fmt.Errorf("resolve media directory %s: %w", dir, err)
			}
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("media path %s must be an ordinary directory", dir)
		}
		if !checkExisting {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		valid := ok && int(stat.Gid) == gid
		if mode.Perm() == 0o700 {
			valid = valid && int(stat.Uid) == uid && info.Mode().Perm() == mode.Perm()
		} else {
			valid = valid && info.Mode().Perm()&0o070 == 0o070 && info.Mode()&os.ModeSetgid != 0
		}
		if !valid {
			return fmt.Errorf("%s has existing ownership or permissions incompatible with separate media App users; review ownership, ACLs and namespace mappings in an explicit migration; existing data was not changed", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if setMetadata == nil && os.Geteuid() != 0 {
		groups, err := os.Getgroups()
		if err != nil {
			return fmt.Errorf("inspect media directory group permissions: %w", err)
		}
		if !mediaOwnershipAllowed(uid, gid, os.Geteuid(), os.Getegid(), groups) {
			return fmt.Errorf("creating %s for media file owner %d:%d needs permission to set ownership; rerun the approved install with sudo or provision the directory explicitly", dir, uid, gid)
		}
	}
	if err := p.mkdirAll(dir); err != nil {
		return err
	}
	if setMetadata != nil {
		if err := setMetadata(dir, uid, gid, mode); err != nil {
			// This directory was just created and must still be empty. Avoid
			// leaving a partly provisioned owner that makes retry ambiguous.
			_ = os.Remove(dir)
			return fmt.Errorf("provision media directory %s: %w", dir, err)
		}
		return nil
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
	} else if err := os.Chown(dir, -1, gid); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}

func mediaOwnershipAllowed(uid, gid, euid, egid int, groups []int) bool {
	if euid == 0 {
		return true
	}
	return uid == euid && (gid == egid || slices.Contains(groups, gid))
}
