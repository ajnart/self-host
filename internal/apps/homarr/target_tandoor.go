package homarr

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ajnart/self-host/internal/apps/tandoor"
	"github.com/ajnart/self-host/internal/helpers"
)

const tandoorNativeHint = "Homarr could not verify this owned Tandoor integration's recorded account and Spaces. Confirm the current Homarr API supports authenticated integration requests and restore only this integration's matching read-only token in Management > Integrations, then retry self-host setup homarr. Preserve other integrations and all Tandoor accounts, Spaces and private issuance records."

func tandoorTarget() target {
	return target{
		id: "tandoor", name: "Tandoor", kind: "tandoor", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("users", "recipes", "keywords")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := tandoor.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info(tandoor.HomarrRecoveryHint)
			}
			return err
		},
		verify: verifyTandoor,
		stats:  verifyTandoorStats,
	}
}

func verifyTandoor(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := tandoor.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	var personal, current, spaces json.RawMessage
	for _, request := range []struct {
		path string
		body *json.RawMessage
	}{
		{"/api/user-space/all_personal/", &personal},
		{"/api/space/current/", &current},
		{"/api/space/", &spaces},
	} {
		value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
			"integrationId": integrationID, "method": "GET", "path": request.path,
		})
		if err != nil {
			return &helpers.ManualError{Hint: tandoorNativeHint}
		}
		result, _ := value.(map[string]any)
		integration, _ := result["integration"].(map[string]any)
		if result["ok"] != true || result["status"] != float64(200) || result["error"] != nil || integration["id"] != integrationID || integration["kind"] != "tandoor" {
			return &helpers.ManualError{Hint: tandoorNativeHint}
		}
		body, err := json.Marshal(result["data"])
		if err != nil {
			return errors.New("Homarr did not return Tandoor account and Space metadata")
		}
		*request.body = body
	}
	if err := tandoor.VerifyHomarrIdentity(to, personal, current, spaces); err != nil {
		return &helpers.ManualError{Hint: tandoorNativeHint}
	}
	return nil
}

func verifyTandoorStats(sample statsSample) error {
	users, valid := countValue(sample.values["users"])
	if !validCountStats(sample, "users", "recipes", "keywords") || !valid || users < 1 || sample.updatedAt < sample.startedAt-60_000 || sample.updatedAt > sample.completedAt+60_000 {
		return errors.New("Homarr returned invalid Tandoor Space counts; retry self-host setup homarr")
	}
	if sample.retryAt != nil && sample.retryAt != float64(0) {
		return errors.New("Homarr deferred Tandoor statistics; retry self-host setup homarr")
	}
	return nil
}
