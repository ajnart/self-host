package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

const healthchecksTestProject = "27d76ba7-a070-4fe1-89ba-20c425a30579"
const healthchecksTestBadge = "http://healthchecks.example.invalid/badge/selected-project/abcDEF12-2.svg"
const healthchecksTestKey = "synthetic-read-only-project-key"

func healthchecksPeer(t *testing.T, checks string) (*helpers.H, *atomic.Int32) {
	t.Helper()
	reads := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet {
			t.Error("connector attempted an App/provider action")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/accounts/profile/":
			if cookie, err := r.Cookie("sessionid"); err != nil || cookie.Value != "fixture-session" {
				t.Error("missing existing App session")
			}
			fmt.Fprintf(w, `<h1 class="settings-title">Settings <small>admin@selfhost.local</small></h1><table id="my-projects"><tr><td><a href="/projects/%s/settings/">Settings</a></td><td>Owner</td></tr></table>`, healthchecksTestProject)
		case "/projects/" + healthchecksTestProject + "/badges/":
			fmt.Fprintf(w, `<div id="preview"><h2>Preview</h2><img src="%s"></div>`, healthchecksTestBadge)
		case "/api/v3/badges/", "/api/v3/channels/", "/api/v3/checks/":
			if r.Header.Get("X-Api-Key") != healthchecksTestKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/api/v3/channels/" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/api/v3/checks/" {
				w.Write([]byte(checks))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"badges": map[string]any{"*": map[string]any{"svg": healthchecksTestBadge}}})
		default:
			t.Errorf("connector used an unexpected peer endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	fingerprint := sha256.Sum256([]byte(healthchecksTestKey))
	ownership := fmt.Sprintf(`{"version":1,"selection":{"owner":"admin@selfhost.local","projectId":"%s","badge":"%s"},"fingerprint":"%s"}`, healthchecksTestProject, healthchecksTestBadge, hex.EncodeToString(fingerprint[:]))
	h := helpers.New(helpers.Config{
		App: "healthchecks", Service: "healthchecks", ContainerPort: 8000, Port: 8000,
		BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Log: events.Discard(), Inputs: map[string]string{"project_id": healthchecksTestProject},
		Secrets: statsSecrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "fixture-password", "HEALTHCHECKS_HOMARR_API_KEY": healthchecksTestKey, "HEALTHCHECKS_HOMARR_KEY_OWNERSHIP": ownership},
	})
	if _, err := h.Remember("healthchecks-session", func() (string, error) { return "fixture-session", nil }); err != nil {
		t.Fatal(err)
	}
	return h, reads
}

func TestHealthchecksIdentityBeforeOneCentralStatsSample(t *testing.T) {
	for _, scenario := range []struct {
		name              string
		change            func(*statsFixture)
		native            string
		peerChange        func(*helpers.H)
		wantOK            bool
		catalogs, samples int
	}{
		{name: "new check without a ping", wantOK: true, catalogs: 1, samples: 1},
		{name: "empty or paused project zeros", wantOK: true, catalogs: 1, samples: 1, change: func(f *statsFixture) { f.values["checksNew"] = 0 }},
		{name: "negative count", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.values["checksDown"] = -1 }},
		{name: "fractional count", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.values["checksGrace"] = 0.5 }},
		{name: "text count", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.values["checksNew"] = "0" }},
		{name: "missing count", catalogs: 1, samples: 1, change: func(f *statsFixture) { delete(f.values, "checksUp") }},
		{name: "null count", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.values["checksUp"] = nil }},
		{name: "wrong unit", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "missing catalog entry", catalogs: 1, change: func(f *statsFixture) { f.metrics = f.metrics[:3] }},
		{name: "duplicate catalog entry", catalogs: 1, change: func(f *statsFixture) { f.metrics = append(f.metrics, f.metrics[0]) }},
		{name: "no advancing refresh", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.advance = false }},
		{name: "missing retry", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.hasRetryAt = false }},
		{name: "null retry", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.retryAt = nil }},
		{name: "pending retry", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.retryAt = 9000 }},
		{name: "text retry", catalogs: 1, samples: 1, change: func(f *statsFixture) { f.retryAt = "0" }},
		{name: "wrong saved project", native: "badge"},
		{name: "wrong returned integration", native: "id"},
		{name: "wrong kind", native: "kind"},
		{name: "body error", native: "body"},
		{name: "upstream error", native: "status"},
		{name: "missing badge", native: "missing"},
		{name: "write-capable saved key", native: "write scope"},
		{name: "wrong scope integration", native: "scope id"},
		{name: "pending App issuance", peerChange: func(h *helpers.H) { h.Secrets.(statsSecrets)[h.Key("homarr_key_pending")] = `corrupt` }},
		{name: "wrong App selection", peerChange: func(h *helpers.H) { h.Inputs["project_id"] = "37d76ba7-a070-4fe1-89ba-20c425a30579" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"checksUp": 0, "checksDown": 0, "checksGrace": 0, "checksNew": 1}
			fixture.metrics = nil
			fixture.hasRetryAt, fixture.retryAt = true, float64(0)
			for _, key := range []string{"checksUp", "checksDown", "checksGrace", "checksNew"} {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			var mu sync.Mutex
			native := 0
			var sequence []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				sequence = append(sequence, r.URL.Path)
				if r.URL.Path != "/api/trpc/integration.request" {
					fixture.ServeHTTP(w, r)
					return
				}
				var input struct {
					JSON struct{ IntegrationID, Method, Path string }
				}
				if r.Method != http.MethodPost || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "healthchecks-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/api/v3/badges/" && input.JSON.Path != "/api/v3/channels/" {
					t.Error("native query did not use the exact recorded read-only integration")
					w.WriteHeader(400)
					return
				}
				native++
				integration := map[string]any{"id": "healthchecks-owned", "kind": "healthchecks"}
				data := map[string]any{"badges": map[string]any{"*": map[string]any{"svg": healthchecksTestBadge}}}
				value := map[string]any{"ok": true, "status": 200, "integration": integration, "data": data}
				if input.JSON.Path == "/api/v3/channels/" {
					value["ok"], value["status"], value["data"] = false, 401, map[string]any{"error": "wrong api key"}
					if scenario.native == "write scope" {
						value["ok"], value["status"] = true, 200
					}
					if scenario.native == "scope id" {
						integration["id"] = "other-integration"
					}
					json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
					return
				}
				switch scenario.native {
				case "badge":
					data["badges"] = map[string]any{"*": map[string]any{"svg": strings.ReplaceAll(healthchecksTestBadge, "selected-project", "other-project")}}
				case "id":
					integration["id"] = "other-integration"
				case "kind":
					integration["kind"] = "gatus"
				case "body":
					data["error"] = nil
				case "status":
					value["status"] = 401
					value["ok"] = false
				case "missing":
					delete(data, "badges")
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
			}))
			t.Cleanup(server.Close)
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			to, _ := healthchecksPeer(t, `{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":0,"last_ping":null}]}`)
			if scenario.peerChange != nil {
				scenario.peerChange(to)
			}
			err := healthchecksTarget().verifyData(t.Context(), from, to, "healthchecks-owned")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("verification success=%v, want %v: %v", err == nil, scenario.wantOK, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if fixture.catalogs != scenario.catalogs || fixture.snapshots != scenario.samples || fixture.refreshes != scenario.samples || fixture.refreshes > 0 && (!fixture.force || fixture.integrationID != "healthchecks-owned") {
				t.Fatal("semantic failure or success did not use exactly one central acquisition")
			}
			if scenario.catalogs > 0 && (native != 2 || len(sequence) < 2 || sequence[0] != "/api/trpc/integration.request" || sequence[1] != "/api/trpc/integration.request") {
				t.Fatal("Stats was acquired before exact selected-project identity")
			}
			if scenario.peerChange != nil && native != 0 {
				t.Fatal("unready App or pending issuance reached Homarr data")
			}
		})
	}
}

func TestHealthchecksPreflightBlocksMutationAndUsesExactDescriptor(t *testing.T) {
	for _, scenario := range []string{"missing key", "missing ownership", "corrupt pending", "wrong selected project", "wrong owner"} {
		t.Run(scenario, func(t *testing.T) {
			to, _ := healthchecksPeer(t, `{"checks":[]}`)
			stored := to.Secrets.(statsSecrets)
			switch scenario {
			case "missing key":
				delete(stored, to.Key("homarr_api_key"))
			case "missing ownership":
				delete(stored, to.Key("homarr_key_ownership"))
			case "corrupt pending":
				stored[to.Key("homarr_key_pending")] = `corrupt`
			case "wrong selected project":
				to.Inputs["project_id"] = "37d76ba7-a070-4fe1-89ba-20c425a30579"
			case "wrong owner":
				stored["ADMIN_USER"] = "different"
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			descriptor := healthchecksTarget()
			if _, err := descriptor.linked(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("read-only preflight did not preserve Manual")
			}
			if err := descriptor.add(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("apply preflight did not preserve Manual")
			}
			if requests.Load() != 0 {
				t.Fatal("unready App reached Homarr compatibility or mutation")
			}
		})
	}
	to, _ := healthchecksPeer(t, `{"checks":[]}`)
	descriptor := healthchecksTarget()
	credentials := descriptor.credentials(nil, to)
	if descriptor.id != "healthchecks" || descriptor.kind != "healthchecks" || descriptor.internal(to) != "http://healthchecks:8000" || len(credentials) != 1 || credentials[0].Kind != "apiKey" || credentials[0].Value != healthchecksTestKey || len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" || len(descriptor.statsMetrics()) != 4 {
		t.Fatal("descriptor changed the exact kind, private read-only credential, internal Address or supported widget surface")
	}
}
