package homarr

import (
	"context"
	"errors"
	"math"

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
	for key, unit := range map[string]string{"itemsHandled": "count", "episodesHandled": "count", "moviesHandled": "count", "reclaimable": "bytes"} {
		number, ok := sample.values[key].(float64)
		if !ok || sample.units[key] != unit || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 ||
			unit == "count" && math.Trunc(number) != number {
			return errors.New("Homarr returned incomplete Maintainerr statistics; retry self-host setup homarr")
		}
	}
	return nil
}
