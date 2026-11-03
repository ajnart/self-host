package homarr

import (
	"context"
	"errors"
	"math"

	"github.com/ajnart/self-host/internal/apps/unmanic"
	"github.com/ajnart/self-host/internal/helpers"
)

func unmanicTarget() target {
	return target{
		id: "unmanic", name: "Unmanic", kind: "unmanic", internal: fixed("http://unmanic:8888"),
		secrets:   func(_, _ *helpers.H) []secret { return []secret{} },
		widgets:   []widget{statWidget("activeWorkers", "totalWorkers", "recordsTotal")},
		preflight: preflightUnmanic,
		verify:    verifyUnmanic,
		stats:     verifyUnmanicStats,
	}
}

func preflightUnmanic(ctx context.Context, _, to *helpers.H) error {
	_, err := unmanic.ReadHomarrStats(ctx, to)
	return err
}

func verifyUnmanic(ctx context.Context, _, to *helpers.H, _ string) error {
	_, err := unmanic.ReadHomarrStats(ctx, to)
	return err
}

func verifyUnmanicStats(sample statsSample) error {
	counts := map[string]float64{}
	for _, key := range []string{"activeWorkers", "totalWorkers", "recordsTotal"} {
		count, ok := sample.values[key].(float64)
		if sample.units[key] != "count" || !ok || count < 0 || math.IsNaN(count) || math.IsInf(count, 0) || math.Trunc(count) != count {
			return errors.New("Homarr returned incomplete Unmanic statistics; retry self-host setup homarr")
		}
		counts[key] = count
	}
	if counts["activeWorkers"] > counts["totalWorkers"] {
		return errors.New("Homarr returned more active Unmanic workers than workers; retry self-host setup homarr")
	}
	return nil
}
