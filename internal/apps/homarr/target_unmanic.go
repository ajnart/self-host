package homarr

import (
	"context"
	"errors"

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
	if !validCountStats(sample, "activeWorkers", "totalWorkers", "recordsTotal") {
		return errors.New("Homarr returned incomplete Unmanic statistics; retry self-host setup homarr")
	}
	if sample.values["activeWorkers"].(float64) > sample.values["totalWorkers"].(float64) {
		return errors.New("Homarr returned more active Unmanic workers than workers; retry self-host setup homarr")
	}
	return nil
}
