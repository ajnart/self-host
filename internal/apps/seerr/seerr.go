package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/mediastorage"
	"github.com/ajnart/self-host/internal/helpers"
)

// mediaServerJellyfin is Seerr's mediaServerType for Jellyfin; 4 means
// none is configured yet.
const (
	mediaServerJellyfin = 2
	mediaServerNone     = 4
)

// App is Seerr (v3.x at the time of writing). Traps from docs/seerr.md:
// other Apps are reached by Compose service name, never localhost; the
// Jellyfin sign-in needs an email; requests fail unless a default server,
// quality profile and root folder are set; and Seerr refuses a second
// Jellyfin hostname once one is configured.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "seerr", Name: "Seerr", Category: "Requests",
		Description:   "Let your family ask for movies and shows to be added",
		Image:         "ghcr.io/seerr-team/seerr:latest",
		Docs:          "https://docs.seerr.dev/getting-started/docker/",
		Port:          5055,
		ContainerPort: 5055,
		Service:       "seerr",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "sonarr_profile", Prompt: "Sonarr quality profile ID or exact name", RecoveryOnly: true, Help: "Optional when Sonarr has one profile. With multiple profiles, choose an upstream profile ID or unique exact name; existing connections are preserved."},
		{Key: "radarr_profile", Prompt: "Radarr quality profile ID or exact name", RecoveryOnly: true, Help: "Optional when Radarr has one profile. With multiple profiles, choose an upstream profile ID or unique exact name; existing connections are preserved."},
	}
}

// Secrets pins Seerr's API key: the container adopts API_KEY on start.
func (App) Secrets() []app.SecretSpec { return []app.SecretSpec{{Name: "api_key", Length: 32}} }

// The image defaults to node (1000:1000); configured IDs keep its config
// writable when rootless Docker maps host-owned folders to container 0:0.
func (App) Files(app.RenderContext, func(string) string) []app.File {
	return []app.File{{Path: "config/.keep", Mode: 0o644}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  seerr:
    image: ghcr.io/seerr-team/seerr:latest
    init: true
    restart: unless-stopped
    user: ${PUID}:${PGID}
    environment:
      TZ: ${TZ}
      LOG_LEVEL: info
      API_KEY: ${SEERR_API_KEY}
    volumes:
      - ./config:/app/config
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:5055
    healthcheck:
      test: ["CMD-SHELL", "wget --no-verbose --tries=1 --spider http://localhost:5055/api/v1/status || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 30s
`
}

func (App) Links() []app.Link {
	return []app.Link{jellyfinLink(), serverLink(sonarr), serverLink(radarr)}
}

func keyHeader(h *helpers.H) map[string]string {
	return map[string]string{"X-Api-Key": h.Secret(h.Key("api_key"))}
}

type public struct {
	Initialized     bool `json:"initialized"`
	MediaServerType int  `json:"mediaServerType"`
}

func readPublic(ctx context.Context, h *helpers.H) (public, error) {
	var p public
	resp, err := h.Expect(ctx, helpers.Request{Path: "/api/v1/settings/public"})
	if err != nil {
		return p, err
	}
	return p, resp.Decode(&p)
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	return h.Ready(ctx, "/api/v1/status")
}

// Check proves Seerr answers; whether it is connected is the Links' job.
func (App) Check(ctx context.Context, h *helpers.H) error {
	_, err := readPublic(ctx, h)
	return err
}

// signIn signs in to Seerr with the Jellyfin Admin login. The first time it
// also binds Jellyfin (by service name) and creates Seerr's admin.
func signIn(ctx context.Context, from, to *helpers.H, p public) (*helpers.Session, error) {
	body := map[string]any{"username": from.AdminUser(), "password": from.AdminPassword()}
	if p.MediaServerType == mediaServerNone {
		body["hostname"], body["port"], body["useSsl"], body["urlBase"] = to.Service, to.ContainerPort, false, ""
		body["serverType"], body["email"] = mediaServerJellyfin, from.AdminUser()+"@selfhost.local"
	}
	s := from.Session()
	resp, err := s.HTTP(ctx, helpers.Request{Method: "POST", Path: "/api/v1/auth/jellyfin", JSON: body})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("Jellyfin sign-in returned HTTP %d: %s", resp.Status, strings.TrimSpace(string(resp.Body)))
	}
	if s.Cookie("connect.sid") == "" {
		return nil, errors.New("Seerr set no session cookie")
	}
	return s, nil
}

type jellyfinLibrary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func syncLibraries(ctx context.Context, h *helpers.H) ([]jellyfinLibrary, error) {
	resp, err := h.HTTP(ctx, helpers.Request{Method: "POST", Path: "/api/v1/settings/jellyfin/library/sync", Header: keyHeader(h)})
	if err != nil {
		return nil, err
	}
	if resp.Status == 404 {
		return nil, errors.New("Jellyfin has no libraries yet")
	}
	if !resp.OK() {
		return nil, fmt.Errorf("library sync returned HTTP %d", resp.Status)
	}
	var libs []jellyfinLibrary
	return libs, resp.Decode(&libs)
}

func verifyJellyfinTarget(ctx context.Context, from, to *helpers.H) error {
	resp, err := from.Expect(ctx, helpers.Request{Path: "/api/v1/settings/jellyfin", Header: keyHeader(from)})
	if err != nil {
		return err
	}
	var configured struct {
		IP       string `json:"ip"`
		Port     int    `json:"port"`
		UseSSL   *bool  `json:"useSsl"`
		URLBase  string `json:"urlBase"`
		ServerID string `json:"serverId"`
	}
	if err := resp.Decode(&configured); err != nil {
		return err
	}
	if to.App != "jellyfin" || configured.IP != to.Service || configured.Port != to.ContainerPort || configured.UseSSL == nil || *configured.UseSSL || configured.URLBase != "" || configured.ServerID == "" {
		from.Info("Seerr's existing Jellyfin connection does not match this Project. Review its media-server settings; no connection or library selection was changed.")
		return helpers.ErrManual
	}
	resp, err = to.Expect(ctx, helpers.Request{Path: "/System/Info/Public"})
	if err != nil {
		return err
	}
	var target struct {
		ID string `json:"Id"`
	}
	if err := resp.Decode(&target); err != nil {
		return err
	}
	if target.ID == "" || target.ID != configured.ServerID {
		from.Info("Seerr's saved Jellyfin server identity does not match the target. Review the connection before retrying; no settings were changed.")
		return helpers.ErrManual
	}
	return nil
}

func connected(ctx context.Context, h, to *helpers.H) (bool, error) {
	p, err := readPublic(ctx, h)
	if err != nil || !p.Initialized || p.MediaServerType != mediaServerJellyfin {
		return false, err
	}
	err = verifyJellyfinTarget(ctx, h, to)
	return err == nil, err
}

func connect(ctx context.Context, from, to *helpers.H) error {
	p, err := readPublic(ctx, from)
	if err != nil {
		return err
	}
	if p.MediaServerType != mediaServerNone && p.MediaServerType != mediaServerJellyfin {
		from.Info("Seerr is already set up with another media server; its connection and library selections were left unchanged.")
		return helpers.ErrManual
	}
	if p.MediaServerType == mediaServerJellyfin {
		if err := verifyJellyfinTarget(ctx, from, to); err != nil {
			return err
		}
		if p.Initialized {
			return nil
		}
		from.Info("Seerr's Jellyfin Setup is unfinished. Open " + from.URL + "/setup, review the selected libraries and finish Setup, then run self-host setup seerr. Existing selections were left unchanged; an interrupted library sync is not completion.")
		return helpers.ErrManual
	}
	session, err := signIn(ctx, from, to, p)
	if err != nil {
		return err
	}
	libs, err := syncLibraries(ctx, from)
	if err != nil {
		return err
	}
	for _, l := range libs {
		if l.Enabled {
			continue
		}
		if _, err := from.Expect(ctx, helpers.Request{
			Method: "PUT", Path: "/api/v1/settings/jellyfin/library/" + l.ID, Header: keyHeader(from),
			JSON: map[string]bool{"enabled": true},
		}); err != nil {
			return err
		}
	}
	if p.Initialized {
		return nil
	}
	_, err = session.Expect(ctx, helpers.Request{Method: "POST", Path: "/api/v1/settings/initialize"})
	return err
}

func jellyfinLink() app.Link {
	return app.NewLink("link-jellyfin", "jellyfin", "media server: Jellyfin",
		app.LinkStep{
			Message: "connect Jellyfin and preserve existing library selections",
			Check: func(ctx context.Context, from, to *helpers.H) (bool, error) {
				return connected(ctx, from, to)
			},
			Do: func(ctx context.Context, from, to *helpers.H) error { return connect(ctx, from, to) },
			Hint: func(from, to *helpers.H) string {
				return "Review " + from.URL + "/settings/jellyfin: keep your chosen libraries and verify server " + to.Service + " port " + strconv.Itoa(to.ContainerPort) +
					". For a new Seerr, open /setup, choose Jellyfin and sign in with the Admin login (any email). Do not replace an existing media server."
			},
		}, nil)
}

// server is Sonarr or Radarr as Seerr sees them.
type server struct {
	id, name string
	// rootFolder is the library folder the server's own Setup registers.
	rootFolder string
	// extra holds the fields only this kind of server takes.
	extra map[string]any
}

var (
	sonarr = server{id: "sonarr", name: "Sonarr", rootFolder: "/media/tv", extra: map[string]any{
		"enableSeasonFolders": true, "seriesType": "standard", "animeSeriesType": "standard",
		"animeTags": []any{}, "monitorNewItems": "all",
	}}
	radarr = server{id: "radarr", name: "Radarr", rootFolder: "/media/movies", extra: map[string]any{
		"minimumAvailability": "released",
	}}
)

func (s server) libraryPath(to *helpers.H) string {
	return mediastorage.Media(to.Settings) + strings.TrimPrefix(s.rootFolder, "/media")
}

func (s server) points(item map[string]any, to *helpers.H) bool {
	port, valid := item["port"].(json.Number)
	parsed, err := port.Int64()
	return valid && err == nil && item["hostname"] == to.Service && parsed == int64(to.ContainerPort)
}

func (s server) list(ctx context.Context, h *helpers.H) ([]map[string]any, error) {
	resp, err := h.Expect(ctx, helpers.Request{Path: "/api/v1/settings/" + s.id, Header: keyHeader(h)})
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := decodeServerResponse(resp, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return nil, errors.New("Seerr returned an invalid server list; no connection was changed")
	}
	ids := map[int64]bool{}
	for _, item := range items {
		id, valid := serverResourceID(item)
		host, _ := item["hostname"].(string)
		port, portValid := item["port"].(json.Number)
		parsed, err := port.Int64()
		if !valid || ids[id] || strings.TrimSpace(host) == "" || !portValid || err != nil || parsed <= 0 || parsed > 65535 {
			return nil, errors.New("Seerr returned invalid or duplicate server identities; no connection was changed")
		}
		ids[id] = true
	}
	return items, nil
}

func (s server) owned() app.Owned {
	return app.Owned{
		Find:   s.findOwned,
		Delete: s.deleteOwned,
	}
}

type qualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type testResult struct {
	Profiles    []qualityProfile `json:"profiles"`
	RootFolders []struct {
		Path string `json:"path"`
	} `json:"rootFolders"`
}

func (s server) selectProfile(h *helpers.H, profiles []qualityProfile) (qualityProfile, error) {
	selection := h.Inputs[s.id+"_profile"]
	var matches []qualityProfile
	for _, profile := range profiles {
		if selection == "" && len(profiles) == 1 || selection != "" && (selection == strconv.Itoa(profile.ID) || selection == profile.Name) {
			matches = append(matches, profile)
		}
	}
	if len(matches) != 1 || matches[0].ID <= 0 || matches[0].Name == "" {
		h.Info("Choose an upstream " + s.name + " quality profile ID or unique exact name, then run: self-host install seerr --set seerr." + s.id + "_profile=<choice> --yes. No connection was added or adopted.")
		return qualityProfile{}, helpers.ErrManual
	}
	return matches[0], nil
}

func (s server) selectRoot(from, to *helpers.H, result testResult) (string, error) {
	folders := map[string]bool{}
	for _, folder := range result.RootFolders {
		if !strings.HasPrefix(folder.Path, "/") || folder.Path == "/" {
			return "", fmt.Errorf("%s returned an invalid root folder; no connection was added", s.name)
		}
		folders[folder.Path] = true
	}
	if folders[s.libraryPath(to)] {
		return s.libraryPath(to), nil
	}
	if len(folders) == 1 {
		for folder := range folders {
			return folder, nil
		}
	}
	from.Info("Review " + s.name + " > Settings > Media Management and Seerr > Settings > Services: choose an existing target root folder explicitly. No unique root folder could be selected and no connection was added.")
	return "", helpers.ErrManual
}

func (s server) add(ctx context.Context, from, to *helpers.H) error {
	p, err := readPublic(ctx, from)
	if err != nil {
		return err
	}
	if !p.Initialized {
		return errors.New("finish Seerr's media-server setup before adding a request service (see link-jellyfin for Jellyfin)")
	}
	ids, err := s.findOwned(ctx, from, to)
	if err != nil || len(ids) > 0 {
		return err
	}
	if to.Secret(to.Key("api_key")) == "" {
		from.Info("The target's API key is missing. Repair its own Setup before retrying Seerr; no server was added.")
		return helpers.ErrManual
	}
	conn := map[string]any{"hostname": to.Service, "port": to.ContainerPort, "apiKey": to.Secret(to.Key("api_key")), "useSsl": false, "baseUrl": ""}
	resp, err := from.HTTP(ctx, helpers.Request{Method: "POST", Path: "/api/v1/settings/" + s.id + "/test", Header: keyHeader(from), JSON: conn})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return fmt.Errorf("Seerr cannot reach %s at %s (HTTP %d)", s.name, to.InternalURL(), resp.Status)
	}
	var test testResult
	if err := resp.Decode(&test); err != nil {
		return err
	}
	if len(test.Profiles) == 0 {
		return fmt.Errorf("%s has no quality profile", s.name)
	}
	profile, err := s.selectProfile(from, test.Profiles)
	if err != nil {
		return err
	}
	folder, err := s.selectRoot(from, to, test)
	if err != nil {
		return err
	}
	existing, err := s.list(ctx, from)
	if err != nil {
		return err
	}
	makeDefault := true
	for _, item := range existing {
		if s.points(item, to) {
			return s.manualOwnership(from)
		}
		isDefault, defaultValid := item["isDefault"].(bool)
		is4k, kindValid := item["is4k"].(bool)
		if !defaultValid || !kindValid {
			return s.manualOwnership(from)
		}
		if isDefault && !is4k {
			makeDefault = false
		}
	}
	body := map[string]any{
		"name": s.name, "activeProfileId": profile.ID, "activeProfileName": profile.Name,
		"activeDirectory": folder, "is4k": false, "isDefault": makeDefault, "syncEnabled": false, "preventSearch": false,
		"tagRequests": false, "tags": []any{}, "overrideRule": []any{},
	}
	for k, v := range conn {
		body[k] = v
	}
	for k, v := range s.extra {
		body[k] = v
	}
	return s.createOwned(ctx, from, to, body)
}

func serverLink(s server) app.Link {
	return app.NewOwnedLink("link-"+s.id, s.id, "requests go to "+s.name, s.owned(),
		app.LinkStep{
			Message: "send requests to " + s.name,
			Do:      func(ctx context.Context, from, to *helpers.H) error { return s.add(ctx, from, to) },
			Hint: func(from, to *helpers.H) string {
				return "Review " + from.URL + "/settings/services: " + s.name + " uses host " + to.Service + ", port " + strconv.Itoa(to.ContainerPort) +
					", its API key from `self-host credentials` and a root folder currently configured in " + s.name + ". For multiple profiles, run self-host install seerr --set seerr." + s.id + "_profile=<ID-or-exact-name> --yes. Existing defaults and search choices are preserved; unowned servers are never adopted or deleted."
			},
		},
		app.LinkStep{
			Message: "stop sending requests to " + s.name,
			Hint: func(from, _ *helpers.H) string {
				return "Review " + from.URL + "/settings/services and remove only the intended " + s.name + " entry. Unowned or changed entries are left for manual review."
			},
		})
}
