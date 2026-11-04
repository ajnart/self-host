package homarr

import (
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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

const karakeepFixtureKey = "ak2_aaaaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const karakeepFixtureOtherKey = "ak2_cccccccccccccccccccc_dddddddddddddddddddddddddddddddd"

func karakeepPeerHelpers(t *testing.T) *helpers.H {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := map[string]any{"id": "selected-owner", "email": "admin@selfhost.local", "role": "admin"}
		var value any
		switch r.Method + " " + r.URL.Path {
		case "POST /api/auth/sign-in/email":
			http.SetCookie(w, &http.Cookie{Name: "better-auth.session_token", Value: "karakeep-fixture-session", Path: "/"})
			value = map[string]any{"token": "karakeep-fixture-token", "user": user}
		case "GET /api/auth/get-session":
			value = map[string]any{"user": user, "session": map[string]any{"userId": "selected-owner", "token": "karakeep-fixture-token", "expiresAt": "2099-01-01T00:00:00Z"}}
		case "GET /api/v1/users/me":
			if key := r.Header.Get("Authorization"); key != "" && key != "Bearer "+karakeepFixtureKey && key != "Bearer "+karakeepFixtureOtherKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			value = map[string]any{"id": "selected-owner", "email": "admin@selfhost.local", "localUser": true}
		case "GET /api/trpc/apiKeys.list":
			value = map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{"keys": []any{map[string]any{
				"id": "key-resource-1", "keyId": "aaaaaaaaaaaaaaaaaaaa", "name": "self-host Homarr statistics", "scopes": []string{"users:read"},
			}, map[string]any{"id": "key-resource-2", "keyId": "cccccccccccccccccccc", "name": "Existing same-account key", "scopes": []string{"users:read"}}}}}}}
		case "POST /api/auth/sign-out":
			value = map[string]any{"success": true}
		default:
			t.Errorf("unexpected Karakeep request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/api/auth/sign-in/email" && r.Header.Get("Authorization") == "" {
			cookie, err := r.Cookie("better-auth.session_token")
			if err != nil || cookie.Value != "karakeep-fixture-session" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(server.Close)
	fingerprint := sha256.Sum256([]byte(karakeepFixtureKey))
	ownership, err := json.Marshal(map[string]any{
		"id": "key-resource-1", "keyId": "aaaaaaaaaaaaaaaaaaaa", "name": "self-host Homarr statistics", "scopes": []string{"users:read"},
		"accountId": "selected-owner", "fingerprint": hex.EncodeToString(fingerprint[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return helpers.New(helpers.Config{
		App: "karakeep", Service: "karakeep", ContainerPort: 3000, BaseURL: server.URL, URL: "http://karakeep.example.test", HTTP: server.Client(), Log: events.Discard(),
		Secrets: statsSecrets{
			"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password", "KARAKEEP_ACCOUNT_ID": "selected-owner", "KARAKEEP_ADMIN_EMAIL": "admin@selfhost.local",
			"KARAKEEP_HOMARR_API_KEY": karakeepFixtureKey, "KARAKEEP_HOMARR_KEY_OWNERSHIP": string(ownership),
		},
	})
}

func karakeepNativeIdentity(id string) map[string]any {
	return map[string]any{"ok": true, "status": float64(200), "integration": map[string]any{"id": id, "kind": "karakeep"}, "data": map[string]any{"id": "selected-owner", "email": "admin@selfhost.local", "localUser": true}}
}

func TestKarakeepSavedSameAccountKeyCannotEstablishOwnedCredential(t *testing.T) {
	peer := karakeepPeerHelpers(t)
	for _, key := range []string{karakeepFixtureKey, karakeepFixtureOtherKey} {
		t.Run(key[4:24], func(t *testing.T) {
			statsRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
					statsRequests++
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if r.URL.Path != "/api/trpc/integration.request" {
					t.Errorf("unexpected Homarr request: %s", r.URL.Path)
				}
				response, err := peer.HTTP(r.Context(), helpers.Request{Path: "/api/v1/users/me", Header: map[string]string{"Authorization": "Bearer " + key}})
				if err != nil || response.Status != http.StatusOK {
					t.Error("saved fixture key did not authenticate")
				}
				var profile map[string]any
				if response.Decode(&profile) != nil || profile["id"] != "selected-owner" {
					t.Error("saved fixture key did not identify the same account")
				}
				identity := karakeepNativeIdentity("karakeep-owned")
				identity["data"] = profile
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": identity}}})
			}))
			t.Cleanup(server.Close)
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}, Log: events.Discard()})
			err := karakeepTarget().verifyData(t.Context(), from, peer, "karakeep-owned")
			if !errors.Is(err, helpers.ErrManual) || statsRequests != 0 {
				t.Fatalf("account-only response accepted a saved key without proof: %v; Stats requests %d", err, statsRequests)
			}
		})
	}
}

func TestKarakeepDescriptorKeepsAccountDiagnosticsManualBeforeStats(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "matching account with explicit zero counters"},
		{name: "wrong saved account", change: func(v map[string]any) { v["data"].(map[string]any)["id"] = "different-owner" }},
		{name: "wrong returned integration", change: func(v map[string]any) { v["integration"].(map[string]any)["id"] = "user-owned" }},
		{name: "wrong kind", change: func(v map[string]any) { v["integration"].(map[string]any)["kind"] = "linkwarden" }},
		{name: "native body error", change: func(v map[string]any) { v["data"].(map[string]any)["error"] = "failed" }},
		{name: "native outer error", change: func(v map[string]any) { v["error"] = "failed" }},
		{name: "missing identity", change: func(v map[string]any) { delete(v, "data") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.timestamp = 1700000000000 - 1
			fixture.values = map[string]any{}
			fixture.metrics = []any{}
			fixture.hasRetryAt, fixture.retryAt = true, float64(0)
			for _, metric := range karakeepStatsMetrics() {
				fixture.values[metric] = float64(0)
				fixture.metrics = append(fixture.metrics, map[string]any{"key": metric, "unit": "count"})
			}
			identity := karakeepNativeIdentity("karakeep-owned")
			if scenario.change != nil {
				scenario.change(identity)
			}
			from := fixture.helpers(t)
			statsAddress, err := url.Parse(from.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(statsAddress)
			nativeReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/trpc/integration.request" {
					proxy.ServeHTTP(w, r)
					return
				}
				nativeReads++
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.Method != http.MethodPost || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "karakeep-owned" || input.JSON.Method != http.MethodGet || input.JSON.Path != "/api/v1/users/me" {
					t.Error("saved native identity request changed")
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": identity}}})
			}))
			t.Cleanup(server.Close)
			from.BaseURL, from.Config.HTTP = server.URL, server.Client()
			from.Now = func() time.Time { return time.UnixMilli(1700000000000) }
			err = karakeepTarget().verifyData(t.Context(), from, karakeepPeerHelpers(t), "karakeep-owned")
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("verification = %v", err)
			}
			if nativeReads != 1 || fixture.catalogs != 0 || fixture.refreshes != 0 || fixture.snapshots != 0 {
				t.Fatalf("native=%d catalogs=%d snapshots=%d refreshes=%d", nativeReads, fixture.catalogs, fixture.snapshots, fixture.refreshes)
			}
		})
	}
}

func TestKarakeepCountCallbackRetainsOneCentralSampleContract(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    func(*statsFixture)
		fail      bool
		refreshes int
	}{
		{name: "explicit zeros", refreshes: 1},
		{name: "overlapping favorites and archives", refreshes: 1, change: func(f *statsFixture) {
			f.values = map[string]any{"bookmarks": float64(2), "favorites": float64(2), "archived": float64(2), "highlights": float64(8), "lists": float64(9), "tags": float64(12)}
		}},
		{name: "fractional tags", fail: true, refreshes: 1, change: func(f *statsFixture) { f.values["tags"] = 0.5 }},
		{name: "missing count", fail: true, refreshes: 1, change: func(f *statsFixture) { delete(f.values, "highlights") }},
		{name: "null count", fail: true, refreshes: 1, change: func(f *statsFixture) { f.values["tags"] = nil }},
		{name: "wrong unit", fail: true, refreshes: 1, change: func(f *statsFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "missing catalog entry", fail: true, change: func(f *statsFixture) { f.metrics = f.metrics[:5] }},
		{name: "no-op refresh", fail: true, refreshes: 1, change: func(f *statsFixture) { f.advance = false }},
		{name: "missing retry metadata", fail: true, refreshes: 1, change: func(f *statsFixture) { f.hasRetryAt = false }},
		{name: "pending retry", fail: true, refreshes: 1, change: func(f *statsFixture) { f.retryAt = float64(1) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.timestamp = 1700000000000 - 1
			fixture.values, fixture.metrics = map[string]any{}, []any{}
			fixture.hasRetryAt, fixture.retryAt = true, float64(0)
			for _, metric := range karakeepStatsMetrics() {
				fixture.values[metric] = float64(0)
				fixture.metrics = append(fixture.metrics, map[string]any{"key": metric, "unit": "count"})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			from := fixture.helpers(t)
			from.Now = func() time.Time { return time.UnixMilli(1700000000000) }
			callback := karakeepTarget()
			callback.verify = func(context.Context, *helpers.H, *helpers.H, string) error { return nil }
			err := callback.verifyData(t.Context(), from, nil, "karakeep-owned")
			if (err != nil) != scenario.fail {
				t.Fatalf("count callback = %v", err)
			}
			if fixture.catalogs != 1 || fixture.refreshes != scenario.refreshes || fixture.snapshots != scenario.refreshes {
				t.Fatal("count callback did not use one central sample")
			}
			if fixture.refreshes > 0 && (!fixture.force || fixture.integrationID != "karakeep-owned") {
				t.Fatal("shared refresh used the wrong integration")
			}
		})
	}
}

func TestKarakeepStatsRetainsCountRelationshipsAndMetadataPolicy(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*statsSample)
	}{
		{name: "negative", change: func(s *statsSample) { s.values["bookmarks"] = float64(-1) }},
		{name: "nonfinite", change: func(s *statsSample) { s.values["lists"] = math.Inf(1) }},
		{name: "text", change: func(s *statsSample) { s.values["tags"] = "0" }},
		{name: "too many favorites", change: func(s *statsSample) { s.values["favorites"] = float64(1) }},
		{name: "too many archived", change: func(s *statsSample) { s.values["archived"] = float64(1) }},
		{name: "null retry", change: func(s *statsSample) { s.retryAt = nil }},
		{name: "text retry", change: func(s *statsSample) { s.retryAt = "0" }},
		{name: "future timestamp", change: func(s *statsSample) { s.updatedAt = s.completedAt + 60001 }},
		{name: "old timestamp", change: func(s *statsSample) { s.updatedAt = s.startedAt - 3600001 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sample := statsSample{values: map[string]any{}, units: map[string]string{}, retryAt: float64(0), startedAt: 1700000000000, completedAt: 1700000000000, updatedAt: 1700000000000}
			for _, key := range karakeepStatsMetrics() {
				sample.values[key] = float64(0)
				sample.units[key] = "count"
			}
			scenario.change(&sample)
			if err := verifyKarakeepStats(sample); err == nil {
				t.Fatal("invalid Karakeep semantics were accepted")
			}
		})
	}
}

func TestKarakeepMissingCredentialStopsBeforeHomarrRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}, Log: events.Discard()})
	to := helpers.New(helpers.Config{App: "karakeep", Service: "karakeep", ContainerPort: 3000, Secrets: statsSecrets{}, Log: events.Discard()})
	target := karakeepTarget()
	if target.kind != "karakeep" || target.internal(to) != "http://karakeep:3000" || !slices.Equal(target.statsMetrics(), karakeepStatsMetrics()) {
		t.Fatal("native descriptor contract changed")
	}
	if _, err := target.linked(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Check did not retain Manual: %v", err)
	}
	if err := target.add(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("apply did not retain Manual: %v", err)
	}
	if requests != 0 {
		t.Fatal("missing Karakeep credentials reached Homarr mutation or data APIs")
	}
}

func KarakeepLifecycleFixtureForTest(t *testing.T) (app.Link, app.LinkCheck, *helpers.H, http.Handler, string) {
	stats := newStatsFixture()
	stats.timestamp = 1700000000000
	stats.values = map[string]any{}
	stats.metrics = []any{}
	stats.hasRetryAt, stats.retryAt = true, float64(0)
	for _, key := range karakeepStatsMetrics() {
		stats.values[key] = float64(0)
		stats.metrics = append(stats.metrics, map[string]any{"key": key, "unit": "count"})
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/integration.getKinds":
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": []any{map[string]any{"kind": "karakeep", "requiredSecrets": []any{[]any{"apiKey"}}}}}}})
		case r.URL.Path == "/api/trpc/integration.request":
			var input struct {
				JSON struct{ IntegrationID string } `json:"json"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": karakeepNativeIdentity(input.JSON.IntegrationID)}}})
		case strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats."):
			if strings.HasSuffix(r.URL.Path, "refresh") {
				stats.refreshes = 0
			}
			stats.ServeHTTP(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	target := karakeepTarget()
	peer := karakeepPeerHelpers(t)
	return target.link(), target.linked, peer, handler, integrationFingerprint(target, nil, peer)
}
