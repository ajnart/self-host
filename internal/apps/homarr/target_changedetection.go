package homarr

import (
	"context"
	"errors"
	"math"

	"github.com/ajnart/self-host/internal/apps/changedetection"
	"github.com/ajnart/self-host/internal/helpers"
)

func changedetectionMetrics() []string { return []string{"diffsDetected", "totalObserved"} }

func changedetectionTarget() target {
	return target{
		id: "changedetection", name: "Changedetection.io", kind: "changedetection", internal: fixed("http://changedetection:5000"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("api_key"))}}
		},
		widgets: []widget{statWidget(changedetectionMetrics()...)},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := changedetection.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info(err.Error())
			}
			return err
		},
		verify: verifyChangedetection,
		stats:  verifyChangedetectionStats,
	}
}

func verifyChangedetection(ctx context.Context, _, to *helpers.H, _ string) error {
	return changedetection.VerifyHomarr(ctx, to)
}

func verifyChangedetectionStats(sample statsSample) error {
	for _, key := range changedetectionMetrics() {
		count, ok := sample.values[key].(float64)
		if !ok || sample.units[key] != "count" || count < 0 || math.Trunc(count) != count || math.IsNaN(count) || math.IsInf(count, 0) {
			return errors.New("Homarr returned incomplete Changedetection.io statistics; missing values are not zero, retry self-host setup homarr")
		}
	}
	return nil
}
