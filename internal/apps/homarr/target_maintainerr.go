package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/maintainerr"
	"github.com/ajnart/self-host/internal/helpers"
)

func maintainerrTarget() target {
	return target{
		id: "maintainerr", name: "Maintainerr", kind: "maintainerr", internal: fixed("http://maintainerr:6246"),
		secrets:   func(_, _ *helpers.H) []secret { return []secret{} },
		widgets:   []widget{statWidget("itemsHandled", "episodesHandled", "moviesHandled", "reclaimable")},
		preflight: func(ctx context.Context, _, to *helpers.H) error { return maintainerr.VerifyHomarr(ctx, to) },
		verify: func(ctx context.Context, _, to *helpers.H, _ string) error {
			return maintainerr.VerifyHomarr(ctx, to)
		},
		stats: verifyMaintainerrStats,
	}
}

func verifyMaintainerrStats(sample statsSample) error {
	if !validMeasuredStats(sample, map[string]string{"itemsHandled": "count", "episodesHandled": "count", "moviesHandled": "count", "reclaimable": "bytes"}) {
		return errors.New("Homarr returned incomplete Maintainerr statistics; retry self-host setup homarr")
	}
	return nil
}
