package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestTubearchivistDescriptorStatsVerification(t *testing.T) {
	const token = "synthetic-token-0123456789abcdef"
	for _, scenario := range []struct {
		name      string
		change    func(*statsFixture)
		peerID    int
		savedID   int
		missing   bool
		wantOK    bool
		manual    bool
		catalogs  int
		refreshes int
	}{
		{name: "populated counts", wantOK: true, catalogs: 1, refreshes: 1},
		{name: "explicit zeros", change: func(f *statsFixture) { f.values = map[string]any{"videos": 0, "channels": 0, "playlists": 0} }, wantOK: true, catalogs: 1, refreshes: 1},
		{name: "large integral count", change: func(f *statsFixture) { f.values["videos"] = 1e20 }, wantOK: true, catalogs: 1, refreshes: 1},
		{name: "absent peer credential", missing: true, manual: true},
		{name: "wrong peer account", peerID: 7, manual: true},
		{name: "wrong saved account", savedID: 7, manual: true},
		{name: "first sample remains invalid", change: func(f *statsFixture) { f.values["videos"] = -1 }, catalogs: 1, refreshes: 1},
		{name: "fractional channels", change: func(f *statsFixture) { f.values["channels"] = 0.5 }, catalogs: 1, refreshes: 1},
		{name: "null playlists", change: func(f *statsFixture) { f.values["playlists"] = nil }, catalogs: 1, refreshes: 1},
		{name: "missing videos", change: func(f *statsFixture) { delete(f.values, "videos") }, catalogs: 1, refreshes: 1},
		{name: "wrong unit", change: func(f *statsFixture) { f.metrics[1].(map[string]any)["unit"] = "bytes" }, catalogs: 1, refreshes: 1},
		{name: "missing playlist catalog", change: func(f *statsFixture) { f.metrics = f.metrics[:2] }, catalogs: 1},
		{name: "no-op refresh", change: func(f *statsFixture) { f.advance = false }, catalogs: 1, refreshes: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"videos": 3, "channels": 2, "playlists": 4}
			fixture.metrics = []any{}
			for _, key := range []string{"videos", "channels", "playlists"} {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			peerID, savedID := scenario.peerID, scenario.savedID
			if peerID == 0 {
				peerID = 42
			}
			if savedID == 0 {
				savedID = 42
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
				if r.Method != "POST" || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "ta-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/api/user/account/" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				savedQueries++
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
					"ok": true, "status": 200, "integration": map[string]any{"id": "ta-owned", "kind": "tubearchivist"},
					"data": map[string]any{"id": savedID, "name": "admin"},
				}}}})
			}))
			t.Cleanup(homarrServer.Close)
			from.BaseURL, from.Config.HTTP = homarrServer.URL, homarrServer.Client()
			peerQueries := 0
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Token "+token {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				peerQueries++
				switch r.URL.Path {
				case "/api/user/account/":
					json.NewEncoder(w).Encode(map[string]any{"id": peerID, "name": "admin"})
				case "/api/ping/":
					json.NewEncoder(w).Encode(map[string]any{"response": "pong", "user": peerID, "version": "v0.6.0"})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(peer.Close)
			sum := sha256.Sum256([]byte(token))
			secrets := statsSecrets{"TUBEARCHIVIST_HOMARR_API_KEY": token, "TUBEARCHIVIST_HOMARR_TOKEN_OWNERSHIP": `{"accountId":"42","fingerprint":"` + hex.EncodeToString(sum[:]) + `"}`}
			if scenario.missing {
				secrets = statsSecrets{}
			}
			to := helpers.New(helpers.Config{
				App: "tubearchivist", BaseURL: peer.URL, HTTP: peer.Client(), Log: events.Discard(),
				Now: func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }, Secrets: secrets,
			})
			err = tubearchivistTarget().verifyData(t.Context(), from, to, "ta-owned")
			if (err == nil) != scenario.wantOK || errors.Is(err, helpers.ErrManual) != scenario.manual {
				t.Fatalf("verification: %v, want success %v manual %v", err, scenario.wantOK, scenario.manual)
			}
			if fixture.catalogs != scenario.catalogs || fixture.refreshes != scenario.refreshes || fixture.refreshes > 0 && !fixture.force {
				t.Fatalf("catalogs=%d refreshes=%d force=%v", fixture.catalogs, fixture.refreshes, fixture.force)
			}
			if scenario.manual && (fixture.catalogs != 0 || fixture.refreshes != 0) {
				t.Fatal("Manual acquired Homarr data")
			}
			if wantSaved := scenario.wantOK || scenario.catalogs > 0; wantSaved && savedQueries != 1 || !wantSaved && !scenario.manual && savedQueries != 0 {
				t.Fatalf("saved-account queries = %d", savedQueries)
			}
		})
	}
}

func TestTubearchivistTargetShape(t *testing.T) {
	target := tubearchivistTarget()
	if target.kind != "tubearchivist" || target.internal(nil) != "http://tubearchivist:8000" || len(target.statsMetrics()) != 3 {
		t.Fatal("descriptor changed")
	}
	secrets := target.secrets(nil, helpers.New(helpers.Config{Secrets: statsSecrets{"TUBEARCHIVIST_HOMARR_API_KEY": "k"}, App: "tubearchivist"}))
	if len(secrets) != 1 || secrets[0].Kind != "apiKey" || secrets[0].Value != "k" {
		t.Fatal("credential kinds changed")
	}
}
