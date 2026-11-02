package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/healthchecks"
	"github.com/ajnart/self-host/internal/helpers"
)

func healthchecksTarget() target {
	return target{
		id: "healthchecks", name: "Healthchecks", kind: "healthchecks", internal: fixed("http://healthchecks:8000"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("checksUp", "checksDown", "checksGrace", "checksNew")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := healthchecks.VerifyHomarr(ctx, to)
			var manual *helpers.ManualError
			if errors.As(err, &manual) {
				from.Info(manual.Hint)
			}
			return err
		},
		verify: verifyHealthchecks,
		stats:  verifyHealthchecksStats,
	}
}

func verifyHealthchecks(ctx context.Context, from, to *helpers.H, integrationID string) error {
	expected, err := healthchecks.HomarrProject(ctx, to)
	if err != nil {
		return err
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/api/v3/badges/",
	})
	if err != nil {
		return errors.New("Homarr could not verify Healthchecks project identity; preserve the integration and retry self-host setup homarr")
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	data, _ := result["data"].(map[string]any)
	badges, _ := data["badges"].(map[string]any)
	overall, _ := badges["*"].(map[string]any)
	_, bodyError := data["error"]
	if integrationID == "" || result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "healthchecks" || bodyError || overall["svg"] != expected {
		return &helpers.ManualError{Hint: "Homarr's recorded Healthchecks integration does not identify the selected project. Restore only its known project read-only key through Healthchecks Setup, preserve unrelated integrations and retry self-host setup homarr; equal or zero counts do not prove identity."}
	}
	return nil
}

func verifyHealthchecksStats(sample statsSample) error {
	if !validCountStats(sample, "checksUp", "checksDown", "checksGrace", "checksNew") {
		return errors.New("Homarr returned incomplete Healthchecks check counts; retry self-host setup homarr")
	}
	retry, ok := sample.retryAt.(float64)
	if !ok || retry != 0 {
		return errors.New("Homarr returned invalid Healthchecks refresh recovery metadata; retry self-host setup homarr")
	}
	return nil
}
