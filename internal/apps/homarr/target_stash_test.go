package homarr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestStashDescriptor(t *testing.T) {
	target := stashTarget()
	if target.id != "stash" || target.kind != "stash" || target.internal(nil) != "http://stash:9999" {
		t.Fatal("Stash descriptor does not use the exact kind and internal address")
	}
	to := helpers.New(helpers.Config{App: "stash", Log: events.Discard(), Secrets: statsSecrets{"STASH_API_KEY": "fixture-instance-key"}})
	credentials := target.credentials(to, to)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" || credentials[0].Value != "fixture-instance-key" {
		t.Fatal("Homarr must receive only the existing instance key as apiKey")
	}
	if len(target.widgets) != 1 || target.widgets[0].kind != "stats" || len(target.widgets[0].metrics) != 9 {
		t.Fatal("Stash must have one Stats widget with nine metrics")
	}
}

func TestStashDescriptorVerifyData(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    func(*statsFixture)
		identity  int
		wantError bool
		manual    bool
		refreshes int
	}{
		{name: "fresh counts, sizes and explicit zeros", refreshes: 1},
		{name: "fractional duration", change: func(f *statsFixture) { f.values["sceneDuration"] = 12.5 }, refreshes: 1},
		{name: "missing value", change: func(f *statsFixture) { delete(f.values, "tags") }, wantError: true, refreshes: 1},
		{name: "null value", change: func(f *statsFixture) { f.values["studios"] = nil }, wantError: true, refreshes: 1},
		{name: "negative count", change: func(f *statsFixture) { f.values["scenes"] = -1.0 }, wantError: true, refreshes: 1},
		{name: "fractional count", change: func(f *statsFixture) { f.values["images"] = 1.5 }, wantError: true, refreshes: 1},
		{name: "fractional bytes", change: func(f *statsFixture) { f.values["sceneBytes"] = 1.5 }, wantError: true, refreshes: 1},
		{name: "wrong units", change: func(f *statsFixture) { f.metrics[1].(map[string]any)["unit"] = "count" }, wantError: true, refreshes: 1},
		{name: "missing catalog metric", change: func(f *statsFixture) { f.metrics = f.metrics[:8] }, wantError: true},
		{name: "no-op refresh", change: func(f *statsFixture) { f.advance = false }, wantError: true, refreshes: 1},
		{name: "rejected instance key", identity: http.StatusForbidden, wantError: true, manual: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.timestamp = 45000
			fixture.values = map[string]any{
				"scenes": 3.0, "sceneBytes": 4096.0, "sceneDuration": 90.0, "images": 0.0, "imageBytes": 0.0,
				"galleries": 0.0, "performers": 2.0, "studios": 1.0, "tags": 5.0,
			}
			fixture.metrics = nil
			for _, key := range stashMetrics() {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": stashMetricUnits[key]})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/graphql" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.Header.Get("ApiKey") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if scenario.identity != 0 {
					w.WriteHeader(scenario.identity)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"systemStatus":  map[string]any{"status": "OK"},
					"configuration": map[string]any{"general": map[string]any{"username": "fixture-admin"}},
					"version":       map[string]any{"version": "v0.32.0", "hash": "synthetic-build-hash"},
				}})
			}))
			defer native.Close()
			homarr := httptest.NewServer(fixture)
			defer homarr.Close()
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: homarr.URL, HTTP: homarr.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}, Now: func() time.Time {
				if fixture.refreshes == 0 {
					return time.UnixMilli(2000)
				}
				return time.UnixMilli(47000)
			}})
			to := helpers.New(helpers.Config{App: "stash", BaseURL: native.URL, HTTP: native.Client(), Log: events.Discard(), Secrets: statsSecrets{
				"STASH_API_KEY": "fixture-instance-key", "STASH_ACCOUNT_USERNAME": "fixture-admin",
			}})
			err := stashTarget().verifyData(t.Context(), from, to, "stash-owned")
			if (err != nil) != scenario.wantError {
				t.Fatalf("unexpected verification result: %v", err)
			}
			if scenario.manual && !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("rejected key must stay Manual: %v", err)
			}
			if fixture.refreshes != scenario.refreshes {
				t.Fatalf("refreshes=%d want %d: %v", fixture.refreshes, scenario.refreshes, err)
			}
			if fixture.refreshes > 0 && (!fixture.force || fixture.integrationID != "stash-owned") {
				t.Fatal("descriptor did not use one owned forced Stats sample")
			}
		})
	}
}

func TestStashPreflightKeepsMissingKeyManual(t *testing.T) {
	from := helpers.New(helpers.Config{App: "homarr", Log: events.Discard()})
	to := helpers.New(helpers.Config{App: "stash", Log: events.Discard(), Secrets: statsSecrets{}})
	if err := stashTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("missing existing key should remain Manual: %v", err)
	}
}
