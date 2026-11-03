package homarr

import "testing"

func TestYourSpotifyStatsVerification(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		change  func(*statsSample)
		wantErr bool
	}{
		{"populated", nil, false},
		{"explicit zeros", func(s *statsSample) {
			for key := range s.values {
				s.values[key] = float64(0)
			}
		}, false},
		{"fractional seconds", func(s *statsSample) { s.values["time"] = 12.5 }, false},
		{"negative songs", func(s *statsSample) { s.values["songs"] = float64(-1) }, true},
		{"fractional artists", func(s *statsSample) { s.values["artists"] = 1.5 }, true},
		{"null time", func(s *statsSample) { s.values["time"] = nil }, true},
		{"missing songs", func(s *statsSample) { delete(s.values, "songs") }, true},
		{"wrong time unit", func(s *statsSample) { s.units["time"] = "count" }, true},
		{"wrong songs unit", func(s *statsSample) { s.units["songs"] = "seconds" }, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sample := statsSample{
				values: map[string]any{"songs": float64(10), "time": float64(3600), "artists": float64(4)},
				units:  map[string]string{"songs": "count", "time": "seconds", "artists": "count"},
			}
			if scenario.change != nil {
				scenario.change(&sample)
			}
			if err := verifyYourSpotifyStats(sample); (err != nil) != scenario.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, scenario.wantErr)
			}
		})
	}
}

func TestYourSpotifyTargetDescriptor(t *testing.T) {
	target := yourSpotifyTarget()
	if target.kind != "yourSpotify" || target.internal(nil) != "http://your-spotify-server:8080" {
		t.Fatalf("unexpected descriptor %q %q", target.kind, target.internal(nil))
	}
	if metrics := target.statsMetrics(); len(metrics) != 3 || metrics[0] != "songs" || metrics[1] != "time" || metrics[2] != "artists" {
		t.Fatalf("metrics=%v", metrics)
	}
}
