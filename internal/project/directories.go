package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func (p *Project) PrepareAppDirectory(id, rel string, mode os.FileMode, uid, gid int) error {
	if err := validAppID(id); err != nil {
		return err
	}
	if !safeAppID(rel) || strings.ContainsRune(id+rel, 0) || mode&^os.FileMode(0o777) != 0 || mode&0o700 != 0o700 || uid < 0 || gid < 0 || uint64(uid) >= 1<<32-1 || uint64(gid) >= 1<<32-1 {
		return errors.New("App directory requires one relative component, owner rwx permission bits and valid host IDs")
	}
	fd, err := unix.Open(p.Root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open Project directory", Path: p.Root, Err: err}
	}
	parent := os.NewFile(uintptr(fd), p.Root)
	defer parent.Close()
	for _, component := range []string{"apps", id} {
		var stat unix.Stat_t
		err := unix.Fstatat(int(parent.Fd()), component, &stat, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case err == nil:
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
				return &fs.PathError{Op: "inspect App directory parent", Path: component, Err: syscall.ENOTDIR}
			}
		case errors.Is(err, fs.ErrNotExist):
			if err := appDirectoryOwnershipAllowed(uid, gid); err != nil {
				return &fs.PathError{Op: "prepare App directory ownership", Path: rel, Err: err}
			}
			if err := prepareAppDirectoryAt(parent, component, 0o755, p.Owner.UID, p.Owner.GID, setAppDirectoryMetadata); err != nil {
				return err
			}
		default:
			return &fs.PathError{Op: "inspect App directory parent", Path: component, Err: err}
		}
		fd, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return &fs.PathError{Op: "open App directory parent", Path: component, Err: err}
		}
		parent = os.NewFile(uintptr(fd), component)
		defer parent.Close()
	}
	return prepareAppDirectoryAt(parent, rel, mode, uid, gid, setAppDirectoryMetadata)
}

func appDirectoryOwnershipAllowed(uid, gid int) error {
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	if !mediaOwnershipAllowed(uid, gid, os.Geteuid(), os.Getegid(), groups) {
		return fmt.Errorf("setting ownership %d:%d needs explicit host permission: %w", uid, gid, fs.ErrPermission)
	}
	return nil
}

func appDirectoryMetadata(info fs.FileInfo, mode os.FileMode, uid, gid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && int(stat.Uid) == uid && int(stat.Gid) == gid && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && info.Mode().Perm()&0o700 == 0o700 && info.Mode().Perm()&^mode == 0
}

func prepareAppDirectoryAt(parent *os.File, name string, mode os.FileMode, uid, gid int, apply func(*os.File, int, int, os.FileMode) error) error {
	var stat unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return &fs.PathError{Op: "inspect App directory", Path: name, Err: syscall.ENOTDIR}
		}
		if int(stat.Uid) != uid || int(stat.Gid) != gid || stat.Mode&0o700 != 0o700 || os.FileMode(stat.Mode)&0o7777&^mode.Perm() != 0 {
			return &fs.PathError{Op: "preserve incompatible App directory owner or mode", Path: name, Err: fs.ErrPermission}
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return &fs.PathError{Op: "inspect App directory", Path: name, Err: err}
	}
	if err := appDirectoryOwnershipAllowed(uid, gid); err != nil {
		return &fs.PathError{Op: "prepare App directory ownership", Path: name, Err: err}
	}
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return prepareAppDirectoryAt(parent, name, mode, uid, gid, apply)
		}
		return &fs.PathError{Op: "create App directory", Path: name, Err: err}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open new App directory", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	err = apply(file, uid, gid, mode)
	if err == nil {
		info, statErr := file.Stat()
		err = statErr
		if err == nil && (!appDirectoryMetadata(info, mode, uid, gid) || info.Mode().Perm() != mode) {
			err = &fs.PathError{Op: "verify new App directory metadata", Path: name, Err: fs.ErrPermission}
		}
	}
	if err != nil {
		var current unix.Stat_t
		info, statErr := file.Stat()
		if statErr == nil && unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW) == nil {
			original := info.Sys().(*syscall.Stat_t)
			if uint64(original.Dev) == uint64(current.Dev) && uint64(original.Ino) == uint64(current.Ino) {
				if cleanupErr := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); cleanupErr != nil {
					err = errors.Join(err, fmt.Errorf("preserve partly prepared directory %s: %w", name, cleanupErr))
				}
			}
		}
		return err
	}
	return nil
}

func setAppDirectoryMetadata(file *os.File, uid, gid int, mode os.FileMode) error {
	owner := -1
	if os.Geteuid() == 0 {
		owner = uid
	}
	if err := file.Chown(owner, gid); err != nil {
		return err
	}
	return file.Chmod(mode)
}
