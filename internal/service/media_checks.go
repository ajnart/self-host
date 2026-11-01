package service

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/host"
)

func (s *Service) mediaHardlinkChecks(ctx context.Context, settings map[string]string, apps []app.App) []host.Check {
	if settings["MEDIA_LAYOUT"] != "shared" {
		return nil
	}
	if !filepath.IsAbs(settings["DATA_ROOT"]) {
		return []host.Check{{ID: "hardlinks", Title: "Media hardlinks (host)", Level: host.Warn,
			Detail: "The shared data folder must be an absolute path before hardlinks can be checked."}}
	}
	usenet := slices.ContainsFunc(apps, func(a app.App) bool { return a.Info().ID == "sabnzbd" })
	var requirements []host.HardlinkRequirement
	for _, a := range apps {
		info := a.Info()
		category := ""
		switch info.ID {
		case "sonarr":
			category = "tv"
		case "radarr":
			category = "movies"
		case "lidarr":
			category = "music"
		default:
			continue
		}
		root := settings["DATA_ROOT"]
		sources := []string{filepath.Join(root, "torrents"), filepath.Join(root, "torrents", category)}
		if usenet {
			sources = append(sources, filepath.Join(root, "usenet", "complete"), filepath.Join(root, "usenet", "complete", category))
		}
		requirements = append(requirements, host.HardlinkRequirement{App: info.ID, Name: info.Name,
			Sources: sources, Library: filepath.Join(root, "media", category)})
	}
	return host.Hardlinks(ctx, s.Deps, requirements, settings["MEDIA_COPY_FALLBACK"] == "on")
}

func (ss *session) mediaHardlinkChecks(ctx context.Context, selected []app.App) []host.Check {
	apps := slices.Clone(selected)
	for _, a := range ss.s.Catalog.Apps() {
		id := a.Info().ID
		if ss.state.Apps[id] != nil && !slices.ContainsFunc(apps, func(selected app.App) bool { return selected.Info().ID == id }) {
			apps = append(apps, a)
		}
	}
	return ss.s.mediaHardlinkChecks(ctx, ss.settings, apps)
}
