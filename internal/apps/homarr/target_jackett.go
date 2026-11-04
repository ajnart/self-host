package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/jackett"
	"github.com/ajnart/self-host/internal/helpers"
)

func jackettTarget() target {
	return target{
		id: "jackett", name: "Jackett", kind: "jackett", internal: (*helpers.H).InternalURL,
		secrets: apiKeySecret,
		widgets: []widget{statWidget("configured")},
		preflight: func(ctx context.Context, _, to *helpers.H) error {
			return (jackett.App{}).Check(ctx, to)
		},
		verify: verifyJackett,
		stats:  verifyJackettStats,
	}
}

func verifyJackett(ctx context.Context, _, to *helpers.H, _ string) error {
	return (jackett.App{}).Check(ctx, to)
}

func verifyJackettStats(sample statsSample) error {
	retryAt, retryValid := countValue(sample.retryAt)
	if !validCountStats(sample, "configured") || !retryValid || retryAt != 0 {
		return errors.New("Homarr did not return complete refreshed Jackett configured-indexer Stats; preserve its key and trackers, then retry self-host setup homarr")
	}
	return nil
}
