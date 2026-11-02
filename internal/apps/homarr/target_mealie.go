package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/mealie"
	"github.com/ajnart/self-host/internal/helpers"
)

func mealieTarget() target {
	return target{
		id: "mealie", name: "Mealie", kind: "mealie", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("recipes", "users", "categories", "tags")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			_, err := mealie.VerifyHomarr(ctx, to)
			if err != nil {
				from.Info(mealie.HomarrRecoveryHint)
			}
			return err
		},
		verify: verifyMealie,
		stats:  verifyMealieStats,
	}
}

func verifyMealie(ctx context.Context, from, to *helpers.H, integrationID string) error {
	identity, err := mealie.VerifyHomarr(ctx, to)
	if err != nil {
		return err
	}
	for _, path := range []string{"/api/users/self", "/api/households/self"} {
		value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
			"integrationId": integrationID, "method": "GET", "path": path,
		})
		if err != nil {
			return err
		}
		result, _ := value.(map[string]any)
		integration, _ := result["integration"].(map[string]any)
		data, _ := result["data"].(map[string]any)
		valid := result["ok"] == true && result["status"] == float64(200) && integration["id"] == integrationID && integration["kind"] == "mealie" && data["groupId"] == identity.GroupID
		if path == "/api/users/self" {
			valid = valid && data["id"] == identity.UserID && data["householdId"] == identity.HouseholdID && data["admin"] == true
		} else {
			valid = valid && data["id"] == identity.HouseholdID
		}
		if !valid {
			return &helpers.ManualError{Hint: "Homarr's saved Mealie connection does not match the selected account and household. " + mealie.HomarrRecoveryHint}
		}
	}
	return nil
}

func verifyMealieStats(sample statsSample) error {
	if !validCountStats(sample, "recipes", "users", "categories", "tags") {
		return errors.New("Homarr returned invalid Mealie counts or units; retry self-host setup homarr")
	}
	users, _ := countValue(sample.values["users"])
	retryAt, ok := countValue(sample.retryAt)
	if users == 0 || !ok || retryAt != 0 || sample.updatedAt > sample.completedAt+5000 {
		return errors.New("Homarr Mealie statistics lack a current authenticated household user or successful refresh; retry self-host setup homarr")
	}
	return nil
}
