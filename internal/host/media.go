package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/system"
)

// MediaUsersSupported checks the active daemon's identity mapping before
// distinct container IDs are used for host bind mounts.
func MediaUsersSupported(ctx context.Context, deps *system.Deps) error {
	_, err := MediaUserNamespace(ctx, deps)
	return err
}

// MediaNamespace maps App identities to the actual host owners used by a
// verified local rootless Docker daemon. A nil namespace means ordinary Docker.
type MediaNamespace struct {
	deps *system.Deps
	raw  *system.UserNamespace
}

// MediaUserNamespace validates the active daemon before any directory or
// ownership changes. Rootless mappings come from its kernel namespace, never
// from guessed subordinate-ID allocations.
func MediaUserNamespace(ctx context.Context, deps *system.Deps) (*MediaNamespace, error) {
	if deps.GOOS != "linux" {
		return nil, fmt.Errorf("separate media App users require a Linux host with verified Docker identity mappings")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := deps.Runner.Run(ctx, system.Cmd{Name: "docker", Args: []string{"info", "--format", "{{json .SecurityOptions}}"}})
	if err != nil {
		return nil, fmt.Errorf("inspect Docker identity mappings: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("could not inspect Docker identity mappings; separate media App users were not enabled")
	}
	var options []string
	if err := json.Unmarshal(result.Stdout, &options); err != nil || options == nil {
		return nil, fmt.Errorf("Docker did not report usable identity mappings; separate media App users were not enabled")
	}
	rootless, remapped := false, false
	for _, option := range options {
		for _, field := range strings.Split(option, ",") {
			if field == "name=rootless" {
				rootless = true
			}
			if field == "name=userns" {
				remapped = true
			}
		}
	}
	if remapped {
		return nil, fmt.Errorf("separate media App users need an explicit ownership migration for Docker user-namespace remapping; keep MEDIA_SEPARATE_USERS=off")
	}
	endpoint, err := ActiveDockerEndpoint(ctx, deps)
	if err != nil {
		return nil, fmt.Errorf("verify the local Docker endpoint: %w; an explicit ownership migration is required for unverified mappings", err)
	}
	if endpoint.Kind != "unix" {
		return nil, fmt.Errorf("separate media App users require a local Docker Unix socket; remote Docker needs an explicit ownership migration on its host")
	}
	if !rootless {
		return nil, nil
	}
	if deps.InspectDockerNamespace == nil || deps.SetNamespaceMetadata == nil {
		return nil, fmt.Errorf("cannot verify the rootless Docker user namespace; an explicit ownership migration is required for unverified mappings")
	}
	parsed, err := url.Parse(endpoint.Host)
	if err != nil || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !filepath.IsAbs(parsed.Path) {
		return nil, fmt.Errorf("the local Docker Unix socket address is invalid")
	}
	raw, err := deps.InspectDockerNamespace(ctx, parsed.Path)
	if err != nil {
		return nil, fmt.Errorf("verify the local rootless Docker namespace: %w; an explicit ownership migration is required for unverified mappings", err)
	}
	if raw == nil || raw.PID <= 0 || raw.Inode == 0 {
		return nil, fmt.Errorf("the local rootless Docker daemon did not provide a usable user namespace")
	}
	if err := validateMediaIDMap(raw.UIDMap); err != nil {
		return nil, fmt.Errorf("Docker UID mapping: %w", err)
	}
	if err := validateMediaIDMap(raw.GIDMap); err != nil {
		return nil, fmt.Errorf("Docker GID mapping: %w", err)
	}
	owner, err := deps.Owner()
	if err != nil {
		return nil, fmt.Errorf("identify the Project owner: %w", err)
	}
	ns := &MediaNamespace{deps: deps, raw: raw}
	uid, uidErr := ns.HostUID(0)
	gid, gidErr := ns.HostGID(0)
	if uidErr != nil || gidErr != nil || owner.UID <= 0 || raw.PeerUID != owner.UID || uid != owner.UID || raw.PeerGID != gid {
		return nil, fmt.Errorf("the local rootless Docker namespace does not map its root to the Project owner; an explicit ownership migration is required")
	}
	return ns, nil
}

// HostUID translates a container identity to its actual host file owner.
func (ns *MediaNamespace) HostUID(id int) (int, error) {
	if ns == nil {
		return validMediaID(id)
	}
	return mapMediaID(id, ns.raw.UIDMap, false)
}

// HostGID translates a container group to its actual host file group.
func (ns *MediaNamespace) HostGID(id int) (int, error) {
	if ns == nil {
		return validMediaID(id)
	}
	return mapMediaID(id, ns.raw.GIDMap, false)
}

// SetMetadata applies both owner and mode to one newly created path. IDs are
// host IDs, matching stat comparisons by the Project's provisioning code.
func (ns *MediaNamespace) SetMetadata(ctx context.Context, path string, uid, gid int, mode os.FileMode) error {
	if ns == nil || ns.raw == nil || ns.deps.SetNamespaceMetadata == nil {
		return fmt.Errorf("the Docker user namespace is unavailable")
	}
	insideUID, err := mapMediaID(uid, ns.raw.UIDMap, true)
	if err != nil {
		return err
	}
	insideGID, err := mapMediaID(gid, ns.raw.GIDMap, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return ns.deps.SetNamespaceMetadata(ctx, ns.raw, path, insideUID, insideGID, mode)
}

func validMediaID(id int) (int, error) {
	if id < 0 || uint64(id) >= uint64(1)<<32-1 {
		return 0, fmt.Errorf("media identity %d is outside the supported numeric range", id)
	}
	return id, nil
}

func validateMediaIDMap(entries []system.IDMap) error {
	if len(entries) == 0 {
		return fmt.Errorf("the identity mapping is empty")
	}
	const end = uint64(1)<<32 - 1
	for i, entry := range entries {
		if entry.Length == 0 || entry.Length > end || entry.Inside >= end || entry.Outside >= end || entry.Inside > end-entry.Length || entry.Outside > end-entry.Length {
			return fmt.Errorf("the identity mapping contains an invalid range")
		}
		for _, previous := range entries[:i] {
			insideOverlap := entry.Inside < previous.Inside+previous.Length && previous.Inside < entry.Inside+entry.Length
			outsideOverlap := entry.Outside < previous.Outside+previous.Length && previous.Outside < entry.Outside+entry.Length
			if insideOverlap || outsideOverlap {
				return fmt.Errorf("the identity mapping contains overlapping ranges")
			}
		}
	}
	return nil
}

func mapMediaID(id int, entries []system.IDMap, reverse bool) (int, error) {
	if _, err := validMediaID(id); err != nil {
		return 0, err
	}
	if err := validateMediaIDMap(entries); err != nil {
		return 0, err
	}
	for _, entry := range entries {
		from, to := entry.Inside, entry.Outside
		if reverse {
			from, to = to, from
		}
		if uint64(id) >= from && uint64(id)-from < entry.Length {
			mapped := to + uint64(id) - from
			if mapped > uint64(^uint(0)>>1) {
				return 0, fmt.Errorf("media identity %d overflows this host's numeric range", id)
			}
			return int(mapped), nil
		}
	}
	return 0, fmt.Errorf("media identity %d is not mapped by the active Docker user namespace", id)
}

// UnusedMediaUID chooses a numeric file owner without creating a host account.
// getent uses the host's configured identity sources, including directory
// accounts that static Go binaries cannot see through os/user.
func UnusedMediaUID(ctx context.Context, deps *system.Deps, start int, reserved map[int]bool) (int, error) {
	return UnusedMediaUIDInNamespace(ctx, deps, start, reserved, nil)
}

// UnusedMediaUIDInNamespace chooses an App ID while checking the mapped host
// owner against all configured host identity sources.
func UnusedMediaUIDInNamespace(ctx context.Context, deps *system.Deps, start int, reserved map[int]bool, ns *MediaNamespace) (int, error) {
	if start <= 0 {
		return 0, fmt.Errorf("media App file owners must use positive IDs")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := mediaUIDLookupReady(ctx, deps); err != nil {
		return 0, err
	}
	for id := start; id < 60000; id++ {
		if reserved[id] {
			continue
		}
		hostID, err := ns.HostUID(id)
		if err != nil {
			return 0, err
		}
		used, err := mediaUIDExists(ctx, deps, hostID)
		if err != nil {
			return 0, err
		}
		if !used {
			return id, nil
		}
	}
	return 0, fmt.Errorf("no unused numeric media file owner is available")
}

// CheckUnusedMediaUID checks an archived identity without reallocating it.
// Restore must refuse collisions rather than granting an unrelated host user
// access to another App's private configuration.
func CheckUnusedMediaUID(ctx context.Context, deps *system.Deps, id int) error {
	return CheckUnusedMediaUIDInNamespace(ctx, deps, id, nil)
}

// CheckUnusedMediaUIDInNamespace verifies an archived App ID at the host ID it
// actually maps to, without changing the persisted container identity.
func CheckUnusedMediaUIDInNamespace(ctx context.Context, deps *system.Deps, id int, ns *MediaNamespace) error {
	if id <= 0 {
		return fmt.Errorf("media App file owners must use positive IDs")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := mediaUIDLookupReady(ctx, deps); err != nil {
		return err
	}
	hostID, err := ns.HostUID(id)
	if err != nil {
		return err
	}
	used, err := mediaUIDExists(ctx, deps, hostID)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("media file owner %d is already assigned on this host; an explicit ownership migration is required before Restore", id)
	}
	return nil
}

func mediaUIDLookupReady(ctx context.Context, deps *system.Deps) error {
	// Confirm that passwd lookup works before treating a missing UID as free.
	used, err := mediaUIDExists(ctx, deps, 0)
	if err != nil {
		return err
	}
	if !used {
		return fmt.Errorf("could not verify host identity sources; separate media App users were not enabled")
	}
	return nil
}

func mediaUIDExists(ctx context.Context, deps *system.Deps, id int) (bool, error) {
	result, err := deps.Runner.Run(ctx, system.Cmd{Name: "getent", Args: []string{"passwd", strconv.Itoa(id)}})
	if err != nil {
		return false, fmt.Errorf("check media file owner %d with getent passwd: %w; a working host identity lookup is required for separate media App users", id, err)
	}
	output := strings.TrimSpace(string(result.Stdout))
	if result.ExitCode == 2 && output == "" && len(result.Stderr) == 0 {
		return false, nil
	}
	fields := strings.Split(output, ":")
	if result.ExitCode != 0 || len(result.Stderr) != 0 || len(fields) != 7 || fields[0] == "" || fields[2] != strconv.Itoa(id) || strings.ContainsAny(output, "\r\n") {
		return false, fmt.Errorf("could not verify host identity for media file owner %d with getent passwd; separate media App users were not enabled", id)
	}
	return true, nil
}
