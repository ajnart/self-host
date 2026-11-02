package host

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ajnart/self-host/internal/system"
)

const unraidDocs = "https://github.com/ajnart/self-host/blob/main/docs/unraid.md"

var unraidVersion = regexp.MustCompile(`(?m)^version\s*=\s*["']?([0-9]+\.[0-9]+\.[0-9]+(?:[-.][A-Za-z0-9]+)*)["']?\s*$`)

func detectUnraid(deps *system.Deps) (string, bool) {
	if deps.GOOS != "linux" || deps.UnraidRelease == nil {
		return "", false
	}
	data, err := deps.UnraidRelease()
	if os.IsNotExist(err) {
		return "", false
	}
	if version := unraidVersion.FindSubmatch(data); err == nil && len(version) == 2 {
		return string(version[1]), true
	}
	return "", true
}

func Unraid(deps *system.Deps, home string) []Check {
	version, detected := detectUnraid(deps)
	if !detected {
		return nil
	}
	check := Check{ID: "unraid", Title: "Unraid", Level: Warn, Docs: unraidDocs,
		Detail: "Unraid identity/version could not be established from /etc/unraid-version; compatibility is unverified",
		Hint:   "Verify the Unraid version in the management interface. Detection is not tested support; review the provisional Unraid guide before installing."}
	if version != "" {
		check.Detail = "Unraid " + version + " from /etc/unraid-version; compatibility and reboot persistence are unverified"
	}
	return []Check{check, unraidStorage(deps, home)}
}

func unraidStorage(deps *system.Deps, home string) Check {
	check := Check{ID: "unraid-home", Title: "Home folder storage", Level: Warn, Docs: unraidDocs,
		Detail: "Home folder backing storage is unknown; persistence is unverified",
		Hint:   "Before approval, choose an absolute SELF_HOST_HOME on an available persistent mount; verify permissions and each selected App's database filesystem requirements. Recheck the same mount after reboot. Do not move an existing Project."}
	if home == "" || deps.Storage == nil {
		return check
	}
	storage, err := deps.Storage(home)
	if err != nil {
		check.Detail = fmt.Sprintf("%s: backing storage could not be inspected; persistence is unverified", home)
		return check
	}
	check.Detail = fmt.Sprintf("%s (resolved %s; existing ancestor %s) uses %s at %s; persistence and database suitability are unverified",
		home, storage.Path, storage.Existing, storage.Type, storage.MountPoint)
	switch strings.ToLower(storage.Type) {
	case "tmpfs", "ramfs", "rootfs":
		check.Detail += "; memory-backed storage will not retain the Project across reboot"
	case "overlay", "overlayfs", "aufs", "fuse", "fuse.shfs", "nfs", "nfs4", "cifs", "vfat", "exfat":
		check.Detail += "; layered, shared or non-POSIX storage needs App-specific verification"
	}
	if storage.ReadOnly {
		check.Level = Fail
		check.Detail += "; backing mount is read-only"
	}
	return check
}

func UnraidInstall(deps *system.Deps, home string) error {
	if _, detected := detectUnraid(deps); !detected {
		return nil
	}
	if deps.Getenv("SELF_HOST_HOME") == "" {
		return fmt.Errorf("Unraid installation needs an explicitly selected absolute SELF_HOST_HOME on persistent, App-suitable storage; review %s before approval", unraidDocs)
	}
	if deps.Storage == nil {
		return fmt.Errorf("Unraid backing storage cannot be inspected; review %s", unraidDocs)
	}
	storage, err := deps.Storage(home)
	if err != nil {
		return fmt.Errorf("Unraid Home folder backing storage is unknown; verify the mount before installing: %s", unraidDocs)
	}
	if err := unraidInstallStorage(storage); err != nil {
		return fmt.Errorf("Unraid Home folder: %w; choose verified App-suitable SELF_HOST_HOME storage, without relocating an existing Project", err)
	}
	switch storage.Type {
	case "xfs", "btrfs", "zfs", "ext2", "ext3", "ext4", "ext":
	default:
		return fmt.Errorf("Unraid Home folder filesystem %q is unverified for App databases; stop and verify an App-suitable local filesystem before installation: %s", storage.Type, unraidDocs)
	}
	if deps.Executable == nil {
		return fmt.Errorf("Unraid binary location cannot be established")
	}
	executable, err := deps.Executable()
	if err != nil || !filepath.IsAbs(executable) {
		return fmt.Errorf("Unraid binary location cannot be established")
	}
	binary, err := deps.Storage(executable)
	if err != nil {
		return fmt.Errorf("Unraid binary backing storage is unknown; install with SELF_HOST_INSTALL_DIR on verified persistent executable storage: %s", unraidDocs)
	}
	if err := unraidInstallStorage(binary); err != nil {
		return fmt.Errorf("Unraid binary: %w; select persistent executable storage with SELF_HOST_INSTALL_DIR", err)
	}
	if binary.NoExec {
		return fmt.Errorf("Unraid binary mount is noexec; select persistent executable storage with SELF_HOST_INSTALL_DIR")
	}
	return nil
}

func unraidInstallStorage(storage system.Storage) error {
	if storage.ReadOnly {
		return fmt.Errorf("backing mount is read-only")
	}
	switch storage.Type {
	case "rootfs", "tmpfs", "ramfs":
		return fmt.Errorf("backing mount is memory-backed and will not retain files across reboot")
	case "", "unknown", "overlay", "overlayfs", "aufs":
		return fmt.Errorf("backing mount persistence is unverified")
	}
	return nil
}
