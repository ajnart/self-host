package homarr

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestUnmanicTargetIsStatsOnlyWithoutSecrets(t *testing.T) {
	descriptor := unmanicTarget()
	if descriptor.kind != "unmanic" || descriptor.internal(nil) != "http://unmanic:8888" || len(descriptor.credentials(nil, nil)) != 0 {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	if len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" {
		t.Fatalf("widgets = %+v", descriptor.widgets)
	}
	if got := descriptor.statsMetrics(); len(got) != 3 || got[0] != "activeWorkers" || got[1] != "totalWorkers" || got[2] != "recordsTotal" {
		t.Fatalf("metrics = %v", got)
	}
}

func TestUnmanicPreparedStatsSemantics(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(statsSample)
		valid  bool
	}{
		{"zeros", func(statsSample) {}, true},
		{"populated", func(s statsSample) {
			s.values["activeWorkers"], s.values["totalWorkers"], s.values["recordsTotal"] = float64(1), float64(2), float64(9)
		}, true},
		{"missing", func(s statsSample) { delete(s.values, "recordsTotal") }, false},
		{"null", func(s statsSample) { s.values["totalWorkers"] = nil }, false},
		{"fractional", func(s statsSample) { s.values["recordsTotal"] = 1.5 }, false},
		{"negative", func(s statsSample) { s.values["activeWorkers"] = float64(-1) }, false},
		{"infinite", func(s statsSample) { s.values["recordsTotal"] = math.Inf(1) }, false},
		{"NaN", func(s statsSample) { s.values["recordsTotal"] = math.NaN() }, false},
		{"wrong unit", func(s statsSample) { s.units["recordsTotal"] = "bytes" }, false},
		{"active over total", func(s statsSample) { s.values["activeWorkers"] = float64(1) }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sample := statsSample{values: map[string]any{}, units: map[string]string{}}
			for _, key := range unmanicTarget().statsMetrics() {
				sample.values[key] = float64(0)
				sample.units[key] = "count"
			}
			scenario.change(sample)
			if err := unmanicTarget().stats(sample); (err == nil) != scenario.valid {
				t.Fatalf("valid=%v err=%v", scenario.valid, err)
			}
		})
	}
}

func TestUnmanicPreflightAndVerifyReadOnlyIdentity(t *testing.T) {
	status, uuid := http.StatusOK, "uuid-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		switch r.URL.Path {
		case "/unmanic/api/v2/version/read":
			json.NewEncoder(w).Encode(map[string]any{"version": "0.4.1"})
		case "/unmanic/api/v2/session/state":
			json.NewEncoder(w).Encode(map[string]any{"uuid": uuid})
		case "/unmanic/api/v2/workers/status":
			json.NewEncoder(w).Encode(map[string]any{"workers_status": []any{}})
		case "/unmanic/api/v2/pending/tasks":
			json.NewEncoder(w).Encode(map[string]any{"recordsTotal": 0})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard()})
	to := helpers.New(helpers.Config{App: "unmanic", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"UNMANIC_INSTALLATION_UUID": "uuid-1"}})
	descriptor := unmanicTarget()
	if err := descriptor.preflight(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	if err := descriptor.verify(t.Context(), from, to, "integration"); err != nil {
		t.Fatal(err)
	}
	uuid = "uuid-2"
	if err := descriptor.preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("changed identity: %v", err)
	}
	uuid, status = "uuid-1", http.StatusUnauthorized
	if err := descriptor.verify(t.Context(), from, to, "integration"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("authentication: %v", err)
	}
}
