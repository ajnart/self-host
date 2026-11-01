package arr

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/apps/mediastorage"
	"github.com/ajnart/self-host/internal/helpers"
)

// Probe as the LinuxServer application user, across the same container mounts
// an import uses. Matching host devices alone cannot establish this capability.
const hardlinkProbe = `source=$(mktemp "$1/.self-host-hardlink.XXXXXX")
trap 'rm -f "$source"' EXIT HUP INT TERM
target="$2/.self-host-link-${source##*/}"
ln "$source" "$target"
trap 'rm -f "$target" "$source"' EXIT HUP INT TERM
[ "$(stat -c '%d:%i' "$source")" = "$(stat -c '%d:%i' "$target")" ]
printf 'hardlink-capable\n'
`

func (a App) checkHardlinks(ctx context.Context, h *helpers.H) error {
	if a.rootFolder == "" || !mediastorage.Shared(h.Settings) {
		return nil
	}
	enabled, err := a.hardlinksEnabled(ctx, h)
	if err != nil || !enabled {
		return fmt.Errorf("%s must enable Use Hardlinks instead of Copy in Settings > Media Management: %v", a.info.Name, err)
	}
	category := strings.TrimPrefix(a.rootFolder, "/media/")
	sources := []string{"/data/torrents", "/data/torrents/" + category}
	if h.Peer("sabnzbd") != nil {
		sources = append(sources, "/data/usenet/complete", "/data/usenet/complete/"+category)
	}
	for _, source := range sources {
		result, err := h.Exec(ctx, nil, "s6-setuidgid", "abc", "sh", "-eu", "-c", hardlinkProbe, "self-host-hardlink", source, a.libraryPath(h.Settings))
		if err == nil && result.ExitCode == 0 && strings.TrimSpace(string(result.Stdout)) == "hardlink-capable" {
			continue
		}
		if h.Settings["MEDIA_COPY_FALLBACK"] == "on" {
			h.Info("Hardlink capability could not be verified from " + source + " to " + a.libraryPath(h.Settings) + "; copying is explicitly allowed and uses additional disk space.")
			return nil
		}
		return fmt.Errorf("hardlink capability from %s to %s could not be verified as the App user; review shared mounts, filesystem boundaries and group permissions, or explicitly allow copying with self-host config set MEDIA_COPY_FALLBACK=on", source, a.libraryPath(h.Settings))
	}
	h.Info("Temporary hardlinks succeeded as the App user; an actual imported file still needs verification.")
	return nil
}

func (a App) mediaManagement(ctx context.Context, h *helpers.H) (map[string]any, error) {
	response, err := h.Expect(ctx, a.request(h, helpers.Request{Path: "/config/mediamanagement"}))
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := response.Decode(&config); err != nil {
		return nil, err
	}
	return config, nil
}

func (a App) hardlinksEnabled(ctx context.Context, h *helpers.H) (bool, error) {
	config, err := a.mediaManagement(ctx, h)
	return config["copyUsingHardlinks"] == true, err
}

func (a App) configureHardlinks(ctx context.Context, h *helpers.H) error {
	if !mediastorage.Shared(h.Settings) {
		return nil
	}
	return h.Step(ctx, "hardlink-imports", "use hardlinks for imports when storage supports them", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) { return a.hardlinksEnabled(ctx, h) },
		Do: func(ctx context.Context) error {
			config, err := a.mediaManagement(ctx, h)
			if err != nil {
				return err
			}
			id, ok := config["id"].(float64)
			if !ok || id < 1 {
				return fmt.Errorf("%s returned no media management configuration ID", a.info.Name)
			}
			config["copyUsingHardlinks"] = true
			_, err = h.Expect(ctx, a.request(h, helpers.Request{Method: "PUT", Path: "/config/mediamanagement/" + strconv.Itoa(int(id)), JSON: config}))
			return err
		},
		Hint: "In " + a.info.Name + " > Settings > Media Management > Importing enable Use Hardlinks instead of Copy.",
	})
}
