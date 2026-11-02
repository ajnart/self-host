package servarr

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/transmission"
	"github.com/ajnart/self-host/internal/helpers"
)

func TransmissionLink(api, categoryField, category string) app.Link {
	protocol := "torrent"
	if api == V1 {
		protocol = "TorrentDownloadProtocol"
	}
	p := providerLink{api: api, path: "/downloadclient", target: "transmission", implementation: "Transmission", name: "Transmission", endpointField: "host", categoryField: categoryField, category: category, configContract: "TransmissionSettings", protocol: protocol}
	p.connectionFields = func(from, to *helpers.H) ([]connectionField, error) {
		if to.Service == "" || to.ContainerPort <= 0 || to.ContainerPort > 65535 || to.AdminUser() == "" || to.AdminPassword() == "" {
			return nil, &helpers.ManualError{Hint: "Run self-host setup transmission to verify its existing Project Admin login and internal Compose Address, then self-host setup " + from.App + ". Preserve RPC authentication, credentials, host/IP protection and selected Access."}
		}
		from.Log.Secret(to.AdminPassword())
		return []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", "/transmission/"}, {"username", to.AdminUser()}, {"password", to.AdminPassword()}}, nil
	}
	p.verify = func(ctx context.Context, from, to *helpers.H, item provider) error {
		expected := map[string]string{"sonarr": "tvCategory", "radarr": "movieCategory", "lidarr": "musicCategory"}[from.App]
		if expected == "" || expected != categoryField || from.App == "lidarr" && api != V1 || from.App != "lidarr" && api != V3 {
			return p.manual(from, "the Transmission consumer API or category schema is unsupported")
		}
		directoryField := strings.TrimSuffix(categoryField, "Category") + "Directory"
		value := item.field(directoryField)
		if err := item.setField(directoryField, value); err != nil {
			return p.manual(from, "the Transmission directory schema is unavailable")
		}
		directory, typed := value.(string)
		if value != nil && !typed {
			return p.manual(from, "the Transmission directory choice is unreadable")
		}
		selected, _ := item.field(categoryField).(string)
		paths, err := transmission.DownloadClientPaths(ctx, to, selected, directory)
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
	return p.link("download client: Transmission", app.LinkStep{
		Message: "connect Transmission as an additional download client",
		Hint: func(from, to *helpers.H) string {
			return "Review " + from.App + " > Settings > Download Clients > Transmission using host " + to.Service + ", container port " + strconv.Itoa(to.ContainerPort) + ", URL base /transmission/ and its existing Project Admin login. Preserve category/directory choices; review matching download/library storage, then run self-host setup " + from.App + ". No torrent or indexer is added."
		},
	}, app.LinkStep{
		Message: "remove the recorded Transmission download client",
		Hint: func(from, _ *helpers.H) string {
			return "Review only the recorded self-host Transmission download-client ID in " + from.App + ". Preserve manual clients, torrents, files and Transmission data; retry self-host setup " + from.App + " after recovery."
		},
	})
}
