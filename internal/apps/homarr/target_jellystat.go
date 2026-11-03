package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"github.com/ajnart/self-host/internal/apps/jellystat"
	"github.com/ajnart/self-host/internal/helpers"
)

func jellystatMetrics() []string { return []string{"songs", "movies", "episodes", "other"} }

func jellystatConnectionManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " Review the recorded Jellystat integration's Address, private API-key alternative and explicitly granted full integration access, then run self-host setup jellystat and self-host setup homarr. Preserve the issued key and selected backend; do not adopt another integration or rotate credentials."}
}

func jellystatTarget() target {
	return target{
		id: "jellystat", name: "Jellystat", kind: "jellystat", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget(jellystatMetrics()...)},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := jellystat.VerifyHomarr(ctx, to)
			if integrationID := from.Secret(from.Key("linked_jellystat")); err == nil && integrationID != "" {
				err = verifyJellystatSavedKey(ctx, from, to, integrationID, false)
			}
			if errors.Is(err, helpers.ErrManual) {
				from.Info(err.Error())
			}
			return err
		},
		verify: verifyJellystat,
		stats:  verifyJellystatStats,
	}
}

func verifyJellystat(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := jellystat.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	return verifyJellystatSavedKey(ctx, from, to, integrationID, true)
}

func verifyJellystatSavedKey(ctx context.Context, from, to *helpers.H, integrationID string, requireCurrentAddress bool) error {
	if integrationID == "" || from.Secret(from.Key("linked_jellystat")) != integrationID {
		return jellystatConnectionManual("Homarr did not identify its recorded Jellystat integration.")
	}
	value, err := apiKeyProcedure(ctx, from, "integration.byId", map[string]any{"id": integrationID})
	if err != nil {
		return jellystatConnectionManual("Homarr could not read its saved Jellystat connection.")
	}
	connection, ok := value.(map[string]any)
	credentials, _ := connection["secrets"].([]any)
	if !ok || stringField(connection, "id") != integrationID || stringField(connection, "kind") != "jellystat" || stringField(connection, "url") == "" || len(credentials) != 1 {
		return jellystatConnectionManual("Homarr returned a different Jellystat connection.")
	}
	if requireCurrentAddress && stringField(connection, "url") != to.InternalURL() {
		return jellystatConnectionManual("Homarr retained a different Jellystat Address.")
	}
	credential, ok := credentials[0].(map[string]any)
	secretValue, present := credential["value"]
	if exposed, ok := secretValue.(string); ok {
		from.Log.Secret(exposed)
	}
	if !ok || credential["kind"] != "apiKey" || !present || secretValue != nil {
		return jellystatConnectionManual("Homarr did not retain the supported private Jellystat API-key alternative.")
	}
	config, err := jellystatSavedRead(ctx, from, to, integrationID, "/api/getconfig")
	if err != nil {
		return err
	}
	inventory, err := jellystatSavedRead(ctx, from, to, integrationID, "/api/keys")
	if err != nil {
		return err
	}
	return jellystat.VerifyHomarrReadback(to, config, inventory)
}

func jellystatSavedRead(ctx context.Context, from, to *helpers.H, integrationID, path string) ([]byte, error) {
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{"integrationId": integrationID, "method": "GET", "path": path})
	if err != nil {
		return nil, jellystatConnectionManual("Homarr could not verify its saved Jellystat credential.")
	}
	response, ok := value.(map[string]any)
	data, err := json.Marshal(response["data"])
	if err != nil {
		return nil, jellystatConnectionManual("Homarr did not return Jellystat verification data.")
	}
	if path == "/api/keys" {
		jellystat.RegisterKeySecrets(from, data)
		jellystat.RegisterKeySecrets(to, data)
	}
	integration, _ := response["integration"].(map[string]any)
	if !ok || response["ok"] != true || response["status"] != float64(http.StatusOK) || stringField(integration, "id") != integrationID || stringField(integration, "kind") != "jellystat" {
		return nil, jellystatConnectionManual("Homarr's saved Jellystat request returned a failed or different connection.")
	}
	from.Log.Secret(string(data))
	return data, nil
}

func verifyJellystatStats(sample statsSample) error {
	if !validCountStats(sample, jellystatMetrics()...) || math.Trunc(sample.updatedAt) != sample.updatedAt || sample.updatedAt < sample.startedAt-5000 || sample.updatedAt > sample.completedAt+5000 {
		return errors.New("Homarr returned invalid Jellystat 30-day playback counts, units or refresh time; retry self-host setup homarr")
	}
	return nil
}
