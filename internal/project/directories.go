package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type AppDirectory struct {
	Path     string
	Mode     os.FileMode
	UID, GID int
}

func (p *Project) PrepareAppDirectories(id string, dirs []AppDirectory) error {
	return p.prepareAppDirectories(id, dirs, inspectAppDirectory, setAppDirectoryMetadata)
}

func (p *Project) prepareAppDirectories(id string, dirs []AppDirectory, inspect func(*os.File) error, apply func(*os.File, int, int, os.FileMode) error) error {
	if err := validAppID(id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if !safeAppID(dir.Path) || strings.ContainsRune(id+dir.Path, 0) || seen[dir.Path] || dir.Mode&^os.FileMode(0o777) != 0 || dir.Mode&0o700 != 0o700 || dir.UID < 0 || dir.GID < 0 || uint64(dir.UID) >= 1<<32-1 || uint64(dir.GID) >= 1<<32-1 {
			return errors.New("App directories require distinct relative components, owner rwx permission bits and valid host IDs")
		}
		seen[dir.Path] = true
	}
	if len(dirs) == 0 {
		return nil
	}
	if p.Owner.UID < 0 || p.Owner.GID < 0 || uint64(p.Owner.UID) >= 1<<32-1 || uint64(p.Owner.GID) >= 1<<32-1 {
		return errors.New("App directory preparation requires valid Project-owner IDs")
	}
	rootPath, err := filepath.Abs(p.Root)
	if err != nil {
		return err
	}
	gate, held := projectLocks.Load(rootPath)
	if !held || len(gate.(chan struct{})) != 1 {
		return &fs.PathError{Op: "require active Project mutation lock", Path: rootPath, Err: fs.ErrPermission}
	}
	anchor, err := openAppDirectoryAt(nil, string(filepath.Separator))
	if err != nil {
		return err
	}
	defer anchor.Close()
	if err := p.trustedAppDirectoryParent(anchor, inspect); err != nil {
		return err
	}
	for _, component := range strings.Split(strings.Trim(filepath.Dir(rootPath), string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, err := openAppDirectoryAt(anchor, component)
		if err != nil {
			return err
		}
		defer next.Close()
		if err := p.trustedAppDirectoryParent(next, inspect); err != nil {
			return err
		}
		anchor = next
	}
	root, err := openAppDirectoryAt(anchor, filepath.Base(rootPath))
	if err != nil {
		return err
	}
	defer root.Close()
	if err := p.trustedAppDirectoryParent(root, inspect); err != nil {
		return err
	}
	lockFD, err := unix.Openat(int(root.Fd()), ".lock", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "inspect Project lock", Path: ".lock", Err: err}
	}
	lock := os.NewFile(uintptr(lockFD), ".lock")
	defer lock.Close()
	lockInfo, err := lock.Stat()
	if err != nil {
		return err
	}
	lockOwner, ordinary := lockInfo.Sys().(*syscall.Stat_t)
	if !ordinary || !lockInfo.Mode().IsRegular() || int(lockOwner.Uid) != p.Owner.UID && lockOwner.Uid != 0 || lockInfo.Mode().Perm()&0o022 != 0 {
		return &fs.PathError{Op: "require ordinary nonshared Project lock", Path: ".lock", Err: fs.ErrPermission}
	}
	if err := inspect(lock); err != nil {
		return err
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err == nil {
		return &fs.PathError{Op: "require held kernel Project lock", Path: ".lock", Err: fs.ErrPermission}
	} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		return err
	}
	parent := root
	missingParent := false
	for _, component := range []string{"apps", id} {
		next, err := openAppDirectoryAt(parent, component)
		if errors.Is(err, fs.ErrNotExist) {
			missingParent = true
			break
		}
		if err != nil {
			return err
		}
		defer next.Close()
		if err := p.trustedAppDirectoryParent(next, inspect); err != nil {
			return err
		}
		parent = next
	}
	if missingParent {
		if err := appDirectoryOwnershipAllowed(p.Owner.UID, p.Owner.GID); err != nil {
			return err
		}
	}
	existing := make([]bool, len(dirs))
	for i, dir := range dirs {
		if !missingParent {
			file, err := openAppDirectoryAt(parent, dir.Path)
			if err == nil {
				err = inspect(file)
				info, statErr := file.Stat()
				file.Close()
				if err != nil {
					return err
				}
				if statErr != nil {
					return statErr
				}
				if !appDirectoryMetadata(info, dir.Mode, dir.UID, dir.GID) {
					return &fs.PathError{Op: "preserve incompatible App directory owner or mode", Path: dir.Path, Err: fs.ErrPermission}
				}
				existing[i] = true
				continue
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := appDirectoryOwnershipAllowed(dir.UID, dir.GID); err != nil {
			return &fs.PathError{Op: "admit new App directory ownership", Path: dir.Path, Err: err}
		}
	}
	parent = root
	for _, component := range []string{"apps", id} {
		next, err := openAppDirectoryAt(parent, component)
		if errors.Is(err, fs.ErrNotExist) {
			next, err = createAppDirectoryAt(parent, AppDirectory{component, 0o755, p.Owner.UID, p.Owner.GID}, inspect, apply)
		}
		if err != nil {
			return err
		}
		defer next.Close()
		if err := p.trustedAppDirectoryParent(next, inspect); err != nil {
			return err
		}
		parent = next
	}
	for i, dir := range dirs {
		if existing[i] {
			continue
		}
		file, err := createAppDirectoryAt(parent, dir, inspect, apply)
		if err != nil {
			return err
		}
		file.Close()
	}
	return nil
}

func openAppDirectoryAt(parent *os.File, name string) (*os.File, error) {
	parentFD := unix.AT_FDCWD
	if parent != nil {
		parentFD = int(parent.Fd())
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open ordinary App directory", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (p *Project) trustedAppDirectoryParent(file *os.File, inspect func(*os.File) error) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != p.Owner.UID && stat.Uid != 0 || info.Mode().Perm()&0o700 != 0o700 || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return &fs.PathError{Op: "require root or Project-owner controlled nonshared parent", Path: file.Name(), Err: fs.ErrPermission}
	}
	return inspect(file)
}

func inspectAppDirectory(file *os.File) error {
	if runtime.GOOS != "linux" {
		return &fs.PathError{Op: "require inspectable Linux filesystem and ACL semantics", Path: file.Name(), Err: fs.ErrPermission}
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return &fs.PathError{Op: "inspect App filesystem", Path: file.Name(), Err: errors.Join(fs.ErrPermission, err)}
	}
	if stat.Flags&1 != 0 {
		return &fs.PathError{Op: "require writable admitted App filesystem", Path: file.Name(), Err: fs.ErrPermission}
	}
	return admitAppDirectoryFilesystem(int64(stat.Type), func(attribute string) error {
		_, err := unix.Fgetxattr(int(file.Fd()), attribute, nil)
		return err
	})
}

func admitAppDirectoryFilesystem(kind int64, attributeError func(string) error) error {
	if kind != 0xef53 && kind != 0x58465342 && kind != 0x01021994 {
		return fmt.Errorf("filesystem 0x%x has unsupported App-directory ACL semantics: %w", kind, fs.ErrPermission)
	}
	for _, attribute := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if err := attributeError(attribute); !errors.Is(err, unix.ENODATA) {
			return fmt.Errorf("App directory requires inspectable absence of %s (inspection result: %v): %w", attribute, err, fs.ErrPermission)
		}
	}
	return nil
}

func appDirectoryOwnershipAllowed(uid, gid int) error {
	groups, err := os.Getgroups()
	if err != nil {
		return errors.Join(fs.ErrPermission, err)
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

func createAppDirectoryAt(parent *os.File, dir AppDirectory, inspect func(*os.File) error, apply func(*os.File, int, int, os.FileMode) error) (*os.File, error) {
	if err := unix.Mkdirat(int(parent.Fd()), dir.Path, 0o700); err != nil {
		return nil, &fs.PathError{Op: "create admitted App directory without replacement", Path: dir.Path, Err: err}
	}
	file, err := openAppDirectoryAt(parent, dir.Path)
	if err != nil {
		return nil, err
	}
	err = inspect(file)
	if err == nil {
		err = apply(file, dir.UID, dir.GID, dir.Mode)
	}
	if err == nil {
		info, statErr := file.Stat()
		err = statErr
		if err == nil && (!appDirectoryMetadata(info, dir.Mode, dir.UID, dir.GID) || info.Mode().Perm() != dir.Mode) {
			err = &fs.PathError{Op: "verify new App directory metadata", Path: dir.Path, Err: fs.ErrPermission}
		}
	}
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("preserve partly prepared directory %s: %w", dir.Path, err)
	}
	return file, nil
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
