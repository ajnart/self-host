package homarr

import (
	"context"
	"errors"
	"math"

	"github.com/ajnart/self-host/internal/apps/anchor"
	"github.com/ajnart/self-host/internal/helpers"
)

func anchorTarget() target {
	return target{
		id: "anchor", name: "Anchor", kind: "anchor", internal: fixed("http://anchor:3000"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("notes")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := anchor.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info(anchor.HomarrRecoveryHint + " No Homarr mutation is permitted while that prerequisite is Manual.")
			}
			return err
		},
		verify: func(ctx context.Context, _, to *helpers.H, _ string) error {
			return anchor.VerifyHomarr(ctx, to)
		},
		stats: verifyAnchorStats,
	}
}

func verifyAnchorStats(sample statsSample) error {
	count, ok := sample.values["notes"].(float64)
	if sample.units["notes"] != "count" || !ok || math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || math.Trunc(count) != count {
		return errors.New("Homarr returned incomplete Anchor statistics; retry self-host setup homarr")
	}
	return nil
}
