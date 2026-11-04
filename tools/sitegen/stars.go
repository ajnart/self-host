package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/catalog"
)

// starRepoOverrides names the GitHub repository of an App whose Docs URL is not
// its own GitHub repository; "" means no star count.
var starRepoOverrides = map[string]string{
	"actual-budget":        "actualbudget/actual",
	"archive-team-warrior": "ArchiveTeam/warrior-dockerfile",
	"aria2":                "aria2/aria2",
	"audiobookshelf":       "advplyr/audiobookshelf",
	"authelia":             "authelia/authelia",
	"authentik":            "goauthentik/authentik",
	"autobrr":              "autobrr/autobrr",
	"bazarr":               "morpheus65535/bazarr",
	"beszel":               "henrygd/beszel",
	"bookstack":            "BookStackApp/BookStack",
	"borgmatic":            "borgmatic-collective/borgmatic",
	"caddy":                "caddyserver/caddy",
	"cleanuparr":           "Cleanuparr/Cleanuparr",
	"cloudflare-tunnel":    "cloudflare/cloudflared",
	"dashdot":              "MauriceNino/dashdot",
	"deluge":               "deluge-torrent/deluge",
	"docmost":              "docmost/docmost",
	"emby":                 "MediaBrowser/Emby",
	"fileflows":            "revenz/FileFlows",
	"firefly-iii":          "firefly-iii/firefly-iii",
	"frigate":              "blakeblackshear/frigate",
	"glances":              "nicolargo/glances",
	"gluetun":              "qdm12/gluetun",
	"gotify":               "gotify/server",
	"grafana":              "grafana/grafana",
	"haproxy":              "haproxy/haproxy",
	"headscale":            "juanfont/headscale",
	"healthchecks":         "healthchecks/healthchecks",
	"homarr":               "homarr-labs/homarr",
	"home-assistant":       "home-assistant/core",
	"homebox":              "sysadminsmedia/homebox",
	"immich":               "immich-app/immich",
	"jackett":              "Jackett/Jackett",
	"jellyfin":             "jellyfin/jellyfin",
	"karakeep":             "karakeep-app/karakeep",
	"keycloak":             "keycloak/keycloak",
	"komga":                "gotson/komga",
	"kopia":                "kopia/kopia",
	"lidarr":               "Lidarr/Lidarr",
	"linkwarden":           "linkwarden/linkwarden",
	"loki":                 "grafana/loki",
	"maintainerr":          "Maintainerr/Maintainerr",
	"mayan-edms":           "mayan-edms/Mayan-EDMS",
	"mealie":               "mealie-recipes/mealie",
	"miniflux":             "miniflux/v2",
	"minio":                "minio/minio",
	"navidrome":            "navidrome/navidrome",
	"netbird":              "netbirdio/netbird",
	"netdata":              "netdata/netdata",
	"nextcloud":            "nextcloud/server",
	"nginx-proxy-manager":  "NginxProxyManager/nginx-proxy-manager",
	"node-red":             "node-red/node-red",
	"ntfy":                 "binwiederhier/ntfy",
	"nzbget":               "nzbgetcom/nzbget",
	"open-webui":           "open-webui/open-webui",
	"pangolin":             "fosrl/pangolin",
	"paperless-ngx":        "paperless-ngx/paperless-ngx",
	"passbolt":             "passbolt/passbolt_api",
	"patchmon":             "PatchMon/PatchMon",
	"photoprism":           "photoprism/photoprism",
	"pihole":               "pi-hole/pi-hole",
	"portainer":            "portainer/portainer",
	"postgresql":           "postgres/postgres",
	"prometheus":           "prometheus/prometheus",
	"prowlarr":             "Prowlarr/Prowlarr",
	"radarr":               "Radarr/Radarr",
	"renovate":             "renovatebot/renovate",
	"restic":               "restic/restic",
	"romm":                 "rommapp/romm",
	"sabnzbd":              "sabnzbd/sabnzbd",
	"scrypted":             "koush/scrypted",
	"seafile":              "haiwen/seafile",
	"seerr":                "seerr-team/seerr",
	"sonarr":               "Sonarr/Sonarr",
	"speedtest-tracker":    "alexjustesen/speedtest-tracker",
	"sportarr":             "Sportarr/Sportarr",
	"stalwart-mail":        "stalwartlabs/stalwart",
	"stash":                "stashapp/stash",
	"stirling-pdf":         "Stirling-Tools/Stirling-PDF",
	"syncthing":            "syncthing/syncthing",
	"tailscale":            "tailscale/tailscale",
	"tandoor":              "TandoorRecipes/recipes",
	"tdarr":                "HaveAGitGat/Tdarr",
	"thingport":            "TautvydasDerzinskas/Thingport",
	"tracearr":             "connorgallopo/Tracearr",
	"traefik":              "traefik/traefik",
	"trilium":              "TriliumNext/Trilium",
	"tubearchivist":        "tubearchivist/tubearchivist",
	"umami":                "umami-software/umami",
	"unbound":              "NLnetLabs/unbound",
	"unmanic":              "Unmanic/unmanic",
	"valkey":               "valkey-io/valkey",
	"vector":               "vectordotdev/vector",
	"victoria-metrics":     "VictoriaMetrics/VictoriaMetrics",
	"wud":                  "getwud/wud",
	"xteve":                "",
	"zerotier":             "zerotier/ZeroTierOne",
}

var githubRepo = regexp.MustCompile(`^https://github\.com/([^/#?]+)/([^/#?]+)`)

// starRepo is the owner/name an App's stars are read from: an override, else the
// repository its Docs URL points into, else "".
func starRepo(info app.Info) string {
	if repo, ok := starRepoOverrides[info.ID]; ok {
		return repo
	}
	if m := githubRepo.FindStringSubmatch(info.Docs); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

func query(ids []string, repos map[string]string) string {
	var b strings.Builder
	b.WriteString("{")
	for i, id := range ids {
		owner, name, _ := strings.Cut(repos[id], "/")
		fmt.Fprintf(&b, "r%d: repository(owner: %q, name: %q) { stargazerCount } ", i, owner, name)
	}
	b.WriteString("}")
	return b.String()
}

func writeStars() error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("set GITHUB_TOKEN to read star counts")
	}
	ids := []string{}
	repos := map[string]string{}
	for _, ap := range catalog.Default().Apps() {
		info := ap.Info()
		if repo := starRepo(info); repo != "" {
			ids = append(ids, info.ID)
			repos[info.ID] = repo
		}
	}
	body, err := json.Marshal(map[string]string{"query": query(ids, repos)})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s; the old stars.json is kept", res.Status)
	}
	var out struct {
		Data   map[string]*struct{ StargazerCount int } `json:"data"`
		Errors []struct{ Message string }               `json:"errors"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return err
	}
	for _, e := range out.Errors {
		fmt.Fprintln(os.Stderr, e.Message)
	}
	if len(out.Data) == 0 {
		return fmt.Errorf("GitHub returned no repositories; the old stars.json is kept")
	}
	type entry struct {
		Repo  string `json:"repo"`
		Stars int    `json:"stars"`
	}
	stars := map[string]entry{}
	for i, id := range ids {
		if r := out.Data[fmt.Sprintf("r%d", i)]; r != nil {
			stars[id] = entry{Repo: repos[id], Stars: r.StargazerCount}
		}
	}
	doc, err := json.MarshalIndent(stars, "", " ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join("site", "src", "data", "stars.json"), append(doc, '\n'))
}
