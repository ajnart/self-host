package autobrr_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/autobrr"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type autobrrSecrets map[string]string

func (s autobrrSecrets) Get(key string) string       { return s[key] }
func (s autobrrSecrets) Set(key, value string) error { s[key] = value; return nil }

type autobrrFixture struct {
	configured    bool
	adminPosts    int
	keyPosts      int
	keys          []map[string]any
	cachedKeys    []map[string]any
	loginCode     int
	identity      string
	onboardCode   int
	keyCode       int
	keyBody       string
	configBody    string
	statsBody     string
	listBody      string
	tokenRequests int
	rejectToken   bool
	mutations     []string
	requests      []string
}

func (f *autobrrFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	request := r.Method + " " + r.URL.Path
	if r.Method != "GET" {
		f.mutations = append(f.mutations, request)
	}
	if r.Header.Get("X-API-Token") != "" {
		f.tokenRequests++
		request += " token"
	}
	f.requests = append(f.requests, request)
	switch r.URL.Path {
	case "/api/healthz/readiness":
		w.Write([]byte(`{"status":"ready"}`))
		return
	case "/api/auth/onboard":
		if f.onboardCode != 0 {
			w.WriteHeader(f.onboardCode)
			return
		}
		if f.configured {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method == "GET" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var account struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.NewDecoder(r.Body).Decode(&account) != nil || account.Username != "admin" || account.Password != "password" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.adminPosts++
		f.configured = true
		w.Write([]byte(`{"message":"user successfully created"}`))
		return
	case "/api/auth/login":
		if f.loginCode != 0 {
			w.WriteHeader(f.loginCode)
			return
		}
		if !f.configured {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var login struct {
			Username   string `json:"username"`
			Password   string `json:"password"`
			RememberMe bool   `json:"remember_me"`
		}
		if json.NewDecoder(r.Body).Decode(&login) != nil || login.Username != "admin" || login.Password != "password" || login.RememberMe {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "autobrr_user_session", Value: "private-session"})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	cookie, cookieErr := r.Cookie("autobrr_user_session")
	cookieOK := cookieErr == nil && cookie.Value == "private-session"
	keyOK := false
	for _, key := range f.keys {
		if r.Header.Get("X-API-Token") != key["key"] || f.rejectToken {
			continue
		}
		keyOK = true
		cached := false
		for _, existing := range f.cachedKeys {
			cached = cached || existing["key"] == key["key"]
		}
		if !cached {
			f.cachedKeys = append(f.cachedKeys, key)
		}
	}
	if !cookieOK && !keyOK {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/auth/validate":
		if f.identity != "" {
			w.Write([]byte(f.identity))
			return
		}
		w.Write([]byte(`{"username":"admin","auth_method":"password"}`))
	case "/api/keys":
		if r.Method == "POST" {
			f.keyPosts++
			var request struct {
				Name   string   `json:"name"`
				Scopes []string `json:"scopes"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Name != "self-host Homarr statistics" || request.Scopes == nil || len(request.Scopes) != 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			key := map[string]any{"name": "self-host Homarr statistics", "key": "issued-private-key", "scopes": []string{}, "created_at": "2026-11-02T12:00:00Z"}
			f.keys = append(f.keys, key)
			if len(f.cachedKeys) > 0 {
				f.cachedKeys = append(f.cachedKeys, key)
			}
			code := f.keyCode
			if code == 0 {
				code = http.StatusCreated
			}
			w.WriteHeader(code)
			if f.keyBody != "" {
				w.Write([]byte(f.keyBody))
				return
			}
			json.NewEncoder(w).Encode(key)
			return
		}
		if len(f.cachedKeys) > 0 {
			json.NewEncoder(w).Encode(f.cachedKeys)
			return
		}
		json.NewEncoder(w).Encode(f.keys)
	case "/api/config":
		if f.configBody != "" {
			w.Write([]byte(f.configBody))
			return
		}
		w.Write([]byte(`{"version":"v1.90.0","database":"sqlite","port":7474,"log_level":"INFO"}`))
	case "/api/release/stats":
		if f.statsBody != "" {
			w.Write([]byte(f.statsBody))
			return
		}
		w.Write([]byte(`{"push_approved_count":0,"push_rejected_count":0}`))
	case "/api/filters", "/api/release/indexers":
		if f.listBody != "" {
			w.Write([]byte(f.listBody))
			return
		}
		w.Write([]byte(`[]`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func autobrrH(t *testing.T, f *autobrrFixture, secrets helpers.Secrets) *helpers.H {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return helpers.New(helpers.Config{
		App: "autobrr", Service: "autobrr", ContainerPort: 7474, BaseURL: srv.URL, URL: srv.URL,
		HTTP: srv.Client(), Log: events.Discard(), Secrets: secrets,
		Peer: func(string) *helpers.H { return &helpers.H{} },
	})
}

func autobrrAdminSecrets() autobrrSecrets {
	return autobrrSecrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "password"}
}

func TestAutobrrSetupCreatesOnlyFirstUserAndReusesIssuedKey(t *testing.T) {
	f := &autobrrFixture{keys: []map[string]any{}}
	secrets := autobrrSecrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "password"}
	h := autobrrH(t, f, secrets)
	for range 2 {
		if err := (autobrr.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		if err := (autobrr.App{}).Check(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if f.adminPosts != 1 || f.keyPosts != 1 || len(f.keys) != 1 || secrets["AUTOBRR_HOMARR_API_KEY"] != "issued-private-key" || secrets["AUTOBRR_HOMARR_KEY_PENDING"] != "" || secrets["AUTOBRR_HOMARR_KEY_OWNERSHIP"] == "" {
		t.Fatalf("Setup did not retain the single owned account/key: admin=%d keys=%d", f.adminPosts, f.keyPosts)
	}
}

func TestAutobrrBootstrapRequiresPositiveEligibilityAndIntendedIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		fixture autobrrFixture
	}{
		{"existing rejected login", autobrrFixture{configured: true, loginCode: http.StatusForbidden}},
		{"OIDC", autobrrFixture{loginCode: http.StatusForbidden, onboardCode: http.StatusServiceUnavailable}},
		{"unreadable eligibility", autobrrFixture{loginCode: http.StatusForbidden, onboardCode: http.StatusInternalServerError}},
		{"wrong user", autobrrFixture{configured: true, identity: `{"username":"someone-else","auth_method":"password"}`}},
		{"wrong auth method", autobrrFixture{configured: true, identity: `{"username":"admin","auth_method":"oidc"}`}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := scenario.fixture
			h := autobrrH(t, &f, autobrrAdminSecrets())
			if err := (autobrr.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unsafe first account/authentication admitted: %v", err)
			}
			if f.adminPosts != 0 || f.keyPosts != 0 {
				t.Fatal("existing or uncertain account was changed")
			}
		})
	}
}

func TestAutobrrPendingAndRejectedCredentialsNeverIssueAgain(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		keyBody string
		keyCode int
	}{
		{"failed response with secret", `{"key":"issued-private-key","name":"self-host Homarr statistics","scopes":[],"created_at":"2026-11-02T12:00:00Z"}`, 500},
		{"malformed metadata after secret", `{"key":"issued-private-key","name":"self-host Homarr statistics","scopes":[],"created_at":"broken"}`, 201},
		{"truncated response after secret", `{"key":"issued-private-key","name":`, 201},
		{"missing secret", `{"name":"self-host Homarr statistics","scopes":[]}`, 201},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := &autobrrFixture{configured: true, keys: []map[string]any{}, keyBody: scenario.keyBody, keyCode: scenario.keyCode}
			s := autobrrAdminSecrets()
			for range 2 {
				h := autobrrH(t, f, s)
				if err := (autobrr.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("uncertain issuance admitted: %v", err)
				}
			}
			if f.keyPosts != 1 || s["AUTOBRR_HOMARR_KEY_PENDING"] == "" || s["AUTOBRR_HOMARR_API_KEY"] != "" {
				t.Fatal("pending issuance was replaced or lost")
			}
			if strings.Contains(scenario.keyBody, `"key":"issued-private-key"`) && !strings.Contains(s["AUTOBRR_HOMARR_KEY_PENDING"], `"secret":"issued-private-key"`) {
				t.Fatal("recoverable secret was discarded before status/metadata validation")
			}
		})
	}
	f := &autobrrFixture{configured: true, keys: []map[string]any{}}
	s := autobrrAdminSecrets()
	h := autobrrH(t, f, s)
	if err := (autobrr.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	f.rejectToken = true
	if err := (autobrr.App{}).Setup(t.Context(), autobrrH(t, f, s)); !errors.Is(err, helpers.ErrManual) || f.keyPosts != 1 {
		t.Fatalf("invalid saved credential was replaced: %v", err)
	}
	f.rejectToken = false
	f.keys = []map[string]any{}
	if err := (autobrr.App{}).Setup(t.Context(), autobrrH(t, f, s)); !errors.Is(err, helpers.ErrManual) || f.keyPosts != 1 {
		t.Fatalf("rejected saved key was replaced: %v", err)
	}
}

func TestAutobrrPreservesKeyCollisionAndStopsBeforeTraceExposure(t *testing.T) {
	for _, trace := range []bool{false, true} {
		f := &autobrrFixture{configured: true, keys: []map[string]any{{"key": "user-private-key", "name": "self-host Homarr statistics", "scopes": []string{}, "created_at": "2026-10-01T00:00:00Z"}}}
		if trace {
			f.configBody = `{"version":"v1.90.0","database":"sqlite","port":7474,"log_level":"TRACE"}`
		}
		s := autobrrAdminSecrets()
		if err := (autobrr.App{}).Setup(t.Context(), autobrrH(t, f, s)); !errors.Is(err, helpers.ErrManual) || f.keyPosts != 0 || len(f.keys) != 1 || f.tokenRequests != 0 {
			t.Fatalf("collision/logging prerequisite changed credentials or exposed a token: %v", err)
		}
		for _, v := range s {
			if strings.Contains(v, "user-private-key") {
				t.Fatal("an unrelated key inventory was persisted")
			}
		}
	}
}

func TestAutobrrStatisticsRequireExplicitCountsAndArrays(t *testing.T) {
	for _, scenario := range []struct{ stats, list string }{
		{`{}`, ""}, {`{"push_approved_count":null,"push_rejected_count":0}`, ""},
		{`{"push_approved_count":-1,"push_rejected_count":0}`, ""},
		{`{"push_approved_count":0.5,"push_rejected_count":0}`, ""},
		{"", "null"}, {"", `{"error":"not an array"}`}, {"", `[null]`}, {"", `[{}]`},
	} {
		f := &autobrrFixture{configured: true, keys: []map[string]any{}}
		s := autobrrAdminSecrets()
		h := autobrrH(t, f, s)
		if err := (autobrr.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		f.statsBody, f.listBody = scenario.stats, scenario.list
		if err := autobrr.VerifyHomarr(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("missing/bad Stats admitted: %v", err)
		}
		if f.keyPosts != 1 {
			t.Fatal("bad data caused key rotation")
		}
	}
}

func TestAutobrrComposeAndPeerFirstSetupPreserveProductChoices(t *testing.T) {
	a := autobrr.App{}
	compose := a.Compose(app.RenderContext{Port: 17474})
	for _, want := range []string{"ghcr.io/autobrr/autobrr:latest", `${BIND_IP}:17474:7474`, `user: "${PUID}:${PGID}"`, `./config:/config`, `AUTOBRR__HOST: 0.0.0.0`, `/api/healthz/readiness`} {
		if !strings.Contains(compose, want) {
			t.Fatalf("missing official Compose contract %q", want)
		}
	}
	if a.Info().UsesMedia || a.Info().ContainerPort != 7474 || len(a.Links()) != 0 || strings.Contains(compose, "postgres") || strings.Contains(compose, "DOWNLOADS_ROOT") {
		t.Fatal("unexpected backend/media/downloader wiring")
	}
	f := &autobrrFixture{keys: []map[string]any{}}
	s := autobrrAdminSecrets()
	h := autobrrH(t, f, s)
	h.Peer = func(string) *helpers.H { return nil }
	if err := a.Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if f.keyPosts != 0 {
		t.Fatal("peer-first install issued an unused key")
	}
	h.Peer = func(string) *helpers.H { return &helpers.H{} }
	if err := a.Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if f.keyPosts != 1 || f.adminPosts != 1 {
		t.Fatal("Homarr arrival did not reuse the existing administrator")
	}
	for _, operation := range f.mutations {
		if operation != "POST /api/auth/login" && operation != "POST /api/auth/onboard" && operation != "POST /api/keys" {
			t.Fatalf("Setup changed a person-owned resource: %s", operation)
		}
	}
}

func TestAutobrrPreflightHoldsBeforeUnpreparedNonRootConfigStart(t *testing.T) {
	preflight, ok := any(autobrr.App{}).(app.PreflightChecker)
	if !ok {
		t.Fatal("Autobrr has no pre-start configuration-directory prerequisite")
	}
	f := &autobrrFixture{keys: []map[string]any{}}
	s := autobrrAdminSecrets()
	h := autobrrH(t, f, s)
	err := preflight.Preflight(t.Context(), h)
	var manual *helpers.ManualError
	if !errors.As(err, &manual) || !errors.Is(err, helpers.ErrManual) || !strings.Contains(manual.Hint, "./config") || !strings.Contains(manual.Hint, "before start") || !strings.Contains(manual.Hint, "directory preparation") {
		t.Fatalf("unsafe non-root start was not held with an actionable prerequisite: %v", err)
	}
	if len(f.mutations) != 0 || f.tokenRequests != 0 || len(s) != 2 {
		t.Fatal("the pre-start hold changed accounts, keys or private records")
	}
}

func TestAutobrrNonemptyCachedInventoryNeverAuthorizesIssuance(t *testing.T) {
	for _, hiddenCollision := range []bool{false, true} {
		unrelated := map[string]any{"key": "unrelated-private-key", "name": "person-owned", "scopes": []string{}, "created_at": "2026-10-01T00:00:00Z"}
		f := &autobrrFixture{configured: true, keys: []map[string]any{unrelated}, cachedKeys: []map[string]any{unrelated}}
		if hiddenCollision {
			f.keys = append(f.keys, map[string]any{"key": "hidden-private-key", "name": "self-host Homarr statistics", "scopes": []string{}, "created_at": "2026-10-02T00:00:00Z"})
		}
		before, err := json.Marshal(f.keys)
		if err != nil {
			t.Fatal(err)
		}
		s := &autobrrFaultSecrets{autobrrSecrets: autobrrAdminSecrets()}
		for range 2 {
			err := (autobrr.App{}).Setup(t.Context(), autobrrH(t, f, s))
			var manual *helpers.ManualError
			if !errors.As(err, &manual) || !errors.Is(err, helpers.ErrManual) || !strings.Contains(manual.Hint, "cache") {
				t.Fatalf("partial inventory authorized issuance instead of actionable uncertainty: %v", err)
			}
		}
		after, err := json.Marshal(f.keys)
		if err != nil {
			t.Fatal(err)
		}
		if f.keyPosts != 0 || f.tokenRequests != 0 || len(s.writes) != 0 || string(after) != string(before) || len(f.cachedKeys) != 1 {
			t.Fatal("uncertain cached discovery changed keys, the cache or private records")
		}
	}
}

func TestAutobrrVerifiesSavedKeyBeforeReadingPartialCache(t *testing.T) {
	f := &autobrrFixture{configured: true, keys: []map[string]any{}}
	s := autobrrAdminSecrets()
	if err := (autobrr.App{}).Setup(t.Context(), autobrrH(t, f, s)); err != nil {
		t.Fatal(err)
	}
	unrelated := map[string]any{"key": "unrelated-private-key", "name": "person-owned", "scopes": []string{}, "created_at": "2026-10-01T00:00:00Z"}
	f.keys = append(f.keys, unrelated)
	f.cachedKeys = []map[string]any{unrelated}
	f.requests = nil
	f.tokenRequests = 0
	h := autobrrH(t, f, s)
	if err := autobrr.VerifyHomarr(t.Context(), h); err != nil {
		t.Fatalf("valid saved key was rejected before populating its cached identity: %v", err)
	}
	verified := false
	for _, request := range f.requests {
		if request == "GET /api/config token" {
			verified = true
		}
		if request == "GET /api/keys" && !verified {
			t.Fatal("cached inventory was read before the saved credential was authenticated")
		}
	}
	if !verified || len(f.cachedKeys) != 2 || f.keyPosts != 1 || h.Log.Redact("unrelated-private-key") != "[redacted]" {
		t.Fatal("saved-key verification lost identity, privacy or no-reissuance guarantees")
	}
}
