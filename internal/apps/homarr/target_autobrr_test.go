package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func autobrrVerificationFixture(t *testing.T, stats *statsFixture, integrationID, inventory string) (*helpers.H, *helpers.H) {
	t.Helper()
	keyList := `[{"name":"self-host Homarr statistics","key":"autobrr-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "autobrr_user_session", Value: "private-session"})
			w.WriteHeader(http.StatusNoContent)
		case "/api/auth/validate":
			w.Write([]byte(`{"username":"admin","auth_method":"password"}`))
		case "/api/keys":
			w.Write([]byte(keyList))
		case "/api/config":
			w.Write([]byte(`{"version":"v1.90.0","database":"sqlite","port":7474,"log_level":"INFO"}`))
		case "/api/release/stats":
			w.Write([]byte(`{"push_approved_count":0,"push_rejected_count":0}`))
		case "/api/filters", "/api/release/indexers":
			w.Write([]byte(`[]`))
		case "/api/trpc/integration.request":
			if r.Header.Get("ApiKey") != "fixture-key" || r.Method != http.MethodPost {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var request struct {
				JSON struct {
					IntegrationID string `json:"integrationId"`
					Method        string `json:"method"`
					Path          string `json:"path"`
				} `json:"json"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.JSON.IntegrationID != "autobrr-owned" || request.JSON.Method != "GET" || request.JSON.Path != "/api/keys" {
				t.Error("native verification changed the selected connection or made a provider mutation")
			}
			body := strings.ReplaceAll(keyList, "autobrr-private-key", "[REDACTED]")
			if inventory != "" {
				body = inventory
			}
			var data any
			json.Unmarshal([]byte(body), &data)
			value := map[string]any{"ok": true, "status": 200, "integration": map[string]any{"id": integrationID, "kind": "autobrr"}, "data": data}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
		default:
			stats.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
	hash := sha256.Sum256([]byte("autobrr-private-key"))
	ownership := `{"username":"admin","name":"self-host Homarr statistics","scopes":[],"created_at":"2026-11-02T12:00:00Z","fingerprint":"` + hex.EncodeToString(hash[:]) + `"}`
	to := helpers.New(helpers.Config{App: "autobrr", Service: "autobrr", ContainerPort: 7474, Port: 17474, BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(),
		Secrets: statsSecrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "password", "AUTOBRR_HOMARR_API_KEY": "autobrr-private-key", "AUTOBRR_HOMARR_KEY_OWNERSHIP": ownership}})
	return from, to
}

func autobrrStatsFixture() *statsFixture {
	fixture := newStatsFixture()
	fixture.timestamp = float64(time.Now().UnixMilli() - 1)
	fixture.values = map[string]any{}
	fixture.metrics = []any{}
	fixture.hasRetryAt, fixture.retryAt = true, float64(0)
	for _, key := range []string{"approvedPushes", "rejectedPushes", "filters", "indexers"} {
		fixture.values[key] = float64(0)
		fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
	}
	return fixture
}

func TestAutobrrDescriptorUsesExactKindCredentialAndOneFreshSample(t *testing.T) {
	fixture := autobrrStatsFixture()
	from, to := autobrrVerificationFixture(t, fixture, "autobrr-owned", "")
	descriptor := autobrrTarget()
	if descriptor.id != "autobrr" || descriptor.kind != "autobrr" || descriptor.internal(to) != "http://autobrr:7474" || len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" {
		t.Fatal("Autobrr descriptor does not match native Stats support/internal Compose addressing")
	}
	credentials := descriptor.credentials(from, to)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" || credentials[0].Value != "autobrr-private-key" {
		t.Fatal("Autobrr did not use its peer-owned dedicated API key")
	}
	if err := descriptor.preflight(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	if err := descriptor.verifyData(t.Context(), from, to, "autobrr-owned"); err != nil {
		t.Fatal(err)
	}
	if fixture.catalogs != 1 || fixture.snapshots != 1 || fixture.refreshes != 1 || !fixture.force || fixture.integrationID != "autobrr-owned" {
		t.Fatal("Autobrr did not use exactly one central advancing Stats sample")
	}
}

func TestAutobrrSavedIdentityFailureStopsBeforeStats(t *testing.T) {
	for _, scenario := range []struct{ id, inventory string }{
		{"another-integration", ""}, {"autobrr-owned", "[]"}, {"autobrr-owned", "null"},
		{"autobrr-owned", `[{"name":"self-host Homarr statistics","key":"autobrr-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`},
		{"autobrr-owned", `[{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-10-02T12:00:00Z"}]`},
		{"autobrr-owned", `[{"name":"self-host Homarr statistics","key":"autobrr-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`},
		{"autobrr-owned", `[{"name":"self-host Homarr statistics","key":"different-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`},
		{"autobrr-owned", `[{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"different-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`},
		{"autobrr-owned", `[{"name":"changed-name","key":"autobrr-private-key","scopes":[],"created_at":"2026-10-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`},
	} {
		fixture := autobrrStatsFixture()
		from, to := autobrrVerificationFixture(t, fixture, scenario.id, scenario.inventory)
		if err := autobrrTarget().verifyData(t.Context(), from, to, "autobrr-owned"); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("wrong native identity completed: %v", err)
		}
		if fixture.catalogs != 0 || fixture.refreshes != 0 {
			t.Fatal("native identity failure acquired Stats")
		}
	}
}

func TestAutobrrStatsRejectInvalidValuesUnitsAndNoOp(t *testing.T) {
	for _, change := range []func(*statsFixture){
		func(f *statsFixture) { f.values["approvedPushes"] = float64(-1) },
		func(f *statsFixture) { f.values["rejectedPushes"] = 0.5 },
		func(f *statsFixture) { f.values["filters"] = nil },
		func(f *statsFixture) { delete(f.values, "indexers") },
		func(f *statsFixture) { f.values["filters"] = "0" },
		func(f *statsFixture) { f.values["filters"] = float64(9007199254740992) },
		func(f *statsFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" },
		func(f *statsFixture) { f.advance = false },
		func(f *statsFixture) { f.retryAt = float64(1) },
		func(f *statsFixture) { f.hasRetryAt = false },
		func(f *statsFixture) { f.retryAt = "0" },
		func(f *statsFixture) { f.timestamp = float64(1) },
	} {
		fixture := autobrrStatsFixture()
		change(fixture)
		from, to := autobrrVerificationFixture(t, fixture, "autobrr-owned", "")
		if err := autobrrTarget().verifyData(t.Context(), from, to, "autobrr-owned"); err == nil {
			t.Fatal("bad Autobrr Stats completed verification")
		}
		if fixture.refreshes != 1 {
			t.Fatal("semantic failure refreshed more than once")
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1)} {
		sample := statsSample{
			values:  map[string]any{"approvedPushes": value, "rejectedPushes": float64(0), "filters": float64(0), "indexers": float64(0)},
			units:   map[string]string{"approvedPushes": "count", "rejectedPushes": "count", "filters": "count", "indexers": "count"},
			retryAt: float64(0), startedAt: 4000000, completedAt: 4001000, updatedAt: 4000000,
		}
		if err := verifyAutobrrStats(sample); err == nil {
			t.Fatal("nonfinite native count admitted")
		}
	}
}

func TestAutobrrStatsRetainLocalSafeIntegerAndClockLimits(t *testing.T) {
	for _, metric := range []string{"approvedPushes", "rejectedPushes", "filters", "indexers"} {
		sample := statsSample{
			values:  map[string]any{"approvedPushes": float64(0), "rejectedPushes": float64(0), "filters": float64(0), "indexers": float64(0)},
			units:   map[string]string{"approvedPushes": "count", "rejectedPushes": "count", "filters": "count", "indexers": "count"},
			retryAt: float64(0), startedAt: 4000000, completedAt: 4001000, updatedAt: 4000000,
		}
		sample.values[metric] = float64(9007199254740991)
		if err := verifyAutobrrStats(sample); err != nil {
			t.Fatalf("valid safe-integer limit was rejected: %v", err)
		}
		sample.values[metric] = float64(9007199254740992)
		if err := verifyAutobrrStats(sample); err == nil {
			t.Fatal("shared count reuse lost Autobrr's maximum safe integer limit")
		}
		sample.values[metric] = float64(0)
		for _, timestamp := range []float64{400000, 4061000} {
			sample.updatedAt = timestamp
			if err := verifyAutobrrStats(sample); err != nil {
				t.Fatalf("allowed local clock boundary was rejected: %v", err)
			}
		}
		for _, timestamp := range []float64{399999, 4061001} {
			sample.updatedAt = timestamp
			if err := verifyAutobrrStats(sample); err == nil {
				t.Fatal("shared count reuse lost Autobrr's age/future-skew hold")
			}
		}
	}
}
