package homarr

import (
	"bytes"
	"context"
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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/apps/komga"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
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
		if r.Header.Get("X-API-Key") != "owned-private-key" && r.Header.Get("X-API-Key") != "person-private-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v2/users/me":
			json.NewEncoder(w).Encode(user)
		case "/api/v2/users/me/api-keys":
			owned := komgaTestKey()
			owned["key"] = "******"
			person := komgaTestKey()
			person["id"], person["comment"], person["key"] = "person-key", "Person-managed key", "******"
			json.NewEncoder(w).Encode([]any{owned, person})
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

func TestKomgaAccountEnvelopeAndStatsControlsDoNotAttestSavedKey(t *testing.T) {
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
			response["data"].(map[string]any)["id"] = "another-user"
		}},
		{name: "wrong saved permissions", change: func(_ *statsFixture, response map[string]any) {
			response["data"].(map[string]any)["roles"] = []string{"USER", "ADMIN"}
		}},
		{name: "wrong integration ID", change: func(_ *statsFixture, response map[string]any) {
			response["integration"].(map[string]any)["id"] = "another-integration"
		}},
		{name: "wrong integration kind", change: func(_ *statsFixture, response map[string]any) {
			response["integration"].(map[string]any)["kind"] = "immich"
		}},
		{name: "rejected authentication", change: func(_ *statsFixture, response map[string]any) { response["ok"], response["status"] = false, 401 }},
		{name: "missing identity", change: func(_ *statsFixture, response map[string]any) { delete(response, "data") }},
		{name: "invented nested response", change: func(_ *statsFixture, response map[string]any) {
			response["data"] = map[string]any{"response": komgaTestIdentity()}
		}},
		{name: "null identity", change: func(_ *statsFixture, response map[string]any) { response["data"] = nil }},
		{name: "array identity", change: func(_ *statsFixture, response map[string]any) { response["data"] = []any{komgaTestIdentity()} }},
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
				"ok": true, "status": 200, "statusText": "OK", "integration": map[string]any{"id": "komga-owned", "name": "Komga", "kind": "komga"},
				"data": komgaTestIdentity(),
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
			control := komgaTarget()
			control.verify = verifyKomgaAccount
			err = control.verifyData(t.Context(), from, to, "komga-owned")
			if (err == nil) != scenario.wantOK || fixture.refreshes != scenario.refreshes || identityReads != 1 || fixture.refreshes > 0 && !fixture.force {
				t.Fatalf("identity=%d refreshes=%d forced=%v expectedOK=%v error=%v", identityReads, fixture.refreshes, fixture.force, scenario.wantOK, err)
			}
			fixture.catalogs, fixture.snapshots, fixture.refreshes = 0, 0, 0
			err = komgaTarget().verifyData(t.Context(), from, to, "komga-owned")
			if !errors.Is(err, helpers.ErrManual) || fixture.catalogs != 0 || fixture.snapshots != 0 || fixture.refreshes != 0 {
				t.Fatal("production verifier treated account/Stats controls as exact saved-key proof")
			}
			if scenario.wantOK && !strings.Contains(err.Error(), "exact locally recorded key") {
				t.Fatal("valid account verification did not retain the exact-key hold")
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

func TestKomgaPreflightHoldsEvenWithValidLocalCredentials(t *testing.T) {
	to := komgaTestPeer(t, komgaTestIdentity())
	from := newStatsFixture().helpers(t)
	if err := komgaTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("account identity and local issuance fingerprint cannot prove Homarr's exact saved key")
	}
}

func TestKomgaAccountReadsCannotDistinguishTwoValidKeys(t *testing.T) {
	to := komgaTestPeer(t, komgaTestIdentity())
	if err := komga.VerifyHomarr(t.Context(), to); err != nil {
		t.Fatal("positive control did not verify the locally captured key")
	}
	for _, path := range []string{"/api/v2/users/me", "/api/v2/users/me/api-keys"} {
		var first []byte
		for _, key := range []string{"owned-private-key", "person-private-key"} {
			response, err := to.HTTP(t.Context(), helpers.Request{Path: path, Header: map[string]string{"X-API-Key": key}})
			if err != nil || response.Status != http.StatusOK {
				t.Fatal("same-account positive control rejected a valid key")
			}
			if first != nil && !bytes.Equal(first, response.Body) {
				t.Fatal("source-shaped account responses should not identify the authenticating key")
			}
			if bytes.Contains(response.Body, []byte(key)) {
				t.Fatal("source-shaped account response exposed an unredacted secret")
			}
			first = response.Body
		}
	}
}

func TestKomgaSavedKeyHoldPrecedesCheckApplyAndDriftReconciliation(t *testing.T) {
	for _, savedKey := range []string{"owned-private-key", "person-private-key"} {
		for _, drift := range []string{"none", "Address", "fingerprint"} {
			for _, operation := range []string{"Check", "Apply", "LinkApply"} {
				t.Run(savedKey[:5]+"/"+drift+"/"+operation, func(t *testing.T) {
					to := komgaTestPeer(t, komgaTestIdentity())
					if err := komga.VerifyHomarr(t.Context(), to); err != nil {
						t.Fatal("local-key positive control was not ready")
					}
					resources := map[string]any{
						"integration":     map[string]any{"id": "komga-owned", "kind": "komga", "url": "http://komga:25600", "savedKey": savedKey},
						"userIntegration": map[string]any{"id": "person-integration", "kind": "komga", "savedKey": "person-private-key"},
						"userWidget":      map[string]any{"id": "person-stats", "integrationIds": []any{"komga-owned", "unrelated"}, "options": map[string]any{"title": "Keep my layout"}},
					}
					var requests atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						w.WriteHeader(http.StatusForbidden)
					}))
					t.Cleanup(server.Close)
					store := statsSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_APP_IDS": `{"komga":"owned-app"}`, "HOMARR_LINKED_KOMGA": "komga-owned"}
					from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: store})
					descriptor := komgaTarget()
					store[integrationFingerprintKey(from, "komga-owned")] = integrationFingerprint(descriptor, from, to)
					if drift == "Address" {
						to.Service, to.URL = "komga-new", "http://changed.example:25600"
					}
					if drift == "fingerprint" {
						store[integrationFingerprintKey(from, "komga-owned")] = "previous-local-fingerprint"
					}
					from.InstalledApps = func(context.Context) ([]helpers.InstalledApp, error) {
						t.Error("exact-key hold reached Address reconciliation")
						return nil, errors.New("unexpected installed App read")
					}
					before := komgaTestSnapshot(t, resources, store, to.Secrets)
					var recorded helpers.StepResult
					from.Record = func(result helpers.StepResult) { recorded = result }
					var err error
					switch operation {
					case "Check":
						var ready bool
						ready, err = descriptor.linked(t.Context(), from, to)
						if ready {
							t.Fatal("Check treated a local fingerprint as saved-key proof")
						}
					case "Apply":
						err = descriptor.add(t.Context(), from, to)
					case "LinkApply":
						err = descriptor.link().Apply(t.Context(), from, to)
						if recorded.Status != stepstatus.StepManual || !strings.Contains(recorded.Hint, "exact locally recorded key") || recorded.Error != "" {
							t.Fatal("public Link did not record the actionable exact-key Manual hold")
						}
					}
					if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "exact locally recorded key") || requests.Load() != 0 {
						t.Fatal("exact-key hold did not stop before all common Homarr reads and writes")
					}
					if !bytes.Equal(before, komgaTestSnapshot(t, resources, store, to.Secrets)) {
						t.Fatal("held reconciliation changed a saved key, user resource or ownership record")
					}
					for _, key := range []string{"owned-private-key", "person-private-key", "fixture-key", "admin-password"} {
						if strings.Contains(err.Error(), key) || strings.Contains(recorded.Hint, key) {
							t.Fatal("exact-key recovery exposed a credential")
						}
					}
				})
			}
		}
	}
}

func komgaTestSnapshot(t *testing.T, values ...any) []byte {
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestKomgaUnownedRemovalRemainsPeerIndependentUnderSavedKeyHold(t *testing.T) {
	userIntegration := map[string]any{"id": "person-integration", "kind": "komga", "url": "http://komga:25600"}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Error("unowned resources must not be changed on removal")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/apps":
			json.NewEncoder(w).Encode([]any{map[string]any{"id": "person-app", "pingUrl": "http://komga:25600", "description": "My bookmark"}})
		case "/api/trpc/integration.all":
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": []any{userIntegration}}}})
		default:
			t.Error("removal reached peer verification, Stats or another unrelated request")
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(server.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
	to := helpers.New(helpers.Config{App: "komga", Service: "komga", ContainerPort: 25600, Secrets: statsSecrets{}})
	var recorded helpers.StepResult
	from.Record = func(result helpers.StepResult) { recorded = result }
	if err := komgaTarget().link().Remove(t.Context(), from, to); err != nil || recorded.Status != stepstatus.StepSkip || requests.Load() != 2 {
		t.Fatalf("unowned removal required peer credentials or the Apply preflight: %v", err)
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
