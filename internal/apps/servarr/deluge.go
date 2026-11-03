package servarr

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/deluge"
	"github.com/ajnart/self-host/internal/helpers"
)

func DelugeLink(api, categoryField, category string) app.Link {
	protocol := "torrent"
	if api == V1 {
		protocol = "TorrentDownloadProtocol"
	}
	p := providerLink{api: api, path: "/downloadclient", target: "deluge", implementation: "Deluge", name: "Deluge", endpointField: "host", categoryField: categoryField, category: category, configContract: "DelugeSettings", protocol: protocol}
	p.connectionFields = func(from, to *helpers.H) ([]connectionField, error) {
		password := deluge.WebPassword(to)
		if password == "" || to.Service == "" || to.ContainerPort <= 0 || to.ContainerPort > 65535 {
			return nil, &helpers.ManualError{Hint: "Run self-host setup deluge to verify its saved Web password and internal Project Address, then self-host setup " + from.App + ". Preserve credentials and Access; daemon usernames and port 58846 are not Arr's Web API contract."}
		}
		from.Log.Secret(password)
		return []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", ""}, {"password", password}}, nil
	}
	p.verify = func(ctx context.Context, from, to *helpers.H, item provider) error {
		expected := map[string]string{"sonarr": "tvCategory", "radarr": "movieCategory", "lidarr": "musicCategory"}[from.App]
		if expected == "" || expected != categoryField || from.App == "lidarr" && api != V1 || from.App != "lidarr" && api != V3 {
			return p.manual(from, "the Deluge consumer API or category schema is unsupported")
		}
		importedField := strings.TrimSuffix(categoryField, "Category") + "ImportedCategory"
		choices := map[string]string{}
		fields, _ := item["fields"].([]any)
		for _, raw := range fields {
			field, _ := raw.(map[string]any)
			name, _ := field["name"].(string)
			if name != importedField && name != "downloadDirectory" && name != "completedDirectory" {
				continue
			}
			value, ok := field["value"].(string)
			if !ok && field["value"] != nil {
				return p.manual(from, "the stored Deluge category or directory choice has an unsupported type")
			}
			choices[name] = value
		}
		if len(choices) != 3 {
			return p.manual(from, "the stored Deluge post-import category or directory fields are unavailable")
		}
		selected, _ := item.field(categoryField).(string)
		paths, err := deluge.DownloadPaths(ctx, to, selected, choices[importedField], choices["downloadDirectory"], choices["completedDirectory"])
		if err != nil {
			var manual *helpers.ManualError
			if errors.As(err, &manual) {
				return &helpers.ManualError{Hint: manual.Hint + " Then run self-host setup " + from.App + "."}
			}
			return err
		}
		host, _ := item.field(p.endpointField).(string)
		for _, directory := range paths {
			if err := p.verifyDownloadPath(ctx, from, to, host, directory); err != nil {
				return err
			}
		}
		return nil
	}
	return p.link("download client: Deluge", app.LinkStep{
		Message: "connect Deluge as an additional download client",
		Hint: func(from, to *helpers.H) string {
			return "Review " + from.App + " > Settings > Download Clients > Deluge using host " + to.Service + ", container port " + strconv.Itoa(to.ContainerPort) + " and its saved Web password. Enable Label and create the selected labels explicitly in Deluge, review shared download/library paths, then run self-host setup " + from.App + ". Setup does not start torrents or change daemon access."
		},
	}, app.LinkStep{
		Message: "remove the recorded Deluge download client",
		Hint: func(from, _ *helpers.H) string {
			return "Review only the recorded self-host Deluge download-client ID in " + from.App + ". Preserve manual clients, labels, mappings, torrents and Deluge data; retry self-host setup " + from.App + " after recovery."
		},
	})
}
