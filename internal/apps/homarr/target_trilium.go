package homarr

import (
	"context"
	"errors"
	"strings"

	"github.com/ajnart/self-host/internal/apps/trilium"
	"github.com/ajnart/self-host/internal/helpers"
)

func triliumTarget() target {
	return target{
		id: "trilium", name: "Trilium", kind: "trilium", internal: fixed("http://trilium:8080"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("version", "notesCount", "dbSize")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := trilium.VerifyHomarr(ctx, to)
			if err != nil && errors.Is(err, helpers.ErrManual) {
				from.Info(trilium.HomarrRecoveryHint + " No Homarr mutation is permitted while that prerequisite is Manual.")
			}
			return err
		},
		verify: verifyTrilium,
		stats:  verifyTriliumStats,
	}
}

func verifyTrilium(ctx context.Context, from, _ *helpers.H, integrationID string) error {
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/etapi/app-info",
	})
	if err != nil {
		return err
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	data, _ := result["data"].(map[string]any)
	info, _ := data["response"].(map[string]any)
	if result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "trilium" ||
		!helpers.VersionCompatible(stringField(info, "appVersion"), "0.106.1") || stringField(info, "buildRevision") == "" || !isCount(info["dbVersion"]) || !isCount(info["syncVersion"]) {
		from.Info("Restore the intended notebook's ETAPI token to this owned Trilium integration in Homarr Management > Integrations, preserving unrelated integrations, then retry self-host setup homarr. Equal or empty native counts do not establish notebook identity.")
		return helpers.ErrManual
	}
	return nil
}

func verifyTriliumStats(sample statsSample) error {
	version, _ := sample.values["version"].(string)
	if sample.units["version"] != "text" || strings.TrimSpace(version) == "" {
		return errors.New("Homarr returned an unavailable Trilium version; retry self-host setup homarr")
	}
	if !validMeasuredStats(sample, map[string]string{"notesCount": "count", "dbSize": "bytes"}) {
		return errors.New("Homarr returned incomplete Trilium statistics; retry self-host setup homarr")
	}
	return nil
}
