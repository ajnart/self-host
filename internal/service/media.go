package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/project"
)

// These selected LinuxServer images document PUID/PGID. UsesMedia alone is
// not an identity contract; unrelated Apps retain their own image behavior.
var mediaIdentityApps = []string{"sonarr", "radarr", "qbittorrent", "jellyfin", "bazarr", "lidarr", "sabnzbd", "deluge", "transmission"}

func mediaUIDKey(id string) string { return strings.ToUpper(id) + "_PUID" }

// initializeMediaSettings applies only to new Projects. Absence of
// MEDIA_LAYOUT on an existing Project always retains the old paths.
func (s *Service) initializeMediaSettings(ctx context.Context, settings map[string]string) error {
	if settings["MEDIA_LAYOUT"] == "" {
		settings["MEDIA_LAYOUT"] = "shared"
	}
	if settings["MEDIA_SEPARATE_USERS"] == "" {
		settings["MEDIA_SEPARATE_USERS"] = "off"
	}
	if settings["MEDIA_COPY_FALLBACK"] == "" {
		settings["MEDIA_COPY_FALLBACK"] = "off"
	}
	if err := normalizeMediaSettings(settings); err != nil {
		return err
	}
	return s.ensureMediaIdentities(ctx, settings)
}

func normalizeMediaSettings(settings map[string]string) error {
	for _, key := range []string{"MEDIA_SEPARATE_USERS", "MEDIA_COPY_FALLBACK"} {
		if value := settings[key]; value != "" && value != "on" && value != "off" {
			return fmt.Errorf("%s must be on or off", key)
		}
	}
	layout := settings["MEDIA_LAYOUT"]
	if layout != "" && layout != "legacy" && layout != "shared" {
		return fmt.Errorf("MEDIA_LAYOUT must be shared or legacy")
	}
	if layout == "shared" {
		root := settings["DATA_ROOT"]
		if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
			return fmt.Errorf("DATA_ROOT must be an absolute folder other than the filesystem root")
		}
		settings["DATA_ROOT"] = filepath.Clean(root)
		settings["MEDIA_ROOT"] = filepath.Join(root, "media")
		settings["DOWNLOADS_ROOT"] = filepath.Join(root, "torrents")
	}
	settings["MEDIA_UMASK"] = settings["UMASK"]
	if settings["MEDIA_SEPARATE_USERS"] == "on" {
		settings["MEDIA_UMASK"] = "002"
	}
	return nil
}

func (s *Service) ensureMediaIdentities(ctx context.Context, settings map[string]string) error {
	if settings["MEDIA_SEPARATE_USERS"] != "on" {
		return nil
	}
	namespace, err := host.MediaUserNamespace(ctx, s.Deps)
	if err != nil {
		return err
	}
	reserved := map[int]bool{0: true}
	base, err := strconv.Atoi(settings["PUID"])
	if err != nil || base < 0 {
		return fmt.Errorf("PUID must be a non-negative integer")
	}
	if namespace != nil {
		if _, err := namespace.HostUID(base); err != nil {
			return err
		}
		gid, err := strconv.Atoi(settings["PGID"])
		if err != nil || gid < 0 {
			return fmt.Errorf("PGID must be a non-negative integer")
		}
		if _, err := namespace.HostGID(gid); err != nil {
			return err
		}
	}
	reserved[base] = true
	for _, id := range mediaIdentityApps {
		key := mediaUIDKey(id)
		if settings[key] == "" {
			continue
		}
		uid, err := strconv.Atoi(settings[key])
		if err != nil || uid <= 0 || reserved[uid] {
			return fmt.Errorf("%s must be a distinct positive media App file owner, separate from PUID", key)
		}
		if namespace != nil {
			if _, err := namespace.HostUID(uid); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		reserved[uid] = true
	}
	for _, id := range mediaIdentityApps {
		key := mediaUIDKey(id)
		if settings[key] != "" {
			continue
		}
		uid, err := host.UnusedMediaUIDInNamespace(ctx, s.Deps, 10001, reserved, namespace)
		if err != nil {
			return err
		}
		settings[key] = strconv.Itoa(uid)
		reserved[uid] = true
	}
	return nil
}

// validateMediaChange refuses silent path/ownership migrations. A boolean
// change cannot reconcile existing torrent records, ACLs or configuration.
func (s *Service) validateMediaChange(ctx context.Context, previous, next map[string]string, state *project.State) error {
	if next["MEDIA_LAYOUT"] == "shared" {
		for key, rel := range map[string]string{"MEDIA_ROOT": "media", "DOWNLOADS_ROOT": "torrents"} {
			if next[key] != previous[key] && next[key] != filepath.Join(next["DATA_ROOT"], rel) {
				return fmt.Errorf("%s is derived from DATA_ROOT for shared media storage; change DATA_ROOT instead", key)
			}
		}
	}
	if err := normalizeMediaSettings(next); err != nil {
		return err
	}
	for id := range state.Apps {
		a, found := s.Catalog.App(id)
		if !found || !a.Info().UsesMedia {
			continue
		}
		for _, key := range []string{"MEDIA_LAYOUT", "DATA_ROOT", "MEDIA_ROOT", "DOWNLOADS_ROOT", "MEDIA_SEPARATE_USERS"} {
			before, after := previous[key], next[key]
			if key == "MEDIA_LAYOUT" && before == "" {
				before = "legacy"
			}
			if key == "MEDIA_SEPARATE_USERS" && before == "" {
				before = "off"
			}
			if after == "" {
				after = before
			}
			if before != after {
				return fmt.Errorf("%s changes existing media App %s paths or file ownership; an explicit migration must preserve files, App records, ACLs and Docker identity mappings; nothing was changed", key, id)
			}
		}
		ownershipKeys := []string{"PUID", "PGID"}
		if next["MEDIA_SEPARATE_USERS"] == "on" {
			ownershipKeys = append(ownershipKeys, mediaUIDKey(id))
		}
		for _, key := range ownershipKeys {
			if previous[key] != next[key] {
				return fmt.Errorf("%s changes existing media file ownership; an explicit migration is required before changing this setting", key)
			}
		}
	}
	return s.ensureMediaIdentities(ctx, next)
}

func (ss *session) prepareMedia(ctx context.Context, apps []app.App) error {
	usesMedia := false
	identities := map[string]int{}
	for _, a := range apps {
		usesMedia = usesMedia || a.Info().UsesMedia
	}
	if !usesMedia {
		return nil
	}
	if err := ss.s.ensureMediaIdentities(ctx, ss.settings); err != nil {
		return err
	}
	if ss.settings["MEDIA_SEPARATE_USERS"] == "on" {
		for _, a := range apps {
			if value := ss.settings[mediaUIDKey(a.Info().ID)]; value != "" {
				identities[a.Info().ID], _ = strconv.Atoi(value)
			}
		}
	}
	uid, gid := ss.p.Owner.UID, ss.p.Owner.GID
	var setMetadata func(string, int, int, os.FileMode) error
	if ss.settings["MEDIA_SEPARATE_USERS"] == "on" {
		namespace, err := host.MediaUserNamespace(ctx, ss.s.Deps)
		if err != nil {
			return err
		}
		if namespace != nil {
			containerGID, err := strconv.Atoi(ss.settings["PGID"])
			if err != nil {
				return err
			}
			gid, err = namespace.HostGID(containerGID)
			if err != nil {
				return err
			}
			for id, containerUID := range identities {
				identities[id], err = namespace.HostUID(containerUID)
				if err != nil {
					return err
				}
			}
			setMetadata = func(path string, uid, gid int, mode os.FileMode) error {
				return namespace.SetMetadata(ctx, path, uid, gid, mode)
			}
		}
	} else {
		var err error
		uid, gid, err = ss.s.mediaHostOwner(ctx, ss.p, ss.settings)
		if err != nil {
			return err
		}
	}
	if err := ss.p.PrepareMediaDirectories(ss.settings, identities, uid, gid, setMetadata); err != nil {
		return err
	}
	if err := ss.p.SaveSettings(ss.settings); err != nil {
		return err
	}
	for _, check := range ss.mediaHardlinkChecks(ctx, apps) {
		event := events.StepOK
		if check.Level == host.Skipped {
			event = events.StepSkip
		} else if check.Level != host.OK {
			event = events.StepWarn
		}
		ss.s.Log.Emit(events.Event{Event: event, App: check.App, Step: check.ID, Msg: check.Title + ": " + check.Detail, Hint: check.Hint, Docs: check.Docs})
	}
	return nil
}

// mediaHostOwner resolves only the supported ownership mappings. Existing
// directory ownership stays untouched by Project preparation.
func (s *Service) mediaHostOwner(ctx context.Context, p *project.Project, settings map[string]string) (int, int, error) {
	if s.Deps.GOOS != "linux" {
		return p.Owner.UID, p.Owner.GID, nil
	}
	rootless, err := host.Rootless(ctx, s.Deps)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect Docker before preparing media ownership: %w", err)
	}
	uid, err := strconv.Atoi(settings["PUID"])
	if err != nil || uid < 0 {
		return 0, 0, fmt.Errorf("PUID must be a non-negative integer")
	}
	gid, err := strconv.Atoi(settings["PGID"])
	if err != nil || gid < 0 {
		return 0, 0, fmt.Errorf("PGID must be a non-negative integer")
	}
	if rootless {
		if uid != 0 || gid != 0 {
			return 0, 0, fmt.Errorf("rootless media file owners other than container 0:0 need explicit host ownership mappings; existing data was not changed")
		}
		return p.Owner.UID, p.Owner.GID, nil
	}
	if err := host.MediaUsersSupported(ctx, s.Deps); err != nil {
		return 0, 0, fmt.Errorf("media directory ownership needs explicit host mappings: %w", err)
	}
	return uid, gid, nil
}

func mediaSettingReviews(settings map[string]string) []SettingReview {
	layout := settings["MEDIA_LAYOUT"]
	if layout == "" {
		layout = "legacy"
	}
	rows := []SettingReview{{Key: "MEDIA_LAYOUT", Current: layout, Choices: []string{"legacy", "shared"}, Note: "Shared storage supports hardlinks; existing media paths require an explicit migration."}}
	if layout == "shared" {
		rows = append(rows, SettingReview{Key: "DATA_ROOT", Current: settings["DATA_ROOT"], Note: "One parent for media, torrents and Usenet; existing files are never moved automatically."})
	} else {
		for _, key := range []string{"MEDIA_ROOT", "DOWNLOADS_ROOT"} {
			rows = append(rows, SettingReview{Key: key, Current: settings[key]})
		}
	}
	for _, item := range []struct{ key, note string }{
		{"MEDIA_SEPARATE_USERS", "Optional Linux file owners, separate from App logins; shared PGID and umask 002. Existing ownership needs migration. Enabling needs permission to create privately owned configuration folders."},
		{"MEDIA_COPY_FALLBACK", "On acknowledges extra disk space and writes if the App cannot hardlink; off requires a successful capability probe."},
	} {
		current := settings[item.key]
		if current == "" {
			current = "off"
		}
		rows = append(rows, SettingReview{Key: item.key, Current: current, Choices: []string{"off", "on"}, Note: item.note})
	}
	return rows
}
