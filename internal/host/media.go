package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/user"
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
// Existing local/directory accounts and already reserved App IDs are skipped.
func UnusedMediaUID(start int, reserved map[int]bool) (int, error) {
	for id := start; id < 60000; id++ {
		if reserved[id] {
			continue
		}
		_, err := user.LookupId(strconv.Itoa(id))
		var unknown user.UnknownUserIdError
		if errors.As(err, &unknown) {
			return id, nil
		}
		if err != nil {
			return 0, fmt.Errorf("check media file owner %d: %w", id, err)
		}
	}
	return 0, fmt.Errorf("no unused numeric media file owner is available")
}
