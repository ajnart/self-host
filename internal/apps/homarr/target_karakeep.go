package homarr

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/apps/karakeep"
	"github.com/ajnart/self-host/internal/helpers"
)

const karakeepSavedKeyProofHint = "Karakeep's account API does not identify Homarr's saved API key. This Link remains Manual before reconciliation until a supported read-only saved-connection contract authoritatively identifies the recorded keyId; matching accounts, counters and local fingerprints are insufficient. Preserve saved Addresses, credentials, integrations, widgets, user edits and private issuance records. Local owner/key verification does not authorize overwriting them. Do not rotate credentials or read private databases to bypass this hold. Review supported Karakeep/Homarr API capabilities, then retry self-host setup homarr; native Stats have not been verified."

func karakeepTarget() target {
	return target{
		id: "karakeep", name: "Karakeep", kind: "karakeep", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget(karakeepStatsMetrics()...)},
		preflight: func(_ context.Context, from, _ *helpers.H) error {
			from.Info(karakeepSavedKeyProofHint)
			return &helpers.ManualError{Hint: karakeepSavedKeyProofHint}
		},
		verify: verifyKarakeep,
		stats:  verifyKarakeepStats,
	}
}

func karakeepStatsMetrics() []string {
	return []string{"bookmarks", "favorites", "archived", "highlights", "lists", "tags"}
}

func verifyKarakeep(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := karakeep.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	accountID, err := karakeep.HomarrAccountID(to)
	if err != nil {
		return err
	}
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/api/v1/users/me",
	})
	if err != nil {
		return &helpers.ManualError{Hint: "Homarr could not verify its saved Karakeep connection through the read-only account API. Review saved-integration request support, full-access permission and trusted connectivity without changing the peer's key or bypassing TLS, then retry self-host setup homarr."}
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	profile, _ := result["data"].(map[string]any)
	email, _ := profile["email"].(string)
	if result["ok"] != true || result["status"] != float64(200) || result["error"] != nil || integration["id"] != integrationID || integration["kind"] != "karakeep" || profile["id"] != accountID || profile["localUser"] != true || profile["error"] != nil || !strings.EqualFold(email, to.Secret(to.Key("admin_email"))) {
		return &helpers.ManualError{Hint: "Homarr's saved Karakeep connection did not identify the recorded account. Restore only that owned integration's known matching credential, preserving user integrations and widgets, then retry self-host setup homarr. Empty or equal counters do not establish account identity."}
	}
	return &helpers.ManualError{Hint: karakeepSavedKeyProofHint}
}

func verifyKarakeepStats(sample statsSample) error {
	if !validCountStats(sample, karakeepStatsMetrics()...) {
		return errors.New("Homarr returned incomplete Karakeep count statistics; retry self-host setup homarr")
	}
	bookmarks, _ := countValue(sample.values["bookmarks"])
	favorites, _ := countValue(sample.values["favorites"])
	archived, _ := countValue(sample.values["archived"])
	retry, valid := countValue(sample.retryAt)
	if favorites > bookmarks || archived > bookmarks || !valid || retry != 0 || sample.updatedAt < sample.startedAt-float64(time.Hour.Milliseconds()) || sample.updatedAt > sample.completedAt+float64(time.Minute.Milliseconds()) {
		return errors.New("Homarr returned inconsistent or unready Karakeep statistics; retry self-host setup homarr")
	}
	return nil
}
