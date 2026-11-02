package homarr

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestSonarrLeafUsesNativeAndSingleAdvancingStatsPipeline(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		identity    string
		reject      bool
		wrongID     bool
		noAdvance   bool
		wrongUnit   bool
		nullCount   bool
		itemType    string
		destination string
		wantError   bool
	}{
		{name: "authenticated empty library"},
		{name: "episode queue", itemType: "episode"},
		{name: "invalid authentication", reject: true, wantError: true},
		{name: "wrong product", identity: "Sportarr", wantError: true},
		{name: "wrong integration", wrongID: true, wantError: true},
		{name: "no-op refresh", noAdvance: true, wantError: true},
		{name: "wrong units", wrongUnit: true, wantError: true},
		{name: "null count", nullCount: true, wantError: true},
		{name: "book is not episode", itemType: "book", wantError: true},
		{name: "wrong destination", itemType: "episode", destination: "https://other.invalid/activity/queue", wantError: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stats := newStatsFixture()
			stats.values = map[string]any{}
			stats.metrics = nil
			for _, key := range []string{"shows", "downloaded", "missing", "queued"} {
				stats.values[key] = float64(0)
				unit := "count"
				if scenario.wrongUnit && key == "downloaded" {
					unit = "bytes"
				}
				stats.metrics = append(stats.metrics, map[string]any{"key": key, "unit": unit})
			}
			if scenario.nullCount {
				stats.values["shows"] = nil
			}
			stats.advance = !scenario.noAdvance
			identityCalls, nativeCalls := 0, 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v3/system/status":
					identityCalls++
					if r.Header.Get("X-Api-Key") != "sonarr-key" {
						t.Error("Sonarr credential owner was not reused")
					}
					if scenario.reject {
						w.WriteHeader(401)
						w.Write([]byte("private-sonarr-response"))
						return
					}
					name := scenario.identity
					if name == "" {
						name = "Sonarr"
					}
					json.NewEncoder(w).Encode(map[string]any{"appName": name, "version": "4.0.0"})
				case "/api/trpc/widget.mediaOrganizer.getData":
					nativeCalls++
					if r.Header.Get("ApiKey") != "fixture-key" {
						t.Error("Homarr transport not reused")
					}
					id := "owned-sonarr"
					if scenario.wrongID {
						id = "another"
					}
					queued := []any{}
					if scenario.itemType != "" {
						link := server.URL + "/activity/queue"
						if scenario.destination != "" {
							link = scenario.destination
						}
						queued = append(queued, map[string]any{"id": float64(-123), "title": "Synthetic episode", "type": scenario.itemType, "link": link, "status": "downloading", "percentComplete": 25, "timeLeft": nil})
					}
					json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": []any{map[string]any{"integrationId": id, "missing": []any{}, "missingCount": 0, "queued": queued, "queuedCount": len(queued)}}}}})
				default:
					stats.ServeHTTP(w, r)
				}
			}))
			defer server.Close()
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			to := helpers.New(helpers.Config{App: "sonarr", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"SONARR_API_KEY": "sonarr-key"}})
			target := sonarrTarget()
			err := target.verifyData(t.Context(), from, to, "owned-sonarr")
			if (err != nil) != scenario.wantError {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-sonarr-response") {
				t.Fatal("raw provider body escaped")
			}
			if identityCalls != 1 || stats.refreshes > 1 {
				t.Fatal("duplicated native identity or Stats acquisition")
			}
			if scenario.reject && (nativeCalls != 0 || stats.catalogs != 0 || stats.refreshes != 0) {
				t.Fatal("rejected peer credentials progressed to native or Stats reads")
			}
			if !scenario.wantError && (nativeCalls != 1 || stats.refreshes != 1 || stats.catalogs != 1 || stats.snapshots != 1 || !stats.force || stats.integrationID != "owned-sonarr") {
				t.Fatal("central advancing sample was not reused")
			}
		})
	}
}

func TestSonarrCountSemanticsAndDestinations(t *testing.T) {
	for _, value := range []any{nil, "0", -1.0, 0.5, math.NaN(), math.Inf(1)} {
		sample := statsSample{values: map[string]any{"shows": value, "downloaded": float64(0), "missing": float64(0), "queued": float64(0)}, units: map[string]string{"shows": "count", "downloaded": "count", "missing": "count", "queued": "count"}}
		if verifySonarrStats(sample) == nil {
			t.Fatalf("accepted invalid count %v", value)
		}
	}
	for _, link := range []string{"http://user:secret@sonarr.test/series/show", "http://another.test/series/show", "http://sonarr.test/series/", "http://sonarr.test/book/1", "http://sonarr.test/series/../settings", "http://sonarr.test/series/%2fsettings"} {
		if sonarrMediaItem(map[string]any{"id": float64(1), "title": "episode", "type": "episode", "link": link}, "http://sonarr.test", "/wanted/missing") {
			t.Fatal("invalid destination accepted")
		}
	}
}
