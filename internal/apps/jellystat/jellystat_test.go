package jellystat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type jellystatSecrets map[string]string

func (s jellystatSecrets) Get(key string) string { return s[key] }
func (s jellystatSecrets) Set(string, string) error {
	return errors.New("read-only Jellystat verification must not write credentials")
}

type jellystatFixture struct {
	mu          sync.Mutex
	state       any
	config      map[string]any
	keyConfig   map[string]any
	admins      any
	token       any
	loginStatus int
	username    string
	keyStatus   int
	keys        []keyRecord
	requests    []string
}

func newJellystatFixture() *jellystatFixture {
	return &jellystatFixture{
		state: map[string]any{"state": 2, "version": "1.1.12"},
		config: map[string]any{
			"JF_HOST": "http://selected.invalid:8096", "APP_USER": "admin",
			"REQUIRE_LOGIN": true, "IS_JELLYFIN": true,
			"settings": map[string]any{"ExcludedLibraries": []string{"existing-library"}, "preferred_admin": "kept"},
		},
		admins: []any{map[string]any{"Id": "selected-admin", "Policy": map[string]bool{"IsAdministrator": true}}},
		token:  "synthetic-jwt", loginStatus: http.StatusOK, keyStatus: http.StatusOK, username: "admin",
	}
}

func (f *jellystatFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	w.Header().Set("Content-Type", "application/json")
	var value any
	switch r.Method + " " + r.URL.Path {
	case "GET /auth/isConfigured":
		value = f.state
	case "POST /auth/login":
		var body struct{ Username, Password string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Username != f.username || body.Password != passwordHash("synthetic-password") {
			t.Error("login did not use the existing Admin login and CryptoJS-compatible hash")
		}
		w.WriteHeader(f.loginStatus)
		value = map[string]any{"token": f.token}
	case "GET /api/getconfig":
		if r.Header.Get("X-API-Token") == "synthetic-key" {
			w.WriteHeader(f.keyStatus)
			value = f.config
			if f.keyConfig != nil {
				value = f.keyConfig
			}
		} else if r.Header.Get("Authorization") == "Bearer synthetic-jwt" {
			value = f.config
		} else {
			t.Error("configuration read lacked the intended credential")
			w.WriteHeader(http.StatusUnauthorized)
		}
	case "GET /proxy/getAdminUsers":
		if r.Header.Get("Authorization") != "Bearer synthetic-jwt" {
			t.Error("backend verification did not use the verified Jellystat session")
		}
		value = f.admins
	case "GET /api/keys":
		if r.Header.Get("X-API-Token") != "synthetic-key" {
			t.Error("inventory verification did not use the saved key")
		}
		value = f.keys
	default:
		t.Error("verification attempted an unexpected route or mutation")
		w.WriteHeader(http.StatusNotFound)
	}
	json.NewEncoder(w).Encode(value)
}

func (f *jellystatFixture) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.requests...)
}

func jellystatHarness(t *testing.T, f *jellystatFixture) (*helpers.H, *[]helpers.StepResult) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(t, w, r) }))
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	steps := []helpers.StepResult{}
	h := helpers.New(helpers.Config{
		App: "jellystat", Service: "jellystat", ContainerPort: 3000,
		BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(), Log: events.Discard(),
		Inputs: map[string]string{"media_server": "jellyfin"},
		Secrets: jellystatSecrets{
			"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password",
			"JELLYSTAT_SERVER_ADDRESS": "http://selected.invalid:8096", "JELLYSTAT_SERVER_ADMIN_ID": "selected-admin",
		},
		Now: deps.Now, Sleep: deps.Sleep,
		Record: func(step helpers.StepResult) { steps = append(steps, step) },
	})
	return h, &steps
}

func TestJellystatCryptoJSKeccak512Vector(t *testing.T) {
	want := "0eab42de4c3ceb9235fc91acffe746b29c29a8c366b7c60e4e67c466f36a4304c00fa9caf9d87976ba469bcbe06713b435f091ef2769fb160cdab33d3670680e"
	if passwordHash("") != want {
		t.Fatal("password hashing differs from the upstream CryptoJS Keccak-512 vector")
	}
}

func TestJellystatExistingIdentityAndBackendAreVerifiedWithoutMutation(t *testing.T) {
	for _, kind := range []string{"jellyfin", "emby"} {
		t.Run(kind, func(t *testing.T) {
			f := newJellystatFixture()
			f.config["IS_JELLYFIN"] = kind == "jellyfin"
			f.config["JF_HOST"] = "http://selected.invalid:8096/"
			h, _ := jellystatHarness(t, f)
			h.Inputs["media_server"] = kind
			sec := h.Secrets.(jellystatSecrets)
			sec["JELLYSTAT_HOMARR_API_KEY"] = "synthetic-key"
			owned, err := json.Marshal(keyOwnership{Identity: currentKeyIdentity(h), Name: homarrKeyName, Fingerprint: helpers.Fingerprint("synthetic-key")})
			if err != nil {
				t.Fatal(err)
			}
			sec["JELLYSTAT_HOMARR_OWNERSHIP"] = string(owned)
			f.keys = []keyRecord{{Name: homarrKeyName, Key: "synthetic-key"}}
			beforeSecrets := maps.Clone(sec)
			beforeConfig, _ := json.Marshal(f.config)
			for range 2 {
				if err := (App{}).Setup(t.Context(), h); err != nil {
					t.Fatalf("Setup: %v", err)
				}
				if err := (App{}).Check(t.Context(), h); err != nil {
					t.Fatalf("Check: %v", err)
				}
			}
			afterConfig, _ := json.Marshal(f.config)
			if !reflect.DeepEqual(beforeSecrets, sec) || string(beforeConfig) != string(afterConfig) {
				t.Fatal("verification changed saved credentials or configuration")
			}
			calls := strings.Join(f.calls(), "\n")
			if strings.Contains(calls, "POST /api/keys") || strings.Contains(calls, "/sync") || strings.Contains(calls, "/stats") {
				t.Fatal("App-only verification issued credentials, imported history or acquired provisional Stats")
			}
		})
	}
}

func TestJellystatHoldsAmbiguousAccountsAndWrongBackendWithoutWrites(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		alter func(*jellystatFixture, *helpers.H)
	}{
		{"unconfigured", func(f *jellystatFixture, _ *helpers.H) { f.state = map[string]any{"state": 0, "version": "1.1.12"} }},
		{"existing account state one", func(f *jellystatFixture, _ *helpers.H) { f.state = map[string]any{"state": 1, "version": "1.1.12"} }},
		{"rejected existing account state one", func(f *jellystatFixture, _ *helpers.H) {
			f.state = map[string]any{"state": 1, "version": "1.1.12"}
			f.loginStatus = http.StatusUnauthorized
		}},
		{"missing state", func(f *jellystatFixture, _ *helpers.H) { f.state = map[string]any{"version": "1.1.12"} }},
		{"null state", func(f *jellystatFixture, _ *helpers.H) { f.state = map[string]any{"state": nil, "version": "1.1.12"} }},
		{"unknown state", func(f *jellystatFixture, _ *helpers.H) { f.state = map[string]any{"state": 3, "version": "1.1.12"} }},
		{"unknown version", func(f *jellystatFixture, _ *helpers.H) { f.state = map[string]any{"state": 2, "version": "unknown"} }},
		{"wrong account", func(f *jellystatFixture, _ *helpers.H) { f.config["APP_USER"] = "different" }},
		{"login disabled", func(f *jellystatFixture, _ *helpers.H) { f.config["REQUIRE_LOGIN"] = false }},
		{"missing login flag", func(f *jellystatFixture, _ *helpers.H) { delete(f.config, "REQUIRE_LOGIN") }},
		{"missing token", func(f *jellystatFixture, _ *helpers.H) { f.token = "" }},
		{"malformed token", func(f *jellystatFixture, _ *helpers.H) { f.token = []string{"synthetic-jwt"} }},
		{"rejected login", func(f *jellystatFixture, _ *helpers.H) { f.loginStatus = http.StatusUnauthorized }},
		{"missing Admin login", func(_ *jellystatFixture, h *helpers.H) { delete(h.Secrets.(jellystatSecrets), "ADMIN_PASSWORD") }},
		{"missing selection", func(_ *jellystatFixture, h *helpers.H) {
			delete(h.Secrets.(jellystatSecrets), "JELLYSTAT_SERVER_ADDRESS")
		}},
		{"missing backend identity", func(_ *jellystatFixture, h *helpers.H) {
			delete(h.Secrets.(jellystatSecrets), "JELLYSTAT_SERVER_ADMIN_ID")
		}},
		{"wrong backend", func(f *jellystatFixture, _ *helpers.H) { f.config["JF_HOST"] = "http://other.invalid:8096" }},
		{"credential bearing address", func(_ *jellystatFixture, h *helpers.H) {
			h.Secrets.(jellystatSecrets)["JELLYSTAT_SERVER_ADDRESS"] = "http://user:secret@selected.invalid:8096"
		}},
		{"query bearing address", func(_ *jellystatFixture, h *helpers.H) {
			h.Secrets.(jellystatSecrets)["JELLYSTAT_SERVER_ADDRESS"] += "?apiKey=secret"
		}},
		{"wrong backend kind", func(f *jellystatFixture, _ *helpers.H) { f.config["IS_JELLYFIN"] = false }},
		{"missing backend kind", func(f *jellystatFixture, _ *helpers.H) { delete(f.config, "IS_JELLYFIN") }},
		{"unknown selection", func(_ *jellystatFixture, h *helpers.H) { h.Inputs["media_server"] = "other" }},
		{"body error", func(f *jellystatFixture, _ *helpers.H) { f.config["error"] = "private backend failure" }},
		{"offline backend empty admins", func(f *jellystatFixture, _ *helpers.H) { f.admins = []any{} }},
		{"offline backend null admins", func(f *jellystatFixture, _ *helpers.H) { f.admins = nil }},
		{"wrong backend admin", func(f *jellystatFixture, _ *helpers.H) {
			f.admins = []any{map[string]any{"Id": "other-admin", "Policy": map[string]bool{"IsAdministrator": true}}}
		}},
		{"backend body error", func(f *jellystatFixture, _ *helpers.H) {
			f.admins = map[string]string{"error": "private backend failure"}
		}},
		{"backend not administrator", func(f *jellystatFixture, _ *helpers.H) {
			f.admins = []any{map[string]any{"Id": "selected-admin", "Policy": map[string]bool{"IsAdministrator": false}}}
		}},
		{"backend missing policy", func(f *jellystatFixture, _ *helpers.H) { f.admins = []any{map[string]string{"Id": "selected-admin"}} }},
		{"rejected saved key", func(f *jellystatFixture, h *helpers.H) {
			h.Secrets.(jellystatSecrets)["JELLYSTAT_HOMARR_API_KEY"] = "synthetic-key"
			f.keyStatus = http.StatusForbidden
		}},
		{"saved key wrong identity", func(f *jellystatFixture, h *helpers.H) {
			h.Secrets.(jellystatSecrets)["JELLYSTAT_HOMARR_API_KEY"] = "synthetic-key"
			f.keyConfig = maps.Clone(f.config)
			f.keyConfig["JF_HOST"] = "http://other.invalid:8096"
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newJellystatFixture()
			h, steps := jellystatHarness(t, f)
			scenario.alter(f, h)
			err := (App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) {
				t.Fatalf("expected actionable Manual hold, got %v", err)
			}
			last := (*steps)[len(*steps)-1]
			if last.Status != status.StepManual || !strings.Contains(last.Hint, "self-host setup jellystat") {
				t.Fatal("missing recorded Manual recovery")
			}
			if strings.Contains(fmt.Sprint(err, steps), "private backend failure") {
				t.Fatal("upstream body leaked into diagnostics")
			}
			if err := (App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("Check did not preserve Manual hold: %v", err)
			}
		})
	}
}

func TestJellystatComposeKeepsDatabasePrivateAndPersistent(t *testing.T) {
	a := App{}
	info := a.Info()
	if info.ID != "jellystat" || info.Service != "jellystat" || info.Image != "cyfershepard/jellystat:latest" || info.ContainerPortFor(7123) != 3000 || info.UsesMedia {
		t.Fatal("incorrect App or container-port contract")
	}
	for _, kind := range []string{"jellyfin", "emby"} {
		definition := a.Compose(app.RenderContext{Port: 7123, Inputs: map[string]string{"media_server": kind}})
		for _, want := range []string{
			"image: cyfershepard/jellystat:latest", "image: postgres:18", "${BIND_IP}:7123:3000",
			"POSTGRES_IP: jellystat-db", "POSTGRES_PORT: 5432", "${JELLYSTAT_DB_PASSWORD}", "${JELLYSTAT_JWT_SECRET}",
			"./database:/var/lib/postgresql", "./backup-data:/app/backend/backup-data", "condition: service_healthy", "pg_isready",
			fmt.Sprintf("IS_EMBY_API: %q", fmt.Sprint(kind == "emby")),
		} {
			if !strings.Contains(definition, want) {
				t.Errorf("Compose missing %q", want)
			}
		}
		for _, forbidden := range []string{"container_name:", ":5432\n", "JS_USER:", "JS_PASSWORD:", "JF_API_KEY:", "REJECT_SELF_SIGNED_CERTIFICATES:", "PUID:", "PGID:", "docker.sock", "version:"} {
			if strings.Contains(definition, forbidden) {
				t.Errorf("Compose contains unsafe or unsupported %q", forbidden)
			}
		}
		if strings.Count(definition, "restart: unless-stopped") != 2 || strings.Count(definition, "ports:") != 1 {
			t.Fatal("restart policies or database port isolation are missing")
		}
	}
	if len(a.Secrets()) != 2 || len(a.Links()) != 0 {
		t.Fatal("unexpected credential generation or automatic media Links")
	}
	for _, input := range a.Inputs() {
		if input.Key != "media_server" && !input.Private {
			t.Fatalf("identity or credentials declared public: %s", input.Key)
		}
	}
}

type jellystatTransport func(*http.Request) (*http.Response, error)

func (f jellystatTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestJellystatVerificationDoesNotExposeResponseOrTransportSecrets(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{name: "rejected response", status: http.StatusForbidden, body: "unregistered-private-response"},
		{name: "malformed identity", status: http.StatusOK, body: `{"value":"unregistered-private-response"`},
		{name: "transport and TLS error", err: errors.New("unregistered-private-response")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := helpers.New(helpers.Config{
				App: "jellystat", BaseURL: "http://jellystat.invalid", Log: events.Discard(), Secrets: jellystatSecrets{},
				HTTP: &http.Client{Transport: jellystatTransport(func(*http.Request) (*http.Response, error) {
					if scenario.err != nil {
						return nil, scenario.err
					}
					return &http.Response{StatusCode: scenario.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(scenario.body))}, nil
				})},
			})
			var value map[string]any
			err := request(t.Context(), h, helpers.Request{Path: "/api/getconfig"}, &value)
			if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "unregistered-private-response") {
				t.Fatal("verification lost its Manual boundary or exposed private upstream diagnostics")
			}
		})
	}
}

func TestJellystatVerificationNeverForwardsCredentialsOnRedirect(t *testing.T) {
	forwarded := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/private", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	h := helpers.New(helpers.Config{App: "jellystat", BaseURL: origin.URL, HTTP: origin.Client(), Log: events.Discard(), Secrets: jellystatSecrets{}})
	var value map[string]any
	err := request(t.Context(), h, helpers.Request{Path: "/api/getconfig", Header: map[string]string{"X-API-Token": "synthetic-key"}}, &value)
	if !errors.Is(err, helpers.ErrManual) || forwarded {
		t.Fatal("redirect was followed or accepted as authenticated identity")
	}
}
