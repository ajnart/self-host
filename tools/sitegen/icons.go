package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/ajnart/self-host/internal/catalog"
)

// iconOverrides names the Dashboard Icons path of an App whose icon is not
// svg/<id>.svg; "" means the App has none.
var iconOverrides = map[string]string{
	"archive-team-warrior": "png/archiveteam-warrior.png",
	"aria2":                "png/ariang.png",
	"borgmatic":            "svg/borg.svg",
	"cleanuparr":           "png/cleanuparr.png",
	"cloudflare-tunnel":    "svg/cloudflare.svg",
	"dashdot":              "png/dashdot.png",
	"docmost":              "",
	"linkwarden":           "",
	"oxynote":              "",
	"passbolt":             "",
	"pihole":               "svg/pi-hole.svg",
	"plant-it":             "png/plant-it.png",
	"restic":               "",
	"scrypted":             "",
	"speedtest-tracker":    "",
	"stalwart-mail":        "svg/stalwart.svg",
	"tandoor":              "svg/tandoor-recipes.svg",
	"technitium-dns":       "svg/technitium.svg",
	"trilium":              "svg/triliumnext.svg",
	"tubearchivist":        "png/tube-archivist.png",
	"unmanic":              "png/unmanic.png",
	"victoria-metrics":     "svg/victoriametrics.svg",
	"wud":                  "svg/whats-up-docker.svg",
	"xteve":                "png/xteve.png",
}

var lightIcons = map[string]string{
	"frigate":          "svg/frigate-light.svg",
	"lldap":            "svg/lldap-dark.svg",
	"mayan-edms":       "svg/mayan-edms-light.svg",
	"minio":            "svg/minio-light.svg",
	"ollama":           "svg/ollama-dark.svg",
	"open-webui":       "svg/open-webui-light.svg",
	"plex":             "svg/plex-light.svg",
	"portainer":        "svg/portainer-dark.svg",
	"tailscale":        "svg/tailscale-light.svg",
	"umami":            "svg/umami-light.svg",
	"vaultwarden":      "svg/vaultwarden-light.svg",
	"victoria-metrics": "svg/victoriametrics-light.svg",
}

var iconBase = regexp.MustCompile(`SELFHOST_ICON_BASE =\s*"([^"]+)"`)

// verified holds the icon paths known to exist: those already in the committed
// catalog.json, read before the first write, and those answered by the CDN.
var verified map[string]bool

func icon(id string) (string, error) {
	path, ok := iconOverrides[id]
	if !ok {
		path = "svg/" + id + ".svg"
	}
	if path == "" {
		return "", nil
	}
	return path, verifyIcon(id, path)
}

// verifyIcon asks the pinned Dashboard Icons CDN for a path the committed
// catalog.json does not already hold, so a new App with a wrong default fails
// here and a plain regeneration never needs the network. Only a definite 404
// is an error; an unreachable CDN is a warning.
func verifyIcon(id, path string) error {
	if verified == nil {
		verified = map[string]bool{}
		var committed struct {
			Apps []struct {
				Icon      string `json:"i"`
				LightIcon string `json:"il"`
			} `json:"apps"`
		}
		if raw, err := os.ReadFile(filepath.Join("site", "src", "data", "catalog.json")); err == nil && json.Unmarshal(raw, &committed) == nil {
			for _, a := range committed.Apps {
				verified[a.Icon] = true
				verified[a.LightIcon] = true
			}
		}
	}
	if verified[path] {
		return nil
	}
	source, err := os.ReadFile(filepath.Join("dcm", "src", "lib", "selfhost.ts"))
	if err != nil {
		return err
	}
	base := iconBase.FindSubmatch(source)
	if base == nil {
		return fmt.Errorf("dcm/src/lib/selfhost.ts has no SELFHOST_ICON_BASE")
	}
	client := http.Client{Timeout: 10 * time.Second}
	res, err := client.Head(string(base[1]) + path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not check Dashboard Icons for %s: %v\n", id, path, err)
		return nil
	}
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: Dashboard Icons has no %s; add the App to iconOverrides with its real path, or \"\" for none (tools/sitegen/icons.go)", id, path)
	}
	verified[path] = true
	return nil
}

func checkIcons(cat *catalog.Catalog) error {
	for _, id := range slices.Sorted(maps.Keys(iconOverrides)) {
		if _, found := cat.App(id); !found {
			return fmt.Errorf("iconOverrides names %q, which is not in the Catalog", id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(lightIcons)) {
		if _, found := cat.App(id); !found {
			return fmt.Errorf("lightIcons names %q, which is not in the Catalog", id)
		}
		if path, overridden := iconOverrides[id]; overridden && path == "" {
			return fmt.Errorf("lightIcons names %q, which has no base icon", id)
		}
		if err := verifyIcon(id, lightIcons[id]); err != nil {
			return err
		}
	}
	return nil
}
