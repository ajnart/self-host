package homarr

import (
	"context"
	"errors"
	"net/http"

	"github.com/ajnart/self-host/internal/apps/tubearchivist"
	"github.com/ajnart/self-host/internal/helpers"
)

func tubearchivistTarget() target {
	return target{
		id: "tubearchivist", name: "Tube Archivist", kind: "tubearchivist", internal: fixed("http://tubearchivist:8000"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("videos", "channels", "playlists")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := tubearchivist.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info(err.Error())
			}
			return err
		},
		verify: verifyTubearchivist,
		stats: func(sample statsSample) error {
			if !validCountStats(sample, "videos", "channels", "playlists") {
				return errors.New("Homarr returned incomplete Tube Archivist statistics; missing counts are not zero, retry self-host setup homarr")
			}
			return nil
		},
	}
}

func verifyTubearchivist(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := tubearchivist.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	accountID, err := tubearchivist.HomarrAccountID(to)
	if err != nil {
		return err
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": http.MethodGet, "path": "/api/user/account/",
	})
	if err != nil {
		return err
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	identity, _ := result["data"].(map[string]any)
	if result["ok"] != true || result["status"] != float64(http.StatusOK) || integration["id"] != integrationID || integration["kind"] != "tubearchivist" || identity["id"] != float64(accountID) {
		from.Info("Restore the owned Tube Archivist token to this Homarr integration, preserving unrelated integrations, then retry self-host setup homarr. Equal or empty native counts do not establish account identity.")
		return helpers.ErrManual
	}
	return nil
}
