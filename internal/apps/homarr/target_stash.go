package homarr

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/ajnart/self-host/internal/apps/stash"
	"github.com/ajnart/self-host/internal/helpers"
)

var stashMetricUnits = map[string]string{
	"scenes": "count", "sceneBytes": "bytes", "sceneDuration": "seconds",
	"images": "count", "imageBytes": "bytes", "galleries": "count",
	"performers": "count", "studios": "count", "tags": "count",
}

func stashMetrics() []string {
	return []string{"scenes", "sceneBytes", "sceneDuration", "images", "imageBytes", "galleries", "performers", "studios", "tags"}
}

func stashTarget() target {
	return target{
		id: "stash", name: "Stash", kind: "stash", internal: fixed("http://stash:9999"),
		secrets: apiKeySecret,
		widgets: []widget{statWidget(stashMetrics()...)},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := stash.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info(err.Error())
			}
			return err
		},
		verify: func(ctx context.Context, _, to *helpers.H, _ string) error {
			return stash.VerifyHomarr(ctx, to)
		},
		stats: verifyStashStats,
	}
}

func verifyStashStats(sample statsSample) error {
	if sample.updatedAt < sample.startedAt || sample.updatedAt > sample.completedAt+float64(time.Second.Milliseconds()) {
		return errors.New("Homarr Stash statistics have an invalid refresh time; retry self-host setup homarr")
	}
	for _, key := range stashMetrics() {
		if sample.units[key] != stashMetricUnits[key] {
			return errors.New("Homarr lacks the supported Stash statistics or units; use Homarr 2.4.1 and retry Setup")
		}
		value, ok := sample.values[key].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return errors.New("Homarr returned incomplete Stash statistics; missing values are not zero")
		}
		if stashMetricUnits[key] != "seconds" && math.Trunc(value) != value {
			return errors.New("Homarr returned a fractional Stash count or size")
		}
	}
	return nil
}
