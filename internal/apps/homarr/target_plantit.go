package homarr

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ajnart/self-host/internal/apps/plantit"
	"github.com/ajnart/self-host/internal/helpers"
)

const plantitRecoveryHint = "Run self-host setup plant-it to verify the saved owner login and ID, then run self-host setup homarr. For a lost or rejected owner login, restore PLANT_IT_HOMARR_USERNAME, PLANT_IT_HOMARR_PASSWORD and PLANT_IT_OWNER_ID through the Project secret boundary after an explicit ownership decision. Do not create another garden or a duplicate Homarr integration, reset passwords or delete data."

func plantitTarget() target {
	return target{
		id: "plant-it", name: "Plant-it", kind: "plantit", internal: fixed("http://plant-it:8080"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "username", Value: to.Secret(to.Key("homarr_username"))}, {Kind: "password", Value: to.Secret(to.Key("homarr_password"))}}
		},
		widgets: []widget{statWidget("plants", "species", "photos", "events")},
		preflight: func(ctx context.Context, _, to *helpers.H) error {
			return plantit.VerifyHomarrOwner(ctx, to)
		},
		verify: verifyPlantit,
		stats:  verifyPlantitStats,
	}
}

func verifyPlantit(ctx context.Context, from, to *helpers.H, integrationID string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ownerID, err := plantit.HomarrOwnerID(ctx, to)
	if err != nil {
		return err
	}
	value, err := apiKeyProcedure(ctx, from, "integration.byId", map[string]any{"id": integrationID})
	if err != nil {
		return errors.New("Homarr could not verify its saved Plant-it login; review access to this Link's owned integration, then run self-host setup homarr")
	}
	if integrationID != from.Secret(from.Key("linked_plant-it")) || !plantitIdentityMatches(value, integrationID, to.Secret(to.Key("homarr_username"))) {
		from.Info(plantitRecoveryHint)
		return helpers.ErrManual
	}
	value, err = apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": http.MethodGet, "path": "/api/user",
	})
	if err != nil {
		return errors.New("Homarr could not read the Plant-it owner with its saved login; run self-host setup plant-it, then self-host setup homarr")
	}
	response, ok := value.(map[string]any)
	integration, _ := response["integration"].(map[string]any)
	status, _ := response["status"].(float64)
	if !ok || response["ok"] != true || status != http.StatusOK || stringField(integration, "id") != integrationID || stringField(integration, "kind") != "plantit" {
		return errors.New("Homarr did not verify the saved Plant-it connection; run self-host setup homarr")
	}
	account, _ := response["data"].(map[string]any)
	if id, hasID := account["id"].(float64); !hasID || id != float64(ownerID) {
		from.Info(plantitRecoveryHint)
		return helpers.ErrManual
	}
	return nil
}

func plantitIdentityMatches(value any, integrationID, username string) bool {
	integration, ok := value.(map[string]any)
	if !ok || integrationID == "" || username == "" || stringField(integration, "id") != integrationID || stringField(integration, "kind") != "plantit" || stringField(integration, "url") != "http://plant-it:8080" {
		return false
	}
	credentials, ok := integration["secrets"].([]any)
	if !ok || len(credentials) != 2 {
		return false
	}
	usernameFound, passwordFound := false, false
	for _, raw := range credentials {
		credential, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		value, present := credential["value"]
		switch stringField(credential, "kind") {
		case "username":
			usernameFound = stringField(credential, "value") == username
		case "password":
			passwordFound = present && value == nil
		default:
			return false
		}
	}
	return usernameFound && passwordFound
}

func verifyPlantitStats(sample statsSample) error {
	if !validCountStats(sample, "plants", "species", "photos", "events") {
		return errors.New("Homarr Plant-it Stats counts are incomplete or invalid; retry self-host setup homarr")
	}
	if sample.values["species"].(float64) > sample.values["plants"].(float64) {
		return errors.New("Homarr Plant-it Stats report more species than plants; retry self-host setup homarr")
	}
	return nil
}
