package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/emby"
	"github.com/ajnart/self-host/internal/apps/plex"
	"github.com/ajnart/self-host/internal/helpers"
)

func validateBackend(value string) error {
	switch value {
	case "", "jellyfin", "plex", "emby":
		return nil
	default:
		return errors.New("choose jellyfin, plex or emby")
	}
}

func selectedBackend(ctx context.Context, h *helpers.H) (string, public, error) {
	p, err := readPublic(ctx, h)
	if err != nil {
		return "", p, err
	}
	choice := h.Inputs["media_server"]
	if validateBackend(choice) != nil {
		return "", p, backendChoiceRequired()
	}
	current := map[int]string{1: "plex", 2: "jellyfin", 3: "emby"}[p.MediaServerType]
	saved, exists, err := readBackendIdentity(h)
	if err != nil {
		return "", p, err
	}
	if exists && saved.Backend != current {
		return "", p, &helpers.ManualError{Hint: "Seerr's configured backend differs from its recorded server identity. Review the interrupted connection or migration manually; the recorded connection was not adopted, reset or replaced."}
	}
	if current != "" {
		if choice != "" && choice != current {
			return "", p, &helpers.ManualError{Hint: "Seerr already uses " + current + ". Its backend and libraries were preserved. Restore the matching seerr.media_server input or review a separate migration before retrying self-host setup seerr; changing the input does not approve migration."}
		}
		return current, p, nil
	}
	if p.Initialized {
		return "", p, &helpers.ManualError{Hint: "Seerr reports completed Setup without a configured backend. Review its existing users and settings manually; no authentication or initialization was replayed."}
	}
	if choice != "" {
		return choice, p, nil
	}
	if h.Peer("jellyfin") != nil && h.Peer("plex") == nil && h.Peer("emby") == nil {
		return "jellyfin", p, nil
	}
	return "", p, backendChoiceRequired()
}

func backendChoiceRequired() error {
	return &helpers.ManualError{Hint: "Choose Seerr's intended media server with self-host install seerr --set seerr.media_server=plex --yes (or emby or jellyfin), then retry self-host setup seerr. Installing multiple servers never selects or initializes all of them."}
}

func backendApplicable(id string) func(context.Context, *helpers.H) (bool, error) {
	return func(ctx context.Context, h *helpers.H) (bool, error) {
		selected, _, err := selectedBackend(ctx, h)
		return selected == id, err
	}
}

type backendSettings struct {
	IP        string  `json:"ip"`
	Port      int     `json:"port"`
	UseSSL    *bool   `json:"useSsl"`
	URLBase   *string `json:"urlBase"`
	ServerID  string  `json:"serverId"`
	MachineID string  `json:"machineId"`
	APIKey    string  `json:"apiKey"`
}

type backendLibrary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled *bool  `json:"enabled"`
}

type backendIdentity struct {
	Backend  string `json:"backend"`
	ServerID string `json:"serverId"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

func backendPath(id string) string {
	if id == "emby" {
		return "/api/v1/settings/jellyfin"
	}
	return "/api/v1/settings/plex"
}

func backendRead(ctx context.Context, h *helpers.H, path string, result any) error {
	if h.Secret(h.Key("api_key")) == "" {
		return &helpers.ManualError{Hint: "Restore Seerr's existing API key through the Project credential boundary, then retry self-host setup seerr. No key was rotated."}
	}
	resp, err := h.HTTP(ctx, helpers.Request{Path: path, Header: keyHeader(h), NoRedirect: true})
	if err != nil {
		return err
	}
	if resp.Status == 401 || resp.Status == 403 {
		return &helpers.ManualError{Hint: "Seerr rejected its saved administrator API key or its original administrator is missing. Review /setup and restore the existing credential before retrying; no provider authentication or account replacement was attempted."}
	}
	if resp.Status != 200 || resp.Decode(result) != nil {
		return errors.New("Seerr did not return valid authenticated media-server settings; review the connection without resetting it")
	}
	return nil
}

func readBackendIdentity(h *helpers.H) (backendIdentity, bool, error) {
	raw := h.Secret(h.Key("backend_identity"))
	var identity backendIdentity
	if raw == "" {
		return identity, false, nil
	}
	if json.Unmarshal([]byte(raw), &identity) != nil || (identity.Backend != "plex" && identity.Backend != "emby") || identity.ServerID == "" || identity.Host == "" || identity.Port <= 0 || identity.Port > 65535 {
		return identity, true, &helpers.ManualError{Hint: "Seerr's saved media-server identity is incomplete. Review the connection and Project record manually; no ownership was adopted or released."}
	}
	return identity, true, nil
}

func checkBackendIdentity(h *helpers.H, identity backendIdentity) (bool, error) {
	saved, exists, err := readBackendIdentity(h)
	if err != nil {
		return exists, err
	}
	if exists && saved != identity {
		return true, &helpers.ManualError{Hint: "Seerr's verified server identity changed. Review the existing connection and recorded identity before retrying; no backend, credential or library selection was replaced."}
	}
	return exists, nil
}

func rememberBackendIdentity(h *helpers.H, identity backendIdentity) error {
	exists, err := checkBackendIdentity(h, identity)
	if err != nil || exists {
		return err
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return h.SetSecret(h.Key("backend_identity"), string(raw))
}

func verifyMediaBackend(ctx context.Context, from, to *helpers.H, id string) error {
	selected, p, err := selectedBackend(ctx, from)
	if err != nil || selected != id {
		return err
	}
	if p.MediaServerType == mediaServerNone || !p.Initialized {
		return &helpers.ManualError{Hint: "Open " + from.URL + "/setup, authorize the intended " + id + " server yourself, review its libraries and finish Setup, then run self-host setup seerr. Use " + to.InternalURL() + " internally. No provider login, claim, library sync, selection or initialization was automated."}
	}
	var configured backendSettings
	if err := backendRead(ctx, from, backendPath(id), &configured); err != nil {
		return err
	}
	from.Log.Secret(configured.APIKey)
	if to.App != id || to.Service == "" || to.ContainerPort <= 0 || to.ContainerPort > 65535 || configured.IP != to.Service || configured.Port != to.ContainerPort || configured.UseSSL == nil || *configured.UseSSL || (configured.URLBase != nil && *configured.URLBase != "") || (id == "emby" && configured.URLBase == nil) {
		return &helpers.ManualError{Hint: "Seerr's existing " + id + " endpoint does not match this Project's internal server. Review its media-server settings without replacing the connection or selected libraries."}
	}
	configuredID := configured.ServerID
	if id == "plex" {
		configuredID = configured.MachineID
	}
	if configuredID == "" {
		return backendIdentityMismatch()
	}
	identity := backendIdentity{Backend: id, ServerID: configuredID, Host: configured.IP, Port: configured.Port}
	if _, err := checkBackendIdentity(from, identity); err != nil {
		return err
	}
	var libraries []backendLibrary
	if err := backendRead(ctx, from, backendPath(id)+"/library", &libraries); err != nil {
		return err
	}
	if libraries == nil {
		return errors.New("Seerr returned no explicit library selection; review its media-server settings")
	}
	available := map[string]string{}
	serverID := ""
	if id == "plex" {
		var target []plex.Library
		serverID, target, err = plex.ReadServerLibraries(ctx, to)
		for _, library := range target {
			available[library.ID] = library.Type
		}
	} else {
		var target []emby.Library
		serverID, target, err = emby.ReadServerLibraries(ctx, to, configured.APIKey, configured.ServerID)
		for _, library := range target {
			available[library.ID] = library.Type
		}
	}
	if err != nil {
		return backendTargetError(id, err)
	}
	if configuredID != serverID {
		return backendIdentityMismatch()
	}
	if len(libraries) == 0 && len(available) > 0 {
		return &helpers.ManualError{Hint: "Seerr has no saved library choices for this server's available libraries. Review and select the intended libraries in Seerr manually; no inventory was synced or library enabled automatically."}
	}
	seen := map[string]bool{}
	for _, library := range libraries {
		if library.ID == "" || library.Name == "" || library.Enabled == nil || seen[library.ID] || available[library.ID] != library.Type || (library.Type != "movie" && library.Type != "show") {
			return &helpers.ManualError{Hint: "Seerr's saved library IDs or enabled flags do not match the selected server's authenticated inventory. Review the intended libraries manually; no library was synced, enabled, disabled or removed."}
		}
		seen[library.ID] = true
	}
	if id == "emby" {
		var users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}
		if err := backendRead(ctx, from, backendPath(id)+"/users", &users); err != nil {
			return err
		}
		if len(users) == 0 {
			return &helpers.ManualError{Hint: "Seerr did not read an authenticated Emby user through its saved connection. Review the existing authorization and internal endpoint; settings readback alone does not prove connectivity."}
		}
		seenUsers := map[string]bool{}
		for _, user := range users {
			if user.ID == "" || user.Username == "" || seenUsers[user.ID] {
				return errors.New("Seerr returned incomplete Emby user identities; review its saved connection")
			}
			seenUsers[user.ID] = true
		}
	}
	if err := rememberBackendIdentity(from, identity); err != nil {
		return err
	}
	if id == "plex" {
		return &helpers.ManualError{Hint: "Seerr's Plex server and library identities match, but saved settings and the Project's PMS token do not prove Seerr's own Plex authorization works. Verify the existing connection in Seerr's Plex settings manually. This Link remains Manual on retry because read-only Plex connectivity cannot be proved; no account login, settings test, library sync or scan was attempted."}
	}
	return nil
}

func backendIdentityMismatch() error {
	return &helpers.ManualError{Hint: "Seerr's saved media-server identity does not match the authenticated target. Review the connection before retrying; no backend or library selection was changed."}
}

func backendTargetError(id string, err error) error {
	if !errors.Is(err, helpers.ErrManual) {
		return err
	}
	hint := "Review Seerr's existing " + id + " media-server settings and the target's own Setup, then retry self-host setup " + id + " seerr. No connection credential or selected library was replaced."
	var manual *helpers.ManualError
	if errors.As(err, &manual) && manual.Hint != "" {
		hint = manual.Hint + " " + hint
	}
	return &helpers.ManualError{Hint: hint}
}

func removeMediaBackend(ctx context.Context, from, to *helpers.H, id string) error {
	saved, exists, err := readBackendIdentity(from)
	if err != nil {
		return err
	}
	if exists && saved.Backend == id {
		return &helpers.ManualError{Hint: "Seerr retains its recorded " + id + " connection, users, requests and selected libraries. Review this server's removal manually; Seerr exposes no safe connection-only disconnect, and the historical identity record was kept."}
	}
	p, err := readPublic(ctx, from)
	if err != nil {
		return err
	}
	if (id == "plex" && p.MediaServerType == 1) || (id == "emby" && p.MediaServerType == 3) {
		return &helpers.ManualError{Hint: "Seerr still uses this " + id + " server. Its user-owned connection and libraries were preserved; review removal manually rather than resetting its backend."}
	}
	return nil
}

func mediaBackendLink(id string) app.Link {
	name := "Plex"
	if id == "emby" {
		name = "Emby"
	}
	link := app.NewLink("link-"+id, id, "media server: "+name, app.LinkStep{
		Message: "verify the selected " + name + " server and preserve its libraries",
		Do:      func(ctx context.Context, from, to *helpers.H) error { return verifyMediaBackend(ctx, from, to, id) },
		Hint: func(from, to *helpers.H) string {
			return "Review " + from.URL + "/setup and media-server settings for " + to.Service + " port " + strconv.Itoa(to.ContainerPort) + "; keep the existing authorization and selected libraries, then retry self-host setup seerr."
		},
	}, &app.LinkStep{
		Message: "review removal of the " + name + " media server",
		Do:      func(ctx context.Context, from, to *helpers.H) error { return removeMediaBackend(ctx, from, to, id) },
	})
	link.Applicable = backendApplicable(id)
	return link
}
