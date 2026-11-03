package homarr

import (
	"context"
	"errors"
	"math"

	"github.com/ajnart/self-host/internal/apps/homebox"
	"github.com/ajnart/self-host/internal/helpers"
)

func homeboxTarget() target {
	return target{
		id: "homebox", name: "Homebox", kind: "homebox", internal: fixed("http://homebox:7745"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("api_key"))}}
		},
		widgets: []widget{statWidget("items", "locations", "users")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			if err := homebox.VerifyHomarr(ctx, to); err != nil {
				if errors.Is(err, helpers.ErrManual) {
					from.Info(homebox.HomarrRecoveryHint + " No Homarr mutation is permitted while that prerequisite is Manual.")
				}
				return err
			}
			return nil
		},
		verify: verifyHomebox,
		stats:  verifyHomeboxStats,
	}
}

func verifyHomebox(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := homebox.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	accountID, err := homebox.HomarrAccountID(to)
	if err != nil {
		return err
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/api/v1/users/self",
	})
	if err != nil {
		return err
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	data, _ := result["data"].(map[string]any)
	item, _ := data["item"].(map[string]any)
	if result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "homebox" || item["id"] != accountID {
		from.Info("Restore the known selected account's API key to this owned Homebox integration in Homarr Management > Integrations, preserving unrelated integrations, then retry self-host setup homarr. Equal or empty native counts do not establish account identity.")
		return helpers.ErrManual
	}
	return nil
}

func verifyHomeboxStats(sample statsSample) error {
	for _, key := range []string{"items", "locations", "users"} {
		count, ok := sample.values[key].(float64)
		if sample.units[key] != "count" || !ok || math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || math.Trunc(count) != count {
			return errors.New("Homarr returned incomplete Homebox statistics; retry self-host setup homarr")
		}
	}
	if sample.values["users"].(float64) < 1 {
		return errors.New("Homarr returned Homebox statistics without any user; retry self-host setup homarr")
	}
	return nil
}
