package homarr

import "testing"

func TestChangedetectionStatsAcceptZerosAndRejectIncompleteSamples(t *testing.T) {
	units := map[string]string{"diffsDetected": "count", "totalObserved": "count"}
	for _, scenario := range []struct {
		name   string
		values map[string]any
		units  map[string]string
		fail   bool
	}{
		{name: "explicit zeros", values: map[string]any{"diffsDetected": 0.0, "totalObserved": 0.0}, units: units},
		{name: "counts", values: map[string]any{"diffsDetected": 2.0, "totalObserved": 5.0}, units: units},
		{name: "null", values: map[string]any{"diffsDetected": nil, "totalObserved": 0.0}, units: units, fail: true},
		{name: "missing", values: map[string]any{"totalObserved": 0.0}, units: units, fail: true},
		{name: "fractional", values: map[string]any{"diffsDetected": 0.5, "totalObserved": 1.0}, units: units, fail: true},
		{name: "negative", values: map[string]any{"diffsDetected": 0.0, "totalObserved": -1.0}, units: units, fail: true},
		{name: "wrong unit", values: map[string]any{"diffsDetected": 0.0, "totalObserved": 0.0}, units: map[string]string{"diffsDetected": "bytes", "totalObserved": "count"}, fail: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := verifyChangedetectionStats(statsSample{values: scenario.values, units: scenario.units})
			if (err != nil) != scenario.fail {
				t.Fatalf("fail=%v got %v", scenario.fail, err)
			}
		})
	}
}
