package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func komgaTestIdentity() map[string]any {
	return map[string]any{
		"id": "limited-user", "email": "metrics@selfhost.local", "roles": []string{"USER"},
		"sharedAllLibraries": true, "sharedLibrariesIds": []string{}, "labelsAllow": []string{}, "labelsExclude": []string{},
	}
}

func komgaTestKey() map[string]any {
	return map[string]any{
		"id": "owned-key", "userId": "limited-user", "comment": "self-host Homarr statistics",
		"createdDate": "2026-11-03T08:00:00Z", "lastModifiedDate": "2026-11-03T08:00:00Z",
	}
}

func komgaTestPeer(t *testing.T, user map[string]any) *helpers.H {
	t.Helper()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("Komga verification must not mutate the peer")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if name, password, ok := r.BasicAuth(); ok && name == "admin@selfhost.local" && password == "admin-password" && r.URL.Path == "/api/v2/users/me" {
			json.NewEncoder(w).Encode(map[string]any{"id": "admin-id", "email": name, "roles": []string{"ADMIN", "USER"}})
			return
		}
		if r.Header.Get("X-API-Key") != "owned-private-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v2/users/me":
			json.NewEncoder(w).Encode(user)
		case "/api/v2/users/me/api-keys":
			json.NewEncoder(w).Encode([]any{komgaTestKey()})
		default:
			t.Errorf("unexpected Komga data action: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(peer.Close)
	fingerprint := sha256.Sum256([]byte("owned-private-key"))
	ownership, err := json.Marshal(map[string]any{"key": komgaTestKey(), "email": "metrics@selfhost.local", "fingerprint": hex.EncodeToString(fingerprint[:])})
	if err != nil {
		t.Fatal(err)
	}
	return helpers.New(helpers.Config{
		App: "komga", Service: "komga", ContainerPort: 25600, BaseURL: peer.URL, HTTP: peer.Client(), Log: events.Discard(),
		Secrets: statsSecrets{
			"ADMIN_USER": "admin", "ADMIN_PASSWORD": "admin-password", "KOMGA_ADMIN_USER_ID": "admin-id",
			"KOMGA_HOMARR_USER_ID": "limited-user", "KOMGA_HOMARR_USER_EMAIL": "metrics@selfhost.local",
			"KOMGA_HOMARR_API_KEY": "owned-private-key", "KOMGA_HOMARR_KEY_OWNERSHIP": string(ownership),
		},
	})
}

func TestKomgaDescriptorUsesExactKindCredentialAndRealCountKeys(t *testing.T) {
	to := komgaTestPeer(t, komgaTestIdentity())
	descriptor := komgaTarget()
	if descriptor.id != "komga" || descriptor.kind != "komga" || descriptor.internal(to) != "http://komga:25600" || !reflect.DeepEqual(descriptor.credentials(nil, to), []secret{{Kind: "apiKey", Value: "owned-private-key"}}) || !reflect.DeepEqual(descriptor.statsMetrics(), []string{"libraries", "series", "books"}) {
		t.Fatal("Komga descriptor changed its sourced native contract")
	}
	if len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" || descriptor.widgets[0].bind != nil {
		t.Fatal("Komga invented a library selector or unsupported dedicated widget")
	}
}

func TestKomgaUsesOneAdvancingCentralSampleAfterSavedIntegrationIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    func(*statsFixture, map[string]any)
		wantOK    bool
		refreshes int
	}{
		{name: "legitimate empty libraries", wantOK: true, refreshes: 1},
		{name: "large integral count", wantOK: true, refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.values["books"] = 1e20 }},
		{name: "missing count", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { delete(f.values, "libraries") }},
		{name: "null count", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.values["series"] = nil }},
		{name: "negative count", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.values["books"] = -1 }},
		{name: "fractional count", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.values["books"] = 0.5 }},
		{name: "wrong units", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "missing catalog", change: func(f *statsFixture, _ map[string]any) { f.metrics = f.metrics[:2] }},
		{name: "no-op refresh", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.advance = false }},
		{name: "retry hold", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.retryAt = float64(9000) }},
		{name: "missing retry metadata", refreshes: 1, change: func(f *statsFixture, _ map[string]any) { f.hasRetryAt = false }},
		{name: "wrong saved user", change: func(_ *statsFixture, response map[string]any) {
			response["data"].(map[string]any)["response"].(map[string]any)["id"] = "another-user"
		}},
		{name: "wrong saved permissions", change: func(_ *statsFixture, response map[string]any) {
			response["data"].(map[string]any)["response"].(map[string]any)["roles"] = []string{"USER", "ADMIN"}
		}},
		{name: "wrong integration ID", change: func(_ *statsFixture, response map[string]any) {
			response["integration"].(map[string]any)["id"] = "another-integration"
		}},
		{name: "wrong integration kind", change: func(_ *statsFixture, response map[string]any) {
			response["integration"].(map[string]any)["kind"] = "immich"
		}},
		{name: "rejected authentication", change: func(_ *statsFixture, response map[string]any) { response["ok"], response["status"] = false, 401 }},
		{name: "missing identity", change: func(_ *statsFixture, response map[string]any) { delete(response, "data") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"libraries": 0, "series": 0, "books": 0}
			fixture.metrics = []any{}
			fixture.hasRetryAt, fixture.retryAt = true, float64(0)
			for _, metric := range []string{"libraries", "series", "books"} {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": metric, "unit": "count"})
			}
			response := map[string]any{
				"ok": true, "status": 200, "integration": map[string]any{"id": "komga-owned", "kind": "komga"},
				"data": map[string]any{"response": komgaTestIdentity()},
			}
			if scenario.change != nil {
				scenario.change(fixture, response)
			}
			from := fixture.helpers(t)
			backend, err := url.Parse(from.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(backend)
			identityReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/trpc/integration.request" {
					proxy.ServeHTTP(w, r)
					return
				}
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.Method != http.MethodPost || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "komga-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/api/v2/users/me" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				identityReads++
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": response}}})
			}))
			t.Cleanup(server.Close)
			from.BaseURL, from.Config.HTTP = server.URL, server.Client()
			to := komgaTestPeer(t, komgaTestIdentity())
			err = komgaTarget().verifyData(t.Context(), from, to, "komga-owned")
			if (err == nil) != scenario.wantOK || fixture.refreshes != scenario.refreshes || identityReads != 1 || fixture.refreshes > 0 && !fixture.force {
				t.Fatalf("identity=%d refreshes=%d forced=%v expectedOK=%v error=%v", identityReads, fixture.refreshes, fixture.force, scenario.wantOK, err)
			}
		})
	}
}

func TestKomgaPreflightRejectsMissingCredentialsBeforeHomarrRequests(t *testing.T) {
	to := komgaTestPeer(t, komgaTestIdentity())
	to.Secrets.Set("KOMGA_HOMARR_API_KEY", "")
	fixture := newStatsFixture()
	from := fixture.helpers(t)
	if err := komgaTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) || fixture.catalogs != 0 || fixture.refreshes != 0 || fixture.snapshots != 0 {
		t.Fatalf("missing credentials passed preflight or acquired Homarr data: %v", err)
	}
}

func TestKomgaStatsRejectNonfiniteCountsThroughSharedValidator(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		sample := statsSample{values: map[string]any{"libraries": 0.0, "series": 0.0, "books": value}, units: map[string]string{"libraries": "count", "series": "count", "books": "count"}, retryAt: float64(0)}
		if verifyKomgaStats(sample) == nil {
			t.Fatal("nonfinite Komga count was accepted")
		}
	}
}
