package servarr

import (
	"context"
	"errors"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/nzbget"
	"github.com/ajnart/self-host/internal/helpers"
)

func NZBGetLink(api, categoryField, category string) app.Link {
	p := providerLink{api: api, path: "/downloadclient", target: "nzbget", implementation: "Nzbget", name: "NZBGet", endpointField: "host", categoryField: categoryField, category: category, configContract: "NzbgetSettings", protocol: "usenet"}
	p.connectionFields = func(from, to *helpers.H) ([]connectionField, error) {
		if to.AdminUser() == "" || to.AdminPassword() == "" || to.Service == "" || to.ContainerPort <= 0 || to.ContainerPort > 65535 {
			return nil, &helpers.ManualError{Hint: "Run self-host setup nzbget to verify its existing Admin login and internal Project Address, then self-host setup " + from.App + ". Preserve credentials and Access; do not substitute Homarr's restricted login or an add-only account."}
		}
		from.Log.Secret(to.AdminPassword())
		return []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", ""}, {"username", to.AdminUser()}, {"password", to.AdminPassword()}}, nil
	}
	p.verify = func(ctx context.Context, from, to *helpers.H, item provider) error {
		category, _ := item.field(categoryField).(string)
		completed, err := nzbget.CompletedDownloadPath(ctx, to, category)
		if err != nil {
			var manual *helpers.ManualError
			if errors.As(err, &manual) {
				return &helpers.ManualError{Hint: manual.Hint + " Then run self-host setup " + from.App + "."}
			}
			return err
		}
		host, _ := item.field(p.endpointField).(string)
		return p.verifyDownloadPathAs(ctx, from, to, host, completed, downloadPathSuExecUserUsers)
	}
	return p.link("download client: NZBGet", app.LinkStep{
		Message: "connect NZBGet as an additional download client",
		Hint: func(from, to *helpers.H) string {
			return "Review " + from.App + " > Settings > Download Clients > NZBGet with host " + to.Service + ", container port " + strconv.Itoa(to.ContainerPort) + " and its accepted Admin login. Select an existing exact category and review its completed folder, then run self-host setup " + from.App + ". No category, Usenet provider or indexer is changed."
		},
	}, app.LinkStep{
		Message: "remove the recorded NZBGet download client",
		Hint: func(from, _ *helpers.H) string {
			return "Review only the recorded self-host NZBGet download-client ID in " + from.App + ". Preserve manual clients, categories, files and NZBGet data; retry self-host setup " + from.App + " after recovery."
		},
	})
}
