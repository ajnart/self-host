package servarr

import (
	"context"
	"errors"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/sabnzbd"
	"github.com/ajnart/self-host/internal/helpers"
)

func SABnzbdLink(api, categoryField, category string) app.Link {
	p := providerLink{api: api, path: "/downloadclient", target: "sabnzbd", implementation: "Sabnzbd", name: "SABnzbd", endpointField: "host", categoryField: categoryField, category: category, configContract: "SabnzbdSettings", protocol: "usenet"}
	p.connectionFields = func(from, to *helpers.H) ([]connectionField, error) {
		key := to.Secret(to.Key("api_key"))
		if key == "" || to.Service == "" || to.ContainerPort <= 0 || to.ContainerPort > 65535 {
			return nil, &helpers.ManualError{Hint: "Run self-host setup sabnzbd to verify its existing full API key and internal Project Address, then self-host setup " + from.App + ". Preserve credentials and Access; the NZB upload-only key cannot configure this Link."}
		}
		from.Log.Secret(key)
		return []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", ""}, {"apiKey", key}}, nil
	}
	p.verify = func(ctx context.Context, from, to *helpers.H, item provider) error {
		for _, field := range []string{"username", "password"} {
			if value := item.field(field); value != nil && value != "" {
				return &helpers.ManualError{Hint: "Review " + from.App + " > Settings > Download Clients > SABnzbd: its alternative username/password authentication was changed. Preserve those choices and review the intended full-API-key connection explicitly, then run self-host setup " + from.App + "."}
			}
		}
		category, _ := item.field(categoryField).(string)
		completed, err := sabnzbd.CompletedDownloadPath(ctx, to, category)
		if err != nil {
			var manual *helpers.ManualError
			if errors.As(err, &manual) {
				return &helpers.ManualError{Hint: manual.Hint + " Then run self-host setup " + from.App + "."}
			}
			return err
		}
		return p.verifyDownloadPath(ctx, from, to, completed)
	}
	return p.link("download client: SABnzbd", app.LinkStep{
		Message: "connect SABnzbd as an additional download client",
		Hint: func(from, to *helpers.H) string {
			return "Review " + from.App + " > Settings > Download Clients > SABnzbd, using host " + to.Service + ", container port " + strconv.Itoa(to.ContainerPort) + " and its existing full API key. Review category folders and shared storage, then run self-host setup " + from.App + ". No Usenet provider or indexer is changed."
		},
	}, app.LinkStep{
		Message: "remove the recorded SABnzbd download client",
		Hint: func(from, _ *helpers.H) string {
			return "Review only the recorded self-host SABnzbd download-client ID in " + from.App + ". Preserve manual clients, categories, files and SABnzbd data; retry self-host setup " + from.App + " after recovery."
		},
	})
}
