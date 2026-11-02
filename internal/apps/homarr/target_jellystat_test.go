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
	"sync"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type jellystatConnectorFixture struct {
	mu              sync.Mutex
	stats           *statsFixture
	connection      map[string]any
	config          map[string]any
	masked          any
	nativeStatus    int
	nativeReads     int
	savedReads      int
	statsBeforeKey  bool
	requestStatus   int
	requestKind     string
	requestID       string
	keyBodyOverride any
}

func newJellystatConnectorFixture() *jellystatConnectorFixture {
	f := &jellystatConnectorFixture{
		stats: newStatsFixture(), nativeStatus: http.StatusOK, requestStatus: http.StatusOK,
		requestKind: "jellystat", requestID: "jellystat-owned",
		connection: map[string]any{"id": "jellystat-owned", "kind": "jellystat", "url": "http://jellystat:3000", "secrets": []any{map[string]any{"kind": "apiKey", "value": nil}}},
		config:     map[string]any{"APP_USER": "admin", "JF_HOST": "http://selected.invalid:8096", "IS_JELLYFIN": true, "REQUIRE_LOGIN": true},
		masked:     []any{map[string]any{"name": "another user's key", "key": "private-other-key"}, map[string]any{"name": "self-host Homarr statistics", "key": "[REDACTED]"}},
	}
	f.stats.values = map[string]any{"songs": 0, "movies": 0, "episodes": 0, "other": 0}
	f.stats.timestamp = 100000
	f.stats.metrics = []any{}
	for _, key := range jellystatMetrics() {
		f.stats.metrics = append(f.stats.metrics, map[string]any{"key": key, "unit": "count"})
	}
	return f
}

func (f *jellystatConnectorFixture) peer(t *testing.T) *helpers.H {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.nativeReads++
		if r.URL.Path != "/auth/isConfigured" && r.URL.Path != "/auth/login" && r.Header.Get("Authorization") != "Bearer native-jwt" && r.Header.Get("X-API-Token") != "owned-peer-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(f.nativeStatus)
		var value any
		switch r.Method + " " + r.URL.Path {
		case "GET /auth/isConfigured":
			value = map[string]any{"state": 2, "version": "1.1.12"}
		case "POST /auth/login":
			var body struct{ Username, Password string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Username != "admin" || len(body.Password) != 128 {
				t.Error("native login did not use the established account")
			}
			value = map[string]string{"token": "native-jwt"}
		case "GET /api/getconfig":
			value = f.config
		case "GET /proxy/getAdminUsers":
			value = []any{map[string]any{"Id": "selected-admin", "Policy": map[string]bool{"IsAdministrator": true}}}
		case "GET /api/keys":
			value = []any{map[string]any{"name": "another user's key", "key": "private-other-key"}, map[string]any{"name": "self-host Homarr statistics", "key": "owned-peer-key"}}
		default:
			t.Error("connector attempted native mutation, import, activity or duplicate Stats acquisition")
		}
		json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte("owned-peer-key"))
	ownership, err := json.Marshal(map[string]any{
		"name": "self-host Homarr statistics", "fingerprint": hex.EncodeToString(sum[:]),
		"identity": map[string]string{"endpoint": "http://jellystat:3000", "user": "admin", "serverAddress": "http://selected.invalid:8096", "serverAdminId": "selected-admin", "mediaServer": "jellyfin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return helpers.New(helpers.Config{
		App: "jellystat", Service: "jellystat", ContainerPort: 3000, BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(),
		Inputs:  map[string]string{"media_server": "jellyfin"},
		Secrets: statsSecrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "native-password", "JELLYSTAT_SERVER_ADDRESS": "http://selected.invalid:8096", "JELLYSTAT_SERVER_ADMIN_ID": "selected-admin", "JELLYSTAT_HOMARR_API_KEY": "owned-peer-key", "JELLYSTAT_HOMARR_OWNERSHIP": string(ownership)},
	})
}

func (f *jellystatConnectorFixture) homarr(t *testing.T) *helpers.H {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
			if f.savedReads != 2 {
				f.statsBeforeKey = true
			}
			f.stats.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("ApiKey") != "fixture-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var value any
		switch r.URL.Path {
		case "/api/trpc/integration.byId":
			value = f.connection
		case "/api/trpc/integration.request":
			f.savedReads++
			var input struct {
				JSON struct{ IntegrationID, Method, Path string } `json:"json"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "jellystat-owned" || input.JSON.Method != "GET" {
				t.Error("connector did not use a read through the exact saved integration")
			}
			var data any
			switch input.JSON.Path {
			case "/api/getconfig":
				data = f.config
			case "/api/keys":
				data = f.masked
				if f.keyBodyOverride != nil {
					data = f.keyBodyOverride
				}
			default:
				t.Error("saved connection attempted an unsupported operation")
			}
			value = map[string]any{"ok": f.requestStatus == http.StatusOK, "status": f.requestStatus, "integration": map[string]string{"id": f.requestID, "kind": f.requestKind}, "data": data}
		default:
			t.Error("verification attempted mutation or an unsupported Homarr endpoint")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
	}))
	t.Cleanup(srv.Close)
	return helpers.New(helpers.Config{
		App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(),
		Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_LINKED_JELLYSTAT": "jellystat-owned"},
		Now:     func() time.Time { return time.UnixMilli(100000) },
	})
}

func TestJellystatNativeVerificationUsesExactKeyAndOneFreshCountSample(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    func(*jellystatConnectorFixture)
		fail      bool
		refreshes int
	}{
		{"legitimate four zero counts", nil, false, 1},
		{"populated independent counts", func(f *jellystatConnectorFixture) {
			f.stats.values = map[string]any{"songs": 9, "movies": 2, "episodes": 6, "other": 1}
		}, false, 1},
		{"negative first sample", func(f *jellystatConnectorFixture) { f.stats.values["movies"] = -1 }, true, 1},
		{"fractional play count", func(f *jellystatConnectorFixture) { f.stats.values["songs"] = 0.5 }, true, 1},
		{"text zero", func(f *jellystatConnectorFixture) { f.stats.values["other"] = "0" }, true, 1},
		{"missing selected count", func(f *jellystatConnectorFixture) { delete(f.stats.values, "episodes") }, true, 1},
		{"null selected count", func(f *jellystatConnectorFixture) { f.stats.values["episodes"] = nil }, true, 1},
		{"wrong unit", func(f *jellystatConnectorFixture) { f.stats.metrics[0].(map[string]any)["unit"] = "seconds" }, true, 1},
		{"no-op refresh", func(f *jellystatConnectorFixture) { f.stats.advance = false }, true, 1},
		{"advancing but old timestamp", func(f *jellystatConnectorFixture) { f.stats.timestamp = 1 }, true, 1},
		{"future timestamp", func(f *jellystatConnectorFixture) { f.stats.timestamp = 200000 }, true, 1},
		{"different saved id", func(f *jellystatConnectorFixture) { f.connection["id"] = "user-integration" }, true, 0},
		{"different saved kind", func(f *jellystatConnectorFixture) { f.connection["kind"] = "other" }, true, 0},
		{"different saved endpoint", func(f *jellystatConnectorFixture) { f.connection["url"] = "http://other:3000" }, true, 0},
		{"missing credential metadata", func(f *jellystatConnectorFixture) { f.connection["secrets"] = []any{} }, true, 0},
		{"exposed credential", func(f *jellystatConnectorFixture) {
			f.connection["secrets"].([]any)[0].(map[string]any)["value"] = "owned-peer-key"
		}, true, 0},
		{"another valid key same backend", func(f *jellystatConnectorFixture) {
			f.masked = []any{map[string]string{"name": "another user's key", "key": "[REDACTED]"}, map[string]string{"name": "self-host Homarr statistics", "key": "owned-peer-key"}}
		}, true, 0},
		{"unredacted selected key", func(f *jellystatConnectorFixture) { f.masked.([]any)[1].(map[string]any)["key"] = "owned-peer-key" }, true, 0},
		{"wrong forwarded identity", func(f *jellystatConnectorFixture) { f.requestID = "other" }, true, 0},
		{"wrong forwarded kind", func(f *jellystatConnectorFixture) { f.requestKind = "other" }, true, 0},
		{"forwarded unauthorized", func(f *jellystatConnectorFixture) { f.requestStatus = http.StatusForbidden }, true, 0},
		{"malformed forwarded inventory", func(f *jellystatConnectorFixture) { f.keyBodyOverride = map[string]string{"error": "private error"} }, true, 0},
		{"native rejected", func(f *jellystatConnectorFixture) { f.nativeStatus = http.StatusUnauthorized }, true, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newJellystatConnectorFixture()
			if scenario.change != nil {
				scenario.change(f)
			}
			from, to := f.homarr(t), f.peer(t)
			descriptor := jellystatTarget()
			err := descriptor.verifyData(t.Context(), from, to, "jellystat-owned")
			f.mu.Lock()
			defer f.mu.Unlock()
			if (err != nil) != scenario.fail {
				t.Fatalf("verifyData: %v, want error %v", err, scenario.fail)
			}
			if f.stats.refreshes != scenario.refreshes || f.stats.catalogs != scenario.refreshes || f.statsBeforeKey {
				t.Fatalf("Stats did not follow credential proof with one sample: refreshes=%d catalogs=%d", f.stats.refreshes, f.stats.catalogs)
			}
			if scenario.refreshes == 1 && (!f.stats.force || f.stats.integrationID != "jellystat-owned" || f.savedReads != 2) {
				t.Fatal("Stats or native reads were not bound to the recorded integration")
			}
			if descriptor.kind != "jellystat" || descriptor.credentials(from, to)[0].Kind != "apiKey" || len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" {
				t.Fatal("descriptor invented a credential alternative or widget")
			}
		})
	}
}

func TestJellystatSharedCountValidationRejectsNonfiniteAndAcceptsZeros(t *testing.T) {
	for _, count := range []float64{0, 1, -1, 0.5, math.NaN(), math.Inf(1)} {
		sample := statsSample{values: map[string]any{}, units: map[string]string{}, startedAt: 1000, completedAt: 1000, updatedAt: 1001}
		for _, key := range jellystatMetrics() {
			sample.values[key], sample.units[key] = count, "count"
		}
		valid := count == 0 || count == 1
		if (verifyJellystatStats(sample) == nil) != valid {
			t.Fatal("Jellystat did not apply the accepted shared count semantics")
		}
	}
}

func TestJellystatMissingKeyStopsBeforeHomarrMutationAndAbsenceNeedsNoPeer(t *testing.T) {
	f := newJellystatConnectorFixture()
	peer := f.peer(t)
	delete(peer.Secrets.(statsSecrets), "JELLYSTAT_HOMARR_API_KEY")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/apps":
			json.NewEncoder(w).Encode([]any{map[string]string{"id": "user-tile", "pingUrl": "http://jellystat:3000", "description": ""}})
		case "/api/trpc/integration.all":
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": []any{map[string]string{"id": "user-integration", "kind": "jellystat", "url": "http://jellystat:3000"}}}}})
		default:
			t.Error("absent-peer cleanup attempted mutation, Stats or native authentication")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_APP_IDS": "{}"}})
	descriptor := jellystatTarget()
	if err := descriptor.link().Apply(t.Context(), from, peer); !errors.Is(err, helpers.ErrManual) || requests != 0 {
		t.Fatalf("missing key reached Homarr mutation or lost Manual status: %v", err)
	}
	peer.BaseURL = "http://unreachable.invalid"
	if err := descriptor.link().Remove(t.Context(), from, peer); err != nil {
		t.Fatal(err)
	}
}
