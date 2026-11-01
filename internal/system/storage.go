package system

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func inspectStorage(path string) (Storage, error) {
	if runtime.GOOS != "linux" {
		return Storage{}, fmt.Errorf("mount evidence unavailable on %s", runtime.GOOS)
	}
	resolved, existing, err := storagePath(path)
	if err != nil {
		return Storage{}, err
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return Storage{}, fmt.Errorf("cannot read mount evidence: %w", err)
	}
	return storageFromMounts(resolved, existing, string(data))
}

func storagePath(path string) (string, string, error) {
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("storage path must be absolute")
	}
	path = filepath.Clean(path)
	existing := path
	for {
		info, err := os.Stat(existing)
		if err == nil {
			if !info.IsDir() && (existing != path || !info.Mode().IsRegular()) {
				return "", "", fmt.Errorf("storage ancestor is not a directory or regular file")
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("cannot inspect storage ancestor: %w", err)
		}
		if info, linkErr := os.Lstat(existing); linkErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("storage ancestor has an unresolved symlink")
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", "", fmt.Errorf("no existing storage ancestor")
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve storage ancestor: %w", err)
	}
	relative, err := filepath.Rel(existing, path)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(resolved, relative), resolved, nil
}

func storageFromMounts(path, existing, mounts string) (Storage, error) {
	storage := Storage{Path: path, Existing: existing}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(mounts, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, filesystem := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 6 || len(filesystem) < 3 {
			continue
		}
		mount := unescape.Replace(fields[4])
		if !filepath.IsAbs(mount) || (existing != mount && !strings.HasPrefix(existing, strings.TrimSuffix(mount, "/")+"/")) || len(mount) < len(storage.MountPoint) {
			continue
		}
		options := strings.Split(fields[5]+","+filesystem[2], ",")
		storage.MountPoint, storage.Type = mount, filesystem[0]
		storage.ReadOnly = slices.Contains(options, "ro")
		storage.NoExec = slices.Contains(options, "noexec")
	}
	if storage.MountPoint == "" {
		return Storage{}, fmt.Errorf("no backing mount found")
	}
	return storage, nil
}
