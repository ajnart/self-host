package homarr

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
)

const (
	jackettFixtureKey      = "fixture-jackett-key"
	jackettFixturePassword = "fixture-jackett-password"
	jackettFixtureID       = "jackett-owned"
)

type jackettPeerFixture struct {
	passwordState string
	version       string
	caps          string
	indexers      string
	requests      atomic.Int32
}

func newJackettPeerFixture() *jackettPeerFixture {
	return &jackettPeerFixture{
		passwordState: "hashprefix", version: "0.24.2994",
		caps:     `<caps><server title="Jackett"/><searching><search available="yes"/></searching></caps>`,
		indexers: `<indexers/>`,
	}
}

func (f *jackettPeerFixture) helpers(t *testing.T, secrets statsSecrets) *helpers.H {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		switch r.Method + " " + r.URL.Path {
		case "POST /UI/Dashboard":
			if r.ParseForm() != nil || len(r.PostForm) != 1 {
				t.Error("Jackett login sent unexpected fields")
			}
			if r.PostForm.Get("password") == jackettFixturePassword {
				http.SetCookie(w, &http.Cookie{Name: "Jackett", Value: "fixture-session", Path: "/"})
			}
			w.Header().Set("Location", "Dashboard")
			w.WriteHeader(http.StatusFound)
		case "GET /api/v2.0/server/Config":
			cookie, err := r.Cookie("Jackett")
			if err != nil || cookie.Value != "fixture-session" {
				w.Header().Set("Location", "/UI/Login")
				w.WriteHeader(http.StatusFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"api_key": jackettFixtureKey, "app_version": f.version, "password": f.passwordState})
		case "GET /api/v2.0/indexers/all/results/torznab/api":
			query := r.URL.Query()
			if query.Get("apikey") != jackettFixtureKey {
				w.Write([]byte(`<error code="100" description="Invalid API Key"/>`))
				return
			}
			if query.Get("t") == "caps" && len(query) == 2 {
				w.Write([]byte(f.caps))
				return
			}
			if query.Get("t") == "indexers" && query.Get("configured") == "true" && len(query) == 3 {
				w.Write([]byte(f.indexers))
				return
			}
			t.Error("Jackett connector attempted an unsafe Torznab action")
			w.WriteHeader(http.StatusTeapot)
		default:
			t.Error("Jackett connector attempted an unexpected request")
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	return helpers.New(helpers.Config{
		App: "jackett", Service: "jackett", ContainerPort: 9117, Port: 19117,
		BaseURL: server.URL, URL: server.URL + "/UI/Dashboard", HTTP: server.Client(),
		Secrets: secrets, Log: events.Discard(),
	})
}

func jackettPeerSecrets() statsSecrets {
	return statsSecrets{"ADMIN_PASSWORD": jackettFixturePassword, "JACKETT_API_KEY": jackettFixtureKey}
}

func jackettStatsHelpers(t *testing.T, fixture *statsFixture, change func(map[string]any)) *helpers.H {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/widget.stats.catalog", "/api/trpc/widget.stats.snapshot":
			var input struct {
				JSON struct {
					IntegrationID string `json:"integrationId"`
				} `json:"json"`
			}
			if r.Method != http.MethodGet || json.Unmarshal([]byte(r.URL.Query().Get("input")), &input) != nil || input.JSON.IntegrationID != jackettFixtureID {
				t.Error("Jackett Stats read lost the requested integration identity")
			}
		case "/api/trpc/widget.stats.refresh":
			if r.Method != http.MethodPost {
				t.Error("Jackett Stats refresh used the wrong method")
			}
		default:
			t.Error("Jackett verification called an unexpected Homarr procedure")
			w.WriteHeader(http.StatusTeapot)
			return
		}
		if change == nil || r.URL.Path != "/api/trpc/widget.stats.refresh" {
			fixture.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		fixture.ServeHTTP(recorded, r)
		var body struct {
			Result struct {
				Data struct {
					JSON map[string]any `json:"json"`
				} `json:"data"`
			} `json:"result"`
		}
		if json.Unmarshal(recorded.Body.Bytes(), &body) != nil {
			t.Error("Stats fixture returned invalid JSON")
		}
		change(body.Result.Data.JSON)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return helpers.New(helpers.Config{
		App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(),
		Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"},
	})
}

func TestJackettDescriptorUsesOnlyTheReadOnlyConfiguredMetric(t *testing.T) {
	descriptor := jackettTarget()
	peer := newJackettPeerFixture().helpers(t, jackettPeerSecrets())
	if descriptor.id != "jackett" || descriptor.kind != "jackett" || descriptor.internal(peer) != "http://jackett:9117" || descriptor.preflight == nil || descriptor.verify == nil || descriptor.stats == nil {
		t.Fatal("Jackett descriptor does not preserve its App and native contract")
	}
	credentials := descriptor.credentials(nil, peer)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" || credentials[0].Value != jackettFixtureKey {
		t.Fatal("Jackett descriptor did not consume the existing App-owned API key")
	}
	if len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" || len(descriptor.widgets[0].metrics) != 1 || descriptor.widgets[0].metrics[0] != "configured" {
		t.Fatal("Jackett must not expose tracker tests or fabricate a tracker-health widget")
	}
	link := descriptor.link()
	if link.Target != "jackett" || link.ID != "link-jackett" {
		t.Fatal("Jackett must use the existing consumer-owned Link lifecycle")
	}
}

func TestJackettVerificationUsesOneAdvancingSemanticStatsSample(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		change       func(*statsFixture, *jackettPeerFixture)
		refresh      func(map[string]any)
		wantError    bool
		beforeSample bool
	}{
		{name: "authenticated zero and empty configured catalog"},
		{name: "configured count differs from independent native sample", change: func(f *statsFixture, p *jackettPeerFixture) {
			f.values["configured"] = float64(3)
			p.indexers = `<indexers><indexer id="kept" configured="true"><title>Person-selected tracker</title></indexer></indexers>`
		}},
		{name: "negative count", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.values["configured"] = float64(-1) }},
		{name: "fractional count", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.values["configured"] = 0.5 }},
		{name: "text count", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.values["configured"] = "0" }},
		{name: "null count", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.values["configured"] = nil }},
		{name: "missing count", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { delete(f.values, "configured") }},
		{name: "wrong unit", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "missing catalog metric", wantError: true, beforeSample: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.metrics = []any{} }},
		{name: "wrong Prowlarr metric", wantError: true, beforeSample: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.metrics[0].(map[string]any)["key"] = "indexers" }},
		{name: "duplicate catalog metric", wantError: true, beforeSample: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.metrics = append(f.metrics, f.metrics[0]) }},
		{name: "no-op refresh", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.advance = false }},
		{name: "error refresh", wantError: true, refresh: func(value map[string]any) { value["error"] = true }},
		{name: "stale refresh", wantError: true, refresh: func(value map[string]any) { value["stale"] = true }},
		{name: "missing refresh timestamp", wantError: true, refresh: func(value map[string]any) { delete(value, "updatedAt") }},
		{name: "zero refresh timestamp", wantError: true, refresh: func(value map[string]any) { value["updatedAt"] = float64(0) }},
		{name: "missing retry state", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.hasRetryAt = false }},
		{name: "null retry state", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.retryAt = nil }},
		{name: "text retry state", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.retryAt = "0" }},
		{name: "pending retry", wantError: true, change: func(f *statsFixture, _ *jackettPeerFixture) { f.retryAt = float64(9000) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"configured": float64(0)}
			fixture.metrics = []any{map[string]any{"key": "configured", "unit": "count"}}
			fixture.hasRetryAt, fixture.retryAt = true, float64(0)
			native := newJackettPeerFixture()
			if scenario.change != nil {
				scenario.change(fixture, native)
			}
			secrets := jackettPeerSecrets()
			before, _ := json.Marshal(secrets)
			err := jackettTarget().verifyData(t.Context(), jackettStatsHelpers(t, fixture, scenario.refresh), native.helpers(t, secrets), jackettFixtureID)
			if (err != nil) != scenario.wantError {
				t.Fatalf("verification error=%v, want failure=%v", err, scenario.wantError)
			}
			wantSamples := 1
			if scenario.beforeSample {
				wantSamples = 0
			}
			if fixture.catalogs != 1 || fixture.snapshots != wantSamples || fixture.refreshes != wantSamples {
				t.Fatalf("catalogs=%d snapshots=%d refreshes=%d, want one catalog and %d sample", fixture.catalogs, fixture.snapshots, fixture.refreshes, wantSamples)
			}
			if wantSamples == 1 && (!fixture.force || fixture.integrationID != jackettFixtureID) {
				t.Fatal("Stats refresh lost its force flag or owned integration identity")
			}
			after, _ := json.Marshal(secrets)
			if !bytes.Equal(before, after) || native.requests.Load() == 0 {
				t.Fatal("Jackett verifier replaced credentials or skipped positive native identity")
			}
		})
	}
}

func TestJackettPrerequisitesStopBeforeHomarrOrMutation(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*jackettPeerFixture, statsSecrets)
	}{
		{"missing App-owned key", func(_ *jackettPeerFixture, s statsSecrets) { delete(s, "JACKETT_API_KEY") }},
		{"saved key differs", func(_ *jackettPeerFixture, s statsSecrets) { s["JACKETT_API_KEY"] = "fixture-other-key" }},
		{"password rejected", func(_ *jackettPeerFixture, s statsSecrets) { s["ADMIN_PASSWORD"] = "fixture-other-password" }},
		{"first password Manual", func(f *jackettPeerFixture, _ statsSecrets) { f.passwordState = "" }},
		{"unsupported peer version", func(f *jackettPeerFixture, _ statsSecrets) { f.version = "1.0.0" }},
		{"HTTP 200 XML key error", func(f *jackettPeerFixture, _ statsSecrets) {
			f.caps = `<error code="100" description="Invalid API Key"/>`
		}},
		{"wrong capability identity", func(f *jackettPeerFixture, _ statsSecrets) { f.caps = strings.ReplaceAll(f.caps, "Jackett", "Other") }},
		{"configured catalog error", func(f *jackettPeerFixture, _ statsSecrets) { f.indexers = `<error code="100"/>` }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			native := newJackettPeerFixture()
			secrets := jackettPeerSecrets()
			scenario.change(native, secrets)
			before, _ := json.Marshal(secrets)
			peer := native.helpers(t, secrets)
			var homarrRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				homarrRequests.Add(1)
				w.WriteHeader(http.StatusTeapot)
			}))
			t.Cleanup(server.Close)
			var records []helpers.StepResult
			from := helpers.New(helpers.Config{
				App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(),
				Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"},
				Record:  func(result helpers.StepResult) { records = append(records, result) },
			})
			descriptor := jackettTarget()
			if err := descriptor.verifyData(t.Context(), from, peer, jackettFixtureID); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("native prerequisite did not remain Manual: %v", err)
			}
			if err := descriptor.link().Apply(t.Context(), from, peer); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("Link prerequisite did not remain Manual: %v", err)
			}
			if len(records) != 1 || records[0].Status != status.StepManual || !strings.Contains(records[0].Hint, "self-host setup jackett") {
				t.Fatalf("Link lost its actionable App-owned recovery hint: %+v", records)
			}
			after, _ := json.Marshal(secrets)
			if homarrRequests.Load() != 0 || !bytes.Equal(before, after) {
				t.Fatal("failed prerequisite contacted Homarr or changed Jackett credentials")
			}
		})
	}
}

func TestJackettCompatibleRequiresTheExactAPIKeyAlternative(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		kind  string
		shape [][]string
		valid bool
	}{
		{"exact alternative", "jackett", [][]string{{"apiKey"}}, true},
		{"wrong casing", "Jackett", [][]string{{"apiKey"}}, false},
		{"no-auth is not supported", "jackett", [][]string{{}}, false},
		{"basic authentication is not supported", "jackett", [][]string{{"username", "password"}}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var value any
				switch r.Method + " " + r.URL.Path {
				case "GET /api/trpc/info.getInfo":
					value = map[string]any{"version": "2.10.0"}
				case "GET /api/trpc/integration.getKinds":
					value = []any{map[string]any{"kind": scenario.kind, "requiredSecrets": scenario.shape}}
				default:
					t.Error("compatibility probe attempted mutation")
					w.WriteHeader(http.StatusTeapot)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
			}))
			t.Cleanup(server.Close)
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			peer := newJackettPeerFixture().helpers(t, jackettPeerSecrets())
			if err := jackettTarget().compatible(t.Context(), from, peer); (err == nil) != scenario.valid {
				t.Fatalf("compatible error=%v, want accepted=%v", err, scenario.valid)
			}
		})
	}
}

func TestJackettStatsRejectNonfiniteCountsAndRetryMetadata(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		sample := statsSample{values: map[string]any{"configured": value}, units: map[string]string{"configured": "count"}, retryAt: float64(0)}
		if err := verifyJackettStats(sample); err == nil {
			t.Fatal("Jackett accepted a nonfinite count")
		}
		sample.values["configured"], sample.retryAt = float64(0), value
		if err := verifyJackettStats(sample); err == nil {
			t.Fatal("Jackett accepted nonfinite retry metadata")
		}
	}
}

func TestJackettSharedBindingRemovalKeepsUnrelatedDataAndOptions(t *testing.T) {
	widget := jackettTarget().widgets[0]
	options := widgetOptions(widget, jackettFixtureID)
	entries := options["entries"].([]any)
	entry := entries[0].(map[string]any)
	if entry["id"] != statsEntryID(jackettFixtureID+":configured") || entry["integrationId"] != jackettFixtureID || entry["metric"] != "configured" {
		t.Fatal("Jackett did not use the stable per-integration metric binding")
	}
	survivor := map[string]any{"id": "user-metric", "integrationId": "other-kind", "metric": "requests", "label": "Keep my label"}
	options["entries"] = append(entries, survivor)
	options["custom"] = "keep"
	item := map[string]any{"id": "shared-stats", "kind": "stats", "integrationIds": []any{jackettFixtureID, "other-kind"}, "options": options}
	removeWidgetIntegration(item, jackettFixtureID)
	remaining := options["entries"].([]any)
	if len(remaining) != 1 || remaining[0].(map[string]any)["id"] != "user-metric" || remaining[0].(map[string]any)["label"] != "Keep my label" || options["custom"] != "keep" || wiredTo(item, "stats", jackettFixtureID) || !wiredTo(item, "stats", "other-kind") {
		t.Fatal("Jackett binding removal changed unrelated data or options")
	}
}

func TestJackettUnownedRemovalDoesNotReadThePeerOrReclaimMatches(t *testing.T) {
	userApp := map[string]any{"id": "user-app", "name": "Jackett", "pingUrl": "http://jackett:9117", "description": "My bookmark"}
	userIntegration := map[string]any{"id": "user-integration", "kind": "jackett", "url": "http://jackett:9117"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/apps":
			json.NewEncoder(w).Encode([]any{userApp})
		case "GET /api/trpc/integration.all":
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": []any{userIntegration}}}})
		default:
			t.Error("unowned removal attempted a mutation or Stats refresh")
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	secrets := statsSecrets{"HOMARR_API_KEY": "fixture-key"}
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: secrets})
	native := newJackettPeerFixture()
	peer := native.helpers(t, statsSecrets{})
	if err := jackettTarget().link().Remove(t.Context(), from, peer); err != nil {
		t.Fatal(err)
	}
	if native.requests.Load() != 0 || len(secrets) != 1 {
		t.Fatal("unowned removal called peer preflight or claimed user resources")
	}
}
