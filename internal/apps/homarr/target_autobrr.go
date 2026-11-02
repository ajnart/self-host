package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/ajnart/self-host/internal/apps/autobrr"
	"github.com/ajnart/self-host/internal/helpers"
)

func autobrrTarget() target {
	return target{
		id: "autobrr", name: "Autobrr", kind: "autobrr", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("approvedPushes", "rejectedPushes", "filters", "indexers")},
		preflight: func(ctx context.Context, _, to *helpers.H) error {
			return autobrr.VerifyHomarr(ctx, to)
		},
		verify: verifyAutobrr,
		stats:  verifyAutobrrStats,
	}
}

func verifyAutobrr(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := autobrr.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/api/keys",
	})
	if err != nil {
		return &helpers.ManualError{Hint: "The saved Autobrr integration identity could not be checked. " + autobrr.HomarrRecoveryHint}
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	keys, array := result["data"].([]any)
	for _, value := range keys {
		key, _ := value.(map[string]any)
		if raw, ok := key["key"].(string); ok {
			from.Log.Secret(raw)
			to.Log.Secret(raw)
		}
	}
	if result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "autobrr" || !array {
		return &helpers.ManualError{Hint: "Homarr did not return the saved Autobrr connection's authenticated key inventory. " + autobrr.HomarrRecoveryHint}
	}
	body, err := json.Marshal(keys)
	if err != nil {
		return errors.New("Homarr returned invalid Autobrr identity evidence; retry self-host setup homarr")
	}
	return autobrr.VerifyHomarrInventory(to, body)
}

func verifyAutobrrStats(sample statsSample) error {
	for _, metric := range []string{"approvedPushes", "rejectedPushes", "filters", "indexers"} {
		count, ok := sample.values[metric].(float64)
		if !ok || sample.units[metric] != "count" || math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || count > 9007199254740991 || math.Trunc(count) != count {
			return errors.New("Homarr returned incomplete Autobrr count statistics; retry self-host setup homarr")
		}
	}
	retryAt, ok := sample.retryAt.(float64)
	if !ok || retryAt != 0 || sample.updatedAt > sample.completedAt+60000 || sample.updatedAt < sample.startedAt-3600000 {
		return errors.New("Homarr did not return a successful current Autobrr Stats refresh; retry self-host setup homarr")
	}
	return nil
}
