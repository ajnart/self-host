package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/helpers"
)

func minifluxTarget() target {
	return target{
		id: "miniflux", name: "Miniflux", kind: "miniflux", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("read", "unread")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			if err := miniflux.VerifyHomarr(ctx, to); err != nil {
				if errors.Is(err, helpers.ErrManual) {
					from.Info(miniflux.HomarrRecoveryHint)
				}
				return err
			}
			return nil
		},
		verify: verifyMiniflux,
		stats:  verifyMinifluxStats,
	}
}

func verifyMiniflux(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if integrationID == "" {
		return &helpers.ManualError{Hint: "The owned Miniflux integration ID is missing; preserve existing integrations and retry self-host setup homarr."}
	}
	if err := miniflux.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	readerID, err := miniflux.HomarrReaderID(to)
	if err != nil || readerID > 1<<53-1 {
		return &helpers.ManualError{Hint: miniflux.HomarrRecoveryHint}
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/v1/me",
	})
	if err != nil {
		return err
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	identity, _ := result["data"].(map[string]any)
	id, valid := countValue(identity["id"])
	if result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "miniflux" || !valid || id != float64(readerID) || identity["username"] != to.AdminUser() || identity["is_admin"] != true {
		return &helpers.ManualError{Hint: "Homarr did not authenticate its stored Miniflux integration as the recorded administrator reader. Preserve user integrations and restore only the known matching token on this owned integration, then retry self-host setup homarr. Equal or empty counts do not establish reader identity."}
	}
	return nil
}

func verifyMinifluxStats(sample statsSample) error {
	if !validCountStats(sample, "read", "unread") {
		return errors.New("Homarr returned invalid Miniflux read/unread counts or units; retry self-host setup homarr")
	}
	return nil
}
