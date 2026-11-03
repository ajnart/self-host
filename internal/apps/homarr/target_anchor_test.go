package homarr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

const anchorFixtureToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func anchorPeer(t *testing.T, held string) *helpers.H {
	t.Helper()
	sum := sha256.Sum256([]byte(anchorFixtureToken))
	ownership := `{"accountId":"admin-id","email":"admin@selfhost.local","fingerprint":"` + hex.EncodeToString(sum[:]) + `"}`
	user := `{"id":"admin-id","email":"admin@selfhost.local","isAdmin":true,"status":"active"}`
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.RequestURI() {
		case "GET /api/health":
			w.Write([]byte(`{"app":"anchor","status":"ok","version":"0.17.2"}`))
		case "POST /api/auth/login":
			w.Write([]byte(`{"access_token":"jwt","refresh_token":"refresh","user":` + user + `}`))
		case "GET /api/auth/me":
			w.Write([]byte(user))
		case "GET /api/auth/api-token":
			w.Write([]byte(`{"apiToken":"` + held + `"}`))
		case "GET /api/notes?limit=1":
			if r.Header.Get("Authorization") != "Bearer "+anchorFixtureToken && r.Header.Get("Authorization") != "Bearer jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)
	return helpers.New(helpers.Config{
		App: "anchor", BaseURL: peer.URL, URL: peer.URL, HTTP: peer.Client(), Log: events.Discard(),
		Secrets: statsSecrets{
			"ADMIN_USER": "admin", "ADMIN_PASSWORD": "pw", "ANCHOR_ADMIN_IDENTITY": `{"id":"admin-id","email":"admin@selfhost.local"}`,
			"ANCHOR_HOMARR_API_KEY": anchorFixtureToken, "ANCHOR_HOMARR_TOKEN_OWNERSHIP": ownership,
		},
	})
}

func TestAnchorDescriptor(t *testing.T) {
	anchor := anchorTarget()
	if anchor.kind != "anchor" || len(anchor.statsMetrics()) != 1 || anchor.statsMetrics()[0] != "notes" {
		t.Fatalf("unexpected descriptor: %+v", anchor)
	}
	secrets := anchor.secrets(nil, anchorPeer(t, anchorFixtureToken))
	if len(secrets) != 1 || secrets[0].Kind != "apiKey" || secrets[0].Value != anchorFixtureToken {
		t.Fatal("Anchor must offer only the apiKey secret")
	}
}

func TestAnchorStatsVerification(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*statsFixture)
		wantOK bool
	}{
		{name: "populated", wantOK: true},
		{name: "explicit zero", change: func(f *statsFixture) { f.values["notes"] = 0 }, wantOK: true},
		{name: "negative", change: func(f *statsFixture) { f.values["notes"] = -1 }},
		{name: "fractional", change: func(f *statsFixture) { f.values["notes"] = 0.5 }},
		{name: "text", change: func(f *statsFixture) { f.values["notes"] = "0" }},
		{name: "null", change: func(f *statsFixture) { f.values["notes"] = nil }},
		{name: "missing", change: func(f *statsFixture) { delete(f.values, "notes") }},
		{name: "wrong unit", change: func(f *statsFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "missing catalog", change: func(f *statsFixture) { f.metrics = []any{} }},
		{name: "stale no-op refresh", change: func(f *statsFixture) { f.advance = false }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"notes": 3}
			fixture.metrics = []any{map[string]any{"key": "notes", "unit": "count"}}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			err := anchorTarget().verifyData(t.Context(), fixture.helpers(t), anchorPeer(t, anchorFixtureToken), "anchor-owned")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("verification: %v, want success %v", err, scenario.wantOK)
			}
		})
	}
}

func TestAnchorVerificationRequiresTheRecordedAccountToken(t *testing.T) {
	fixture := newStatsFixture()
	fixture.values = map[string]any{"notes": 3}
	fixture.metrics = []any{map[string]any{"key": "notes", "unit": "count"}}
	other := strings.Repeat("b", 64)
	if err := anchorTarget().verifyData(t.Context(), fixture.helpers(t), anchorPeer(t, other), "anchor-owned"); err == nil {
		t.Fatal("Equal counts must not stand in for a token the selected account no longer holds")
	}
	if fixture.catalogs != 0 {
		t.Fatal("Stats were read despite an unverified account token")
	}
}

func TestAnchorPreflightExplainsManualRecoveryBeforeMutation(t *testing.T) {
	from := helpers.New(helpers.Config{App: "homarr", Log: events.Discard()})
	if err := anchorTarget().preflight(t.Context(), from, anchorPeer(t, strings.Repeat("b", 64))); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("preflight must stop as Manual for an account that no longer holds the recorded token: %v", err)
	}
	if err := anchorTarget().preflight(t.Context(), from, anchorPeer(t, anchorFixtureToken)); err != nil {
		t.Fatal(err)
	}
}
