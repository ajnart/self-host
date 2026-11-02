package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func minifluxPeerFixture(t *testing.T, identity string) *helpers.H {
	t.Helper()
	if identity == "" {
		identity = `{"id":27,"username":"admin","is_admin":true}`
	}
	const token = "miniflux-private-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, basic := r.BasicAuth()
		if r.Method != http.MethodGet || basic && (user != "admin" || password != "miniflux-admin-password") || !basic && r.Header.Get("X-Auth-Token") != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/me":
			w.Write([]byte(identity))
		case "/v1/feeds":
			w.Write([]byte(`[{"id":41,"user_id":27}]`))
		case "/v1/api-keys":
			w.Write([]byte(`[{"id":8,"user_id":27,"description":"self-host Homarr statistics","token":"miniflux-private-key","created_at":"2026-11-02T12:00:00Z"}]`))
		default:
			t.Errorf("unexpected Miniflux request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	fingerprint := sha256.Sum256([]byte(token))
	ownership := `{"version":1,"id":8,"reader_id":27,"username":"admin","is_admin":true,"description":"self-host Homarr statistics","created_at":"2026-11-02T12:00:00Z","fingerprint":"` + hex.EncodeToString(fingerprint[:]) + `"}`
	return helpers.New(helpers.Config{
		App: "miniflux", Service: "miniflux", ContainerPort: 8080, BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(),
		Secrets: statsSecrets{
			"ADMIN_USER": "admin", "ADMIN_PASSWORD": "miniflux-admin-password", "MINIFLUX_READER_ID": "27",
			"MINIFLUX_HOMARR_API_KEY": token, "MINIFLUX_HOMARR_TOKEN_OWNERSHIP": ownership,
		},
	})
}

type minifluxNativeFixture struct {
	stats    *statsFixture
	envelope map[string]any
	reads    int
}

func newMinifluxNativeFixture() *minifluxNativeFixture {
	stats := newStatsFixture()
	stats.values = map[string]any{"read": float64(3), "unread": float64(8)}
	stats.metrics = []any{map[string]any{"key": "read", "unit": "count"}, map[string]any{"key": "unread", "unit": "count"}}
	return &minifluxNativeFixture{
		stats: stats,
		envelope: map[string]any{
			"ok": true, "status": 200,
			"integration": map[string]any{"id": "miniflux-owned", "kind": "miniflux"},
			"data":        map[string]any{"id": 27, "username": "admin", "is_admin": true},
		},
	}
}

func (f *minifluxNativeFixture) helpers(t *testing.T) *helpers.H {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/trpc/integration.request" {
			f.stats.ServeHTTP(w, r)
			return
		}
		var input struct {
			JSON struct{ IntegrationID, Method, Path string } `json:"json"`
		}
		if r.Method != http.MethodPost || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "miniflux-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/v1/me" {
			t.Error("saved reader identity did not use the exact authenticated integration GET")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.reads++
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": f.envelope}}})
	}))
	t.Cleanup(server.Close)
	return helpers.New(helpers.Config{
		App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(),
		Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"},
	})
}

func TestMinifluxDescriptorExactKindCredentialsAndMetrics(t *testing.T) {
	target := minifluxTarget()
	to := minifluxPeerFixture(t, "")
	if target.id != "miniflux" || target.kind != "miniflux" || target.internal(to) != "http://miniflux:8080" || target.preflight == nil || target.verify == nil || target.stats == nil {
		t.Fatal("Miniflux descriptor lost its exact native identity or prerequisite seam")
	}
	if !reflect.DeepEqual(target.credentials(nil, to), []secret{{Kind: "apiKey", Value: "miniflux-private-key"}}) || len(target.widgets) != 1 || target.widgets[0].kind != "stats" || !reflect.DeepEqual(target.statsMetrics(), []string{"read", "unread"}) {
		t.Fatal("Miniflux supplied unsupported credentials or widgets")
	}
}

func TestMinifluxNativeReaderPrecedesOneAdvancingSemanticSample(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		change  func(*minifluxNativeFixture)
		wantOK  bool
		refresh int
	}{
		{name: "populated counters", wantOK: true, refresh: 1},
		{name: "explicit empty counters", wantOK: true, refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.values = map[string]any{"read": 0, "unread": 0} }},
		{name: "fractional read", refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.values["read"] = 0.5 }},
		{name: "negative unread", refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.values["unread"] = -1 }},
		{name: "text read", refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.values["read"] = "0" }},
		{name: "null unread", refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.values["unread"] = nil }},
		{name: "missing read", refresh: 1, change: func(f *minifluxNativeFixture) { delete(f.stats.values, "read") }},
		{name: "wrong unit", refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "missing catalog key", change: func(f *minifluxNativeFixture) { f.stats.metrics = f.stats.metrics[:1] }},
		{name: "no-op refresh", refresh: 1, change: func(f *minifluxNativeFixture) { f.stats.advance = false }},
		{name: "different reader ID", change: func(f *minifluxNativeFixture) { f.envelope["data"].(map[string]any)["id"] = 28 }},
		{name: "equal empty counters from another reader", change: func(f *minifluxNativeFixture) {
			f.envelope["data"].(map[string]any)["id"] = 28
			f.stats.values = map[string]any{"read": 0, "unread": 0}
		}},
		{name: "different username", change: func(f *minifluxNativeFixture) { f.envelope["data"].(map[string]any)["username"] = "another" }},
		{name: "nonadministrator authorization", change: func(f *minifluxNativeFixture) { f.envelope["data"].(map[string]any)["is_admin"] = false }},
		{name: "missing authorization", change: func(f *minifluxNativeFixture) { delete(f.envelope["data"].(map[string]any), "is_admin") }},
		{name: "another integration ID", change: func(f *minifluxNativeFixture) { f.envelope["integration"].(map[string]any)["id"] = "user-owned" }},
		{name: "another kind", change: func(f *minifluxNativeFixture) { f.envelope["integration"].(map[string]any)["kind"] = "linkwarden" }},
		{name: "body-level error", change: func(f *minifluxNativeFixture) { f.envelope["ok"] = false }},
		{name: "upstream unauthorized", change: func(f *minifluxNativeFixture) { f.envelope["status"] = 401 }},
		{name: "missing useful identity", change: func(f *minifluxNativeFixture) { f.envelope["data"] = nil }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newMinifluxNativeFixture()
			if scenario.change != nil {
				scenario.change(fixture)
			}
			err := minifluxTarget().verifyData(t.Context(), fixture.helpers(t), minifluxPeerFixture(t, ""), "miniflux-owned")
			if (err == nil) != scenario.wantOK || fixture.reads != 1 || fixture.stats.refreshes != scenario.refresh {
				t.Fatalf("verification=%v native=%d refreshes=%d, expected success=%v refreshes=%d", err, fixture.reads, fixture.stats.refreshes, scenario.wantOK, scenario.refresh)
			}
			if scenario.refresh > 0 && (fixture.stats.catalogs != 1 || fixture.stats.snapshots != 1 || !fixture.stats.force || fixture.stats.integrationID != "miniflux-owned") {
				t.Fatal("leaf bypassed the single central catalog/advancing refresh sample")
			}
		})
	}
}

func TestMinifluxPeerFailureAndMissingCredentialsStopBeforeHomarr(t *testing.T) {
	for _, mode := range []string{"missing token", "wrong peer reader", "pending issuance", "missing integration ID"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newMinifluxNativeFixture()
			from := fixture.helpers(t)
			to := minifluxPeerFixture(t, "")
			id := "miniflux-owned"
			switch mode {
			case "missing token":
				to.Secrets.Set("MINIFLUX_HOMARR_API_KEY", "")
			case "wrong peer reader":
				to = minifluxPeerFixture(t, `{"id":28,"username":"admin","is_admin":true}`)
			case "pending issuance":
				to.Secrets.Set("MINIFLUX_HOMARR_TOKEN_PENDING", "unknown")
			case "missing integration ID":
				id = ""
			}
			if err := minifluxTarget().verifyData(t.Context(), from, to, id); !errors.Is(err, helpers.ErrManual) || fixture.reads != 0 || fixture.stats.catalogs != 0 || fixture.stats.refreshes != 0 {
				t.Fatalf("unready peer reached Homarr or Stats: %v", err)
			}
			if mode != "missing integration ID" {
				if err := minifluxTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("preflight lost the Manual credential boundary: %v", err)
				}
			}
		})
	}
}

func TestMinifluxPreflightReportsSpecificRecoveryWithoutMutation(t *testing.T) {
	fixture := newMinifluxNativeFixture()
	from := fixture.helpers(t)
	to := minifluxPeerFixture(t, "")
	to.Secrets.Set("MINIFLUX_HOMARR_API_KEY", "")
	var emitted []events.Event
	from.Log = events.New(events.SinkFunc(func(event events.Event) { emitted = append(emitted, event) }))
	if err := minifluxTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) || fixture.reads != 0 || fixture.stats.refreshes != 0 {
		t.Fatalf("missing credential was admitted or mutated Homarr: %v", err)
	}
	encoded, err := json.Marshal(emitted)
	if err != nil || !strings.Contains(string(encoded), "self-host setup miniflux") || !strings.Contains(string(encoded), "not a read-only scope") || strings.Contains(string(encoded), "miniflux-private-key") || miniflux.HomarrRecoveryHint == "" {
		t.Fatalf("missing App-specific credential-safe recovery: %s", encoded)
	}
}
