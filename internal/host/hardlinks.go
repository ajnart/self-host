package host

import (
	"context"
	"errors"
	"io/fs"

	"github.com/ajnart/self-host/internal/system"
)

type HardlinkRequirement struct {
	App, Name string
	Sources   []string
	Library   string
}

// Hardlinks is an advisory host probe. App identity and Docker's mount view
// still need the importing App's own capability check during Setup/Check.
func Hardlinks(ctx context.Context, deps *system.Deps, requirements []HardlinkRequirement, copyAllowed bool) []Check {
	if len(requirements) == 0 {
		return nil
	}
	endpoint, err := ActiveDockerEndpoint(ctx, deps)
	local := err == nil && endpoint.Kind == "unix"
	var checks []Check
	for _, requirement := range requirements {
		check := Check{ID: "hardlinks", App: requirement.App, Title: requirement.Name + " hardlinks (host)", Level: OK,
			Detail: "Temporary hardlinks succeeded as the current host user; the importing App still checks its own permissions and container mounts.",
			Docs:   "https://trash-guides.info/File-and-Folder-Structure/Hardlinks-and-Instant-Moves/"}
		if !local || deps.Hardlink == nil {
			check.Level, check.Detail = Skipped, "Local media storage could not be verified for the active Docker endpoint; the importing App checks its own storage."
			checks = append(checks, check)
			continue
		}
		missing := false
		for _, source := range requirement.Sources {
			if err := ctx.Err(); err != nil {
				check.Level, check.Detail = Skipped, "Host hardlink check was cancelled."
				break
			}
			err := deps.Hardlink(source, requirement.Library)
			if errors.Is(err, fs.ErrNotExist) {
				missing = true
				continue
			}
			if err != nil {
				check.Level, check.Detail = Warn, "Host hardlink probe from "+source+" to "+requirement.Library+" failed: "+err.Error()
				check.Hint = "Review filesystem boundaries and folder permissions. The importing App will check again as its own user."
				if copyAllowed {
					check.Hint += " Copying is explicitly allowed and can use extra disk space and writes."
				} else {
					check.Hint += " To allow copies explicitly, choose Yes for copying when hardlinks fail in Settings."
				}
				break
			}
		}
		if missing && check.Level == OK {
			check.Level, check.Detail = Skipped, "Some selected download or library folders do not exist yet; hardlinks will be checked after the folders are created."
		}
		checks = append(checks, check)
	}
	return checks
}
