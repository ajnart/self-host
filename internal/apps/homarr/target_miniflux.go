package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/helpers"
)

const minifluxNativeKeyProofHint = "The supported APIs cannot attest the exact saved Miniflux key used by Homarr. Account identity, key inventory, timestamps and a local fingerprint are not that proof. Keep this native Link and Stats Manual; use Miniflux directly while preserving the original token, ownership, issuance journal and existing Homarr integrations. Do not overwrite another saved token, rotate/delete keys or clear records to retry. Native admission requires a separately reviewed supported key-specific authentication contract before retrying self-host setup homarr."

func minifluxTarget() target {
	return target{
		id: "miniflux", name: "Miniflux", kind: "miniflux", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets:   []widget{statWidget("read", "unread")},
		preflight: minifluxPreflight,
		verify:    verifyMiniflux,
		stats:     verifyMinifluxStats,
	}
}

func minifluxPreflight(ctx context.Context, from, to *helpers.H) error {
	if err := miniflux.VerifyHomarr(ctx, to); err != nil {
		if errors.Is(err, helpers.ErrManual) {
			from.Info(miniflux.HomarrRecoveryHint)
		}
		return err
	}
	from.Info(minifluxNativeKeyProofHint)
	return &helpers.ManualError{Hint: minifluxNativeKeyProofHint}
}

func verifyMiniflux(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if integrationID == "" {
		return &helpers.ManualError{Hint: "The owned Miniflux integration ID is missing; preserve existing integrations and retry self-host setup homarr."}
	}
	return minifluxPreflight(ctx, from, to)
}

func verifyMinifluxStats(sample statsSample) error {
	if !validCountStats(sample, "read", "unread") {
		return errors.New("Homarr returned invalid Miniflux read/unread counts or units; retry self-host setup homarr")
	}
	return nil
}
