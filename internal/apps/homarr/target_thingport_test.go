package homarr

import "testing"

func TestThingportStatsVerification(t *testing.T) {
	valid := func() statsSample {
		return statsSample{
			values: map[string]any{"modelCount": float64(0), "collectionCount": float64(0), "categoryCount": float64(82), "authorCount": float64(0)},
			units:  map[string]string{"modelCount": "count", "collectionCount": "count", "categoryCount": "count", "authorCount": "count"},
		}
	}
	for _, scenario := range []struct {
		name   string
		change func(*statsSample)
		wantOK bool
	}{
		{name: "explicit zeros and populated", wantOK: true},
		{name: "missing author", change: func(s *statsSample) { delete(s.values, "authorCount") }},
		{name: "null model", change: func(s *statsSample) { s.values["modelCount"] = nil }},
		{name: "negative collection", change: func(s *statsSample) { s.values["collectionCount"] = float64(-1) }},
		{name: "fractional category", change: func(s *statsSample) { s.values["categoryCount"] = 0.5 }},
		{name: "textual author", change: func(s *statsSample) { s.values["authorCount"] = "0" }},
		{name: "wrong unit", change: func(s *statsSample) { s.units["modelCount"] = "bytes" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sample := valid()
			if scenario.change != nil {
				scenario.change(&sample)
			}
			if err := verifyThingportStats(sample); (err == nil) != scenario.wantOK {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}

func TestThingportTargetDescriptor(t *testing.T) {
	got := thingportTarget()
	if got.kind != "thingport" || len(got.widgets) != 1 || got.widgets[0].kind != "stats" || len(got.widgets[0].metrics) != 4 {
		t.Fatal("Thingport must bind one four-metric Stats widget")
	}
}
