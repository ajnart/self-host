package homarr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

const (
	homeboxTestAccount = "11111111-1111-4111-8111-111111111111"
	homeboxTestGroup   = "22222222-2222-4222-8222-222222222222"
)

func TestHomeboxTargetDescriptor(t *testing.T) {
	target := homeboxTarget()
	if target.kind != "homebox" || target.internal(nil) != "http://homebox:7745" || len(target.widgets) != 1 || target.widgets[0].kind != "stats" {
		t.Fatal("Homebox target lost its kind, internal address or single Stats widget")
	}
	if len(target.widgets[0].metrics) != 3 {
		t.Fatal("Homebox Stats metrics changed")
	}
}

func TestHomeboxDescriptorStatsVerification(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    func(*statsFixture)
		peerID    string
		savedID   string
		wantOK    bool
		catalogs  int
		refreshes int
	}{
		{name: "populated counts", wantOK: true, catalogs: 1, refreshes: 1},
		{name: "explicit zero items", change: func(f *statsFixture) { f.values["items"] = 0 }, wantOK: true, catalogs: 1, refreshes: 1},
		{name: "wrong peer account", peerID: "33333333-3333-4333-8333-333333333333"},
		{name: "wrong saved account", savedID: "33333333-3333-4333-8333-333333333333"},
		{name: "zero users", change: func(f *statsFixture) { f.values["users"] = 0 }, catalogs: 1, refreshes: 1},
		{name: "negative items", change: func(f *statsFixture) { f.values["items"] = -1 }, catalogs: 1, refreshes: 1},
		{name: "fractional locations", change: func(f *statsFixture) { f.values["locations"] = 0.5 }, catalogs: 1, refreshes: 1},
		{name: "null users", change: func(f *statsFixture) { f.values["users"] = nil }, catalogs: 1, refreshes: 1},
		{name: "missing locations", change: func(f *statsFixture) { delete(f.values, "locations") }, catalogs: 1, refreshes: 1},
		{name: "wrong unit", change: func(f *statsFixture) { f.metrics[1].(map[string]any)["unit"] = "text" }, catalogs: 1, refreshes: 1},
		{name: "missing metric catalog", change: func(f *statsFixture) { f.metrics = f.metrics[:2] }, catalogs: 1},
		{name: "no-op refresh", change: func(f *statsFixture) { f.advance = false }, catalogs: 1, refreshes: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"items": 3, "locations": 8, "users": 1}
			fixture.metrics = []any{}
			for _, key := range []string{"items", "locations", "users"} {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			peerID, savedID := scenario.peerID, scenario.savedID
			if peerID == "" {
				peerID = homeboxTestAccount
			}
			if savedID == "" {
				savedID = homeboxTestAccount
			}
			from := fixture.helpers(t)
			statsURL, err := url.Parse(from.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(statsURL)
			savedQueries := 0
			homarrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/trpc/integration.request" {
					proxy.ServeHTTP(w, r)
					return
				}
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.Method != "POST" || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "homebox-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/api/v1/users/self" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				savedQueries++
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
					"ok": true, "status": 200, "integration": map[string]any{"id": "homebox-owned", "kind": "homebox"},
					"data": map[string]any{"item": map[string]any{"id": savedID}},
				}}}})
			}))
			t.Cleanup(homarrServer.Close)
			from.BaseURL, from.Config.HTTP = homarrServer.URL, homarrServer.Client()
			peerQueries := 0
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				peerQueries++
				switch r.URL.Path {
				case "/api/v1/users/self":
					json.NewEncoder(w).Encode(map[string]any{"item": map[string]any{"id": peerID, "email": "admin@selfhost.local", "defaultGroupId": homeboxTestGroup, "groupIds": []string{homeboxTestGroup}}})
				case "/api/v1/groups":
					json.NewEncoder(w).Encode(map[string]any{"id": homeboxTestGroup, "currency": "USD"})
				case "/api/v1/groups/statistics":
					json.NewEncoder(w).Encode(map[string]any{"totalItems": 0, "totalLocations": 8, "totalTags": 6, "totalWithWarranty": 0, "totalUsers": 1, "totalItemPrice": 0})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(peer.Close)
			identity := `{"accountId":"` + homeboxTestAccount + `","email":"admin@selfhost.local","groupId":"` + homeboxTestGroup + `"}`
			to := helpers.New(helpers.Config{
				App: "homebox", BaseURL: peer.URL, HTTP: peer.Client(), Log: events.Discard(),
				Secrets: statsSecrets{"ADMIN_USER": "admin", "HOMEBOX_API_KEY": "fixture-token", "HOMEBOX_API_KEY_PEPPER": "pppppppppppppppppppppppppppppppp", "HOMEBOX_ACCOUNT_IDENTITY": identity},
			})
			err = homeboxTarget().verifyData(t.Context(), from, to, "homebox-owned")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("verification: %v, want success %v", err, scenario.wantOK)
			}
			if peerQueries == 0 || fixture.catalogs != scenario.catalogs || fixture.refreshes != scenario.refreshes || fixture.refreshes > 0 && !fixture.force {
				t.Fatalf("peer=%d saved=%d catalogs=%d refreshes=%d force=%v", peerQueries, savedQueries, fixture.catalogs, fixture.refreshes, fixture.force)
			}
			if peerID == homeboxTestAccount && savedQueries != 1 || peerID != homeboxTestAccount && savedQueries != 0 {
				t.Fatalf("unexpected saved-account query count: %d", savedQueries)
			}
		})
	}
}
