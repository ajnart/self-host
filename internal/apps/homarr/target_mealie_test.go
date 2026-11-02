package homarr

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/ajnart/self-host/internal/apps/mealie"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

const (
	mealiePeerUser      = "a2cc03be-55f7-4d96-a7f9-a1f63e084001"
	mealiePeerGroup     = "a2cc03be-55f7-4d96-a7f9-a1f63e084002"
	mealiePeerHousehold = "a2cc03be-55f7-4d96-a7f9-a1f63e084003"
)

func mealiePeer(t *testing.T) *helpers.H {
	t.Helper()
	issued := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/app/about":
			json.NewEncoder(w).Encode(map[string]any{"version": "3.30.0", "production": true, "demoStatus": false, "allowPasswordLogin": true})
		case "POST /api/auth/token":
			if r.ParseForm() != nil || r.Form.Get("username") != "owner" || r.Form.Get("password") != "fixture-password" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "mealie-session", "token_type": "bearer"})
		case "POST /api/users/api-tokens":
			if issued || r.Header.Get("Authorization") != "Bearer mealie-session" {
				w.WriteHeader(409)
				return
			}
			issued = true
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "name": "self-host Homarr statistics", "createdAt": "2026-11-02T12:00:00Z", "token": "mealie-key"})
		case "GET /api/users/self", "GET /api/households/self":
			if r.Header.Get("Authorization") != "Bearer mealie-session" && (!issued || r.Header.Get("Authorization") != "Bearer mealie-key") {
				w.WriteHeader(401)
				return
			}
			if r.URL.Path == "/api/households/self" {
				json.NewEncoder(w).Encode(map[string]any{"id": mealiePeerHousehold, "groupId": mealiePeerGroup, "group": "Group", "name": "Household"})
				return
			}
			tokens := []any{}
			if issued {
				tokens = append(tokens, map[string]any{"id": 7, "name": "self-host Homarr statistics", "createdAt": "2026-11-02T12:00:00Z"})
			}
			json.NewEncoder(w).Encode(map[string]any{"id": mealiePeerUser, "groupId": mealiePeerGroup, "householdId": mealiePeerHousehold, "username": "owner", "email": "owner@example.test", "admin": true, "group": "Group", "household": "Household", "tokens": tokens})
		default:
			t.Errorf("unexpected Mealie request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	h := helpers.New(helpers.Config{App: "mealie", Service: "mealie", ContainerPort: 9000, BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{
		"ADMIN_USER": "owner", "ADMIN_PASSWORD": "fixture-password", "MEALIE_ADMIN_USER_ID": mealiePeerUser, "MEALIE_GROUP_ID": mealiePeerGroup, "MEALIE_HOUSEHOLD_ID": mealiePeerHousehold,
	}})
	if err := (mealie.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestMealieDescriptorVerifiesStoredIdentityBeforeOneFreshStatsSample(t *testing.T) {
	for _, scenario := range []struct {
		name                 string
		change               func(*statsFixture)
		wrongPath, wrongKind bool
		wantOK               bool
		refreshes            int
	}{
		{name: "empty recipes with real household user", wantOK: true, refreshes: 1},
		{name: "populated", change: func(f *statsFixture) { f.values["recipes"] = 3; f.values["tags"] = 4 }, wantOK: true, refreshes: 1},
		{name: "wrong stored household", wrongPath: true},
		{name: "wrong stored integration kind", wrongKind: true},
		{name: "zero users", change: func(f *statsFixture) { f.values["users"] = 0 }, refreshes: 1},
		{name: "missing recipes", change: func(f *statsFixture) { delete(f.values, "recipes") }, refreshes: 1},
		{name: "null tags", change: func(f *statsFixture) { f.values["tags"] = nil }, refreshes: 1},
		{name: "fractional categories", change: func(f *statsFixture) { f.values["categories"] = 0.5 }, refreshes: 1},
		{name: "negative recipes", change: func(f *statsFixture) { f.values["recipes"] = -1 }, refreshes: 1},
		{name: "wrong units", change: func(f *statsFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }, refreshes: 1},
		{name: "missing catalog", change: func(f *statsFixture) { f.metrics = f.metrics[:3] }},
		{name: "no-op cached snapshot", change: func(f *statsFixture) { f.advance = false }, refreshes: 1},
		{name: "pending retry", change: func(f *statsFixture) { f.retryAt = 100 }, refreshes: 1},
		{name: "missing retry metadata", change: func(f *statsFixture) { f.hasRetryAt = false }, refreshes: 1},
		{name: "future timestamp", change: func(f *statsFixture) { f.timestamp = 9e15 }, refreshes: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			peer := mealiePeer(t)
			fixture := newStatsFixture()
			fixture.values = map[string]any{"recipes": 0, "users": 1, "categories": 0, "tags": 0}
			fixture.metrics = []any{}
			fixture.hasRetryAt = true
			fixture.retryAt = 0
			for _, key := range []string{"recipes", "users", "categories", "tags"} {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			from := fixture.helpers(t)
			u, _ := url.Parse(from.BaseURL)
			proxy := httputil.NewSingleHostReverseProxy(u)
			identityReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/trpc/integration.request" {
					proxy.ServeHTTP(w, r)
					return
				}
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.Method != "POST" || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "mealie-owned" || input.JSON.Method != "GET" {
					w.WriteHeader(400)
					return
				}
				identityReads++
				data := map[string]any{"id": mealiePeerUser, "groupId": mealiePeerGroup, "householdId": mealiePeerHousehold, "admin": true}
				if input.JSON.Path == "/api/households/self" {
					data["id"] = mealiePeerHousehold
				} else if input.JSON.Path != "/api/users/self" {
					t.Error("unexpected integration path")
				}
				if scenario.wrongPath {
					data["groupId"] = "another-group"
				}
				kind := "mealie"
				if scenario.wrongKind {
					kind = "linkwarden"
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{"ok": true, "status": 200, "integration": map[string]any{"id": "mealie-owned", "kind": kind}, "data": data}}}})
			}))
			t.Cleanup(server.Close)
			from.BaseURL, from.Config.HTTP = server.URL, server.Client()
			descriptor := mealieTarget()
			if descriptor.kind != "mealie" || descriptor.internal(peer) != "http://mealie:9000" || len(descriptor.credentials(from, peer)) != 1 || descriptor.credentials(from, peer)[0].Kind != "apiKey" {
				t.Fatal("descriptor kind/address/credential drift")
			}
			err := descriptor.verifyData(t.Context(), from, peer, "mealie-owned")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("verification=%v wantOK=%v", err, scenario.wantOK)
			}
			if fixture.refreshes != scenario.refreshes || fixture.refreshes > 1 || fixture.refreshes > 0 && (!fixture.force || fixture.catalogs != 1 || fixture.snapshots != 1) {
				t.Fatalf("refreshes=%d catalogs=%d snapshots=%d force=%v", fixture.refreshes, fixture.catalogs, fixture.snapshots, fixture.force)
			}
			if identityReads == 0 || scenario.wantOK && identityReads != 2 {
				t.Fatal("stored integration identity was not verified")
			}
		})
	}
}

func TestMealieCountSemanticsRejectNonfiniteValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		sample := statsSample{values: map[string]any{"recipes": value, "users": float64(1), "categories": float64(0), "tags": float64(0)}, units: map[string]string{"recipes": "count", "users": "count", "categories": "count", "tags": "count"}, retryAt: float64(0)}
		if err := verifyMealieStats(sample); err == nil {
			t.Fatal("nonfinite count accepted")
		}
	}
}

func TestMealieMissingCredentialPreflightPreventsHomarrRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{}})
	to := helpers.New(helpers.Config{App: "mealie", Service: "mealie", ContainerPort: 9000, Log: events.Discard(), Secrets: statsSecrets{}})
	if err := mealieTarget().link().Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("missing prerequisite=%v", err)
	}
	if requests != 0 {
		t.Fatal("missing credentials reached Homarr or mutated its board")
	}
}

func TestMealieAbsentRemovalDoesNotProbePeerOrRefreshStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps":
			w.Write([]byte(`[]`))
		case "/api/trpc/integration.all":
			w.Write([]byte(`{"result":{"data":{"json":[]}}}`))
		default:
			t.Errorf("unexpected removal request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{}})
	to := helpers.New(helpers.Config{App: "mealie", Service: "mealie", ContainerPort: 9000, Log: events.Discard(), Secrets: statsSecrets{}})
	if err := mealieTarget().link().Remove(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
}
