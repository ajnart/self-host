package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/system"
)

// MediaUsersSupported checks the active daemon's identity mapping before
// distinct container IDs are used for host bind mounts.
func MediaUsersSupported(ctx context.Context, deps *system.Deps) error {
	if deps.GOOS != "linux" {
		return fmt.Errorf("separate media App users require a Linux host with ordinary Docker identity mappings")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := deps.Runner.Run(ctx, system.Cmd{Name: "docker", Args: []string{"info", "--format", "{{json .SecurityOptions}}"}})
	if err != nil {
		return fmt.Errorf("inspect Docker identity mappings: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("could not inspect Docker identity mappings; separate media App users were not enabled")
	}
	var options []string
	if err := json.Unmarshal(result.Stdout, &options); err != nil || options == nil {
		return fmt.Errorf("Docker did not report usable identity mappings; separate media App users were not enabled")
	}
	for _, option := range options {
		for _, field := range strings.Split(option, ",") {
			if field == "name=rootless" || field == "name=userns" {
				return fmt.Errorf("separate media App users need an explicit ownership migration for rootless Docker or user-namespace remapping; keep MEDIA_SEPARATE_USERS=off")
			}
		}
	}
	return nil
}

// UnusedMediaUID chooses a numeric file owner without creating a host account.
// getent uses the host's configured identity sources, including directory
// accounts that static Go binaries cannot see through os/user.
func UnusedMediaUID(ctx context.Context, deps *system.Deps, start int, reserved map[int]bool) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := mediaUIDLookupReady(ctx, deps); err != nil {
		return 0, err
	}
	for id := start; id < 60000; id++ {
		if reserved[id] {
			continue
		}
		used, err := mediaUIDExists(ctx, deps, id)
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
	if id <= 0 {
		return fmt.Errorf("media App file owners must use positive IDs")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := mediaUIDLookupReady(ctx, deps); err != nil {
		return err
	}
	used, err := mediaUIDExists(ctx, deps, id)
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
