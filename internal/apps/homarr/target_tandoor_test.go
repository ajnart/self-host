package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func tandoorPersonalJSON(accountID int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`[{"id":81,"space":31,"user":{"id":%d,"username":"admin","is_active":true,"is_superuser":true},"active":true,"groups":[{"name":"admin"}]}]`, accountID))
}

func tandoorCurrentJSON() json.RawMessage {
	return json.RawMessage(`{"id":31,"created_by":{"id":17,"username":"admin","is_active":true,"is_superuser":true}}`)
}

func tandoorPeer(t *testing.T, now time.Time) (*helpers.H, *int) {
	t.Helper()
	reads := 0
	expires := now.Add(90 * 24 * time.Hour).Format(time.RFC3339)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer tda_fixture_read_key_1234" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		reads++
		switch r.URL.Path {
		case "/api/user-space/all_personal/":
			w.Write(tandoorPersonalJSON(17))
		case "/api/space/current/":
			w.Write(tandoorCurrentJSON())
		case "/api/space/":
			w.Write([]byte(`{"results":[{"id":44},{"id":31}]}`))
		case "/api/access-token/":
			json.NewEncoder(w).Encode([]map[string]any{{"id": 91, "scope": "read", "expires": expires, "token": "tda_************_******_***********1234"}})
		default:
			t.Errorf("unexpected Tandoor endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)
	fingerprint := sha256.Sum256([]byte("tda_fixture_read_key_1234"))
	ownership, err := json.Marshal(map[string]any{
		"tokenId": 91, "accountId": 17, "activeSpaceId": 31, "firstSpaceId": 44, "scope": "read", "expires": expires, "fingerprint": hex.EncodeToString(fingerprint[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return helpers.New(helpers.Config{
		App: "tandoor", Service: "tandoor", ContainerPort: 80, BaseURL: peer.URL, HTTP: peer.Client(), Log: events.Discard(), Now: func() time.Time { return now },
		Secrets: statsSecrets{"ADMIN_USER": "admin", "TANDOOR_HOMARR_API_KEY": "tda_fixture_read_key_1234", "TANDOOR_HOMARR_TOKEN_OWNERSHIP": string(ownership)},
	}), &reads
}

func TestTandoorNativeIdentityPrecedesOneCentralStatsRefresh(t *testing.T) {
	for _, test := range []struct {
		name          string
		savedAccount  int64
		savedFirst    int64
		wrongKind     bool
		change        func(*statsFixture)
		wantOK        bool
		wantRefreshes int
	}{
		{name: "legitimate empty recipes", wantOK: true, wantRefreshes: 1},
		{name: "wrong saved owner", savedAccount: 18},
		{name: "wrong saved first Space", savedFirst: 31},
		{name: "wrong kind", wrongKind: true},
		{name: "missing catalog", change: func(f *statsFixture) { f.metrics = f.metrics[:2] }},
		{name: "null keyword", change: func(f *statsFixture) { f.values["keywords"] = nil }, wantRefreshes: 1},
		{name: "no-op refresh", change: func(f *statsFixture) { f.advance = false }, wantRefreshes: 1},
		{name: "invalid recipe count", change: func(f *statsFixture) { f.values["recipes"] = 0.5 }, wantRefreshes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
			fixture := newStatsFixture()
			fixture.timestamp = float64(now.UnixMilli())
			fixture.values = map[string]any{"users": float64(1), "recipes": float64(0), "keywords": float64(0)}
			fixture.metrics = []any{}
			for _, key := range []string{"users", "recipes", "keywords"} {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
			}
			if test.change != nil {
				test.change(fixture)
			}
			from := fixture.helpers(t)
			from.Now = func() time.Time { return now }
			statsURL, err := url.Parse(from.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(statsURL)
			savedReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/trpc/integration.request" {
					proxy.ServeHTTP(w, r)
					return
				}
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.Method != "POST" || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "tandoor-owned" || input.JSON.Method != "GET" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				savedReads++
				id := test.savedAccount
				if id == 0 {
					id = 17
				}
				first := test.savedFirst
				if first == 0 {
					first = 44
				}
				var body json.RawMessage
				switch input.JSON.Path {
				case "/api/user-space/all_personal/":
					body = tandoorPersonalJSON(id)
				case "/api/space/current/":
					body = tandoorCurrentJSON()
				case "/api/space/":
					body = json.RawMessage(fmt.Sprintf(`[{"id":%d},{"id":31}]`, first))
				default:
					t.Errorf("unsupported saved request %s", input.JSON.Path)
					w.WriteHeader(400)
					return
				}
				kind := "tandoor"
				if test.wrongKind {
					kind = "other"
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
					"ok": true, "status": 200, "integration": map[string]any{"id": "tandoor-owned", "kind": kind}, "data": body,
				}}}})
			}))
			t.Cleanup(server.Close)
			from.BaseURL, from.Config.HTTP = server.URL, server.Client()
			to, peerReads := tandoorPeer(t, now)
			descriptor := tandoorTarget()
			if descriptor.kind != "tandoor" || descriptor.internal(to) != "http://tandoor:80" || len(descriptor.credentials(from, to)) != 1 || descriptor.credentials(from, to)[0].Kind != "apiKey" {
				t.Fatal("wrong Tandoor descriptor")
			}
			err = descriptor.verifyData(t.Context(), from, to, "tandoor-owned")
			if (err == nil) != test.wantOK || fixture.refreshes != test.wantRefreshes || *peerReads != 4 || savedReads != 3 && !test.wrongKind {
				t.Fatalf("verification=%v peer=%d saved=%d refresh=%d", err, *peerReads, savedReads, fixture.refreshes)
			}
			if fixture.refreshes > 0 && (!fixture.force || fixture.catalogs != 1 || fixture.snapshots != 1) {
				t.Fatal("Stats did not use one central forced sample")
			}
		})
	}
}

func TestTandoorStatsSemanticsUseReviewedCountHelpers(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*statsSample)
		valid  bool
	}{
		{"zero recipes and keywords", func(*statsSample) {}, true},
		{"no users", func(s *statsSample) { s.values["users"] = float64(0) }, false},
		{"negative recipes", func(s *statsSample) { s.values["recipes"] = float64(-1) }, false},
		{"fractional keywords", func(s *statsSample) { s.values["keywords"] = 0.5 }, false},
		{"nonfinite", func(s *statsSample) { s.values["keywords"] = math.Inf(1) }, false},
		{"missing", func(s *statsSample) { delete(s.values, "recipes") }, false},
		{"wrong unit", func(s *statsSample) { s.units["users"] = "bytes" }, false},
		{"old timestamp", func(s *statsSample) { s.updatedAt = 1 }, false},
		{"future timestamp", func(s *statsSample) { s.updatedAt = s.completedAt + 60_001 }, false},
		{"deferred", func(s *statsSample) { s.retryAt = float64(100) }, false},
		{"malformed retry", func(s *statsSample) { s.retryAt = "0" }, false},
		{"zero retry", func(s *statsSample) { s.retryAt = float64(0) }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sample := statsSample{values: map[string]any{"users": float64(1), "recipes": float64(0), "keywords": float64(0)}, units: map[string]string{"users": "count", "recipes": "count", "keywords": "count"}, startedAt: 100_000, completedAt: 100_010, updatedAt: 100_005}
			test.change(&sample)
			if err := verifyTandoorStats(sample); (err == nil) != test.valid {
				t.Fatalf("unexpected semantics: %v", err)
			}
		})
	}
}

func TestTandoorPreflightMissingCredentialDoesNotRequestHomarr(t *testing.T) {
	fixture := newStatsFixture()
	from := fixture.helpers(t)
	to, _ := tandoorPeer(t, time.Now())
	to.Secrets.Set("TANDOOR_HOMARR_API_KEY", "")
	if err := tandoorTarget().preflight(t.Context(), from, to); err == nil || fixture.catalogs != 0 || fixture.refreshes != 0 || fixture.snapshots != 0 {
		t.Fatalf("missing credentials did not stop preflight: %v", err)
	}
}
