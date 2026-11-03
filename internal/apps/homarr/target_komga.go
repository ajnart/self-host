package homarr

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ajnart/self-host/internal/apps/komga"
	"github.com/ajnart/self-host/internal/helpers"
)

func komgaTarget() target {
	return target{
		id: "komga", name: "Komga", kind: "komga", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("libraries", "series", "books")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			if err := komga.VerifyHomarr(ctx, to); err != nil {
				from.Info(komga.HomarrRecoveryHint)
				return err
			}
			return nil
		},
		verify: verifyKomga,
		stats:  verifyKomgaStats,
	}
}

func verifyKomga(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := komga.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/api/v2/users/me",
	})
	if err != nil {
		return &helpers.ManualError{Hint: "Homarr could not verify the saved Komga integration's identity; review API compatibility, authentication and trusted TLS before retrying. " + komga.HomarrRecoveryHint}
	}
	result, ok := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	data, _ := result["data"].(map[string]any)
	if !ok || integrationID == "" || result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "komga" {
		return &helpers.ManualError{Hint: "Homarr did not confirm the selected Komga integration's authenticated identity. " + komga.HomarrRecoveryHint}
	}
	body, err := json.Marshal(data["response"])
	if err != nil {
		return &helpers.ManualError{Hint: "Homarr returned an unreadable Komga identity. " + komga.HomarrRecoveryHint}
	}
	return komga.VerifyHomarrIdentity(to, body)
}

func verifyKomgaStats(sample statsSample) error {
	if !validCountStats(sample, "libraries", "series", "books") || sample.retryAt != float64(0) {
		return errors.New("Homarr returned invalid Komga count statistics, units or retry metadata; retry self-host setup homarr")
	}
	return nil
}
