package homarr

import (
	"math"
	"testing"
)

func maintainerrSample() statsSample {
	return statsSample{
		values: map[string]any{"itemsHandled": float64(3), "episodesHandled": float64(2), "moviesHandled": float64(0), "reclaimable": float64(1024)},
		units:  map[string]string{"itemsHandled": "count", "episodesHandled": "count", "moviesHandled": "count", "reclaimable": "bytes"},
	}
}

func TestMaintainerrTargetHasNoSecretAndFourStats(t *testing.T) {
	target := maintainerrTarget()
	if target.kind != "maintainerr" || len(target.secrets(nil, nil)) != 0 || len(target.widgets) != 1 || target.preflight == nil {
		t.Fatalf("unexpected target %+v", target)
	}
}

func TestMaintainerrStatsAcceptExplicitZeros(t *testing.T) {
	sample := maintainerrSample()
	for key := range sample.values {
		sample.values[key] = float64(0)
	}
	if err := verifyMaintainerrStats(sample); err != nil {
		t.Fatal(err)
	}
}

func TestMaintainerrStatsRejectInvalidValues(t *testing.T) {
	for name, mutate := range map[string]func(statsSample){
		"missing":      func(s statsSample) { delete(s.values, "moviesHandled") },
		"null":         func(s statsSample) { s.values["itemsHandled"] = nil },
		"negative":     func(s statsSample) { s.values["reclaimable"] = float64(-1) },
		"fractional":   func(s statsSample) { s.values["episodesHandled"] = 1.5 },
		"infinite":     func(s statsSample) { s.values["reclaimable"] = math.Inf(1) },
		"nan":          func(s statsSample) { s.values["reclaimable"] = math.NaN() },
		"wrong count":  func(s statsSample) { s.units["itemsHandled"] = "bytes" },
		"wrong bytes":  func(s statsSample) { s.units["reclaimable"] = "count" },
		"missing unit": func(s statsSample) { delete(s.units, "moviesHandled") },
		"string":       func(s statsSample) { s.values["moviesHandled"] = "1" },
	} {
		sample := maintainerrSample()
		mutate(sample)
		if verifyMaintainerrStats(sample) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestMaintainerrStatsAcceptFractionalBytes(t *testing.T) {
	sample := maintainerrSample()
	sample.values["reclaimable"] = 10.5
	if err := verifyMaintainerrStats(sample); err != nil {
		t.Fatal(err)
	}
}
