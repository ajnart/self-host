package changedetection_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/changedetection"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestDefinitionUsesOfficialImageAndPreservesTheCompleteDatastore(t *testing.T) {
	a := changedetection.App{}
	info := a.Info()
	if info.ID != "changedetection" || info.Service != "changedetection" || info.Port != 5050 || info.ContainerPort != 5000 {
		t.Fatalf("unexpected connection metadata: %+v", info)
	}
	if info.Image != "ghcr.io/dgtlmoon/changedetection.io:latest" || len(info.Description) > 100 {
		t.Fatalf("unexpected public metadata: %+v", info)
	}
	compose := a.Compose(app.RenderContext{Port: 15050, Inputs: map[string]string{"api_key": "synthetic-private-key"}})
	for _, expected := range []string{info.Image, "restart: unless-stopped", "./data:/datastore", "${BIND_IP}:15050:5000", "TZ: ${TZ}", `DISABLE_VERSION_CHECK: "true"`} {
		if !strings.Contains(compose, expected) {
			t.Errorf("Compose lacks %q", expected)
		}
	}
	for _, forbidden := range []string{"synthetic-private-key", "ADMIN_PASSWORD", "SALTED_PASS", "PUID", "PGID", "privileged:", "cap_add:", "docker.sock", "PLAYWRIGHT", "ALLOW_IANA_RESTRICTED_ADDRESSES", "recheck", "init:"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("Compose contains %q", forbidden)
		}
	}
	inputs := a.Inputs()
	if len(inputs) != 1 || inputs[0].Key != "api_key" || !inputs[0].Private || !inputs[0].RecoveryOnly {
		t.Fatalf("API key must use the existing private recovery input boundary: %+v", inputs)
	}
	if len(a.Links()) != 0 || !strings.Contains(a.StandaloneGuide(), "Do not start") {
		t.Fatal("App must not add outgoing Links or advertise an unheld fresh start")
	}
}

func TestInstallStopsBeforeDockerWithoutInferringFreshAuthority(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			data := filepath.Join(clitest.Root(t, deps), "apps", "changedetection", "data")
			if existing {
				if err := os.MkdirAll(data, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(data, "untouched"), []byte("synthetic-private-state"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result := clitest.Run(t, deps, catalog.New([]app.App{changedetection.App{}}, nil), "install", "changedetection", "--yes", "--json")
			if result.Code == 0 || !strings.Contains(result.Out, "demo watches") {
				t.Fatalf("installation must retain the actionable startup hold: %s", result.Out)
			}
			for _, call := range deps.Runner.(*systemtest.Runner).Calls() {
				if strings.HasPrefix(call, "docker compose") || strings.HasPrefix(call, "docker run") {
					t.Fatal("held install started containers: " + call)
				}
			}
			if existing && clitest.Read(t, filepath.Join(data, "untouched")) != "synthetic-private-state" {
				t.Fatal("runtime hold changed pre-existing App data")
			}
		})
	}
	err := (changedetection.App{}).Preflight(t.Context(), nil)
	if !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("preflight must be an explicit Manual hold: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (changedetection.App{}).Preflight(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflight must preserve cancellation: %v", err)
	}
}

type savedSecrets struct {
	values map[string]string
	writes int
}

func (s *savedSecrets) Get(key string) string { return s.values[key] }

func (s *savedSecrets) Set(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

type fakeChangedetection struct {
	info            string
	watches         string
	keyRejected     bool
	apiUnprotected  bool
	uiUnprotected   bool
	passwordChanged bool
	settingsInvalid bool
	loginRedirect   string
	apiRedirect     string
	mu              sync.Mutex
	requests        []string
}

func (f *fakeChangedetection) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	if r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/systeminfo", "GET /api/v1/watch":
		if f.apiRedirect != "" {
			http.Redirect(w, r, f.apiRedirect, http.StatusFound)
			return
		}
		if !f.apiUnprotected && (f.keyRejected || r.Header.Get("x-api-key") != "synthetic-existing-key") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "synthetic-private-error-body")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/systeminfo" {
			fmt.Fprint(w, f.info)
		} else {
			fmt.Fprint(w, f.watches)
		}
	case "GET /settings":
		cookie, _ := r.Cookie("session")
		if f.uiUnprotected || cookie != nil && cookie.Value == "synthetic-authenticated-session" {
			if f.settingsInvalid {
				fmt.Fprint(w, "synthetic-private-error-body")
			} else {
				fmt.Fprint(w, `<span id="api-key">synthetic-existing-key</span>synthetic-private-settings`)
			}
			return
		}
		location := "/login"
		if f.loginRedirect != "" {
			location = f.loginRedirect
		}
		http.Redirect(w, r, location, http.StatusFound)
	case "GET /login":
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-csrf-session"})
		fmt.Fprint(w, `<input type="hidden" name="csrf_token" value="synthetic-csrf-token">`)
	case "POST /login":
		cookie, _ := r.Cookie("session")
		if r.ParseForm() != nil || r.Form.Get("csrf_token") != "synthetic-csrf-token" || cookie == nil || cookie.Value != "synthetic-csrf-session" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !f.passwordChanged && r.Form.Get("password") == "synthetic-admin-password" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-authenticated-session"})
		}
		http.Redirect(w, r, "/", http.StatusFound)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newVerifiedApp(t *testing.T, fake *fakeChangedetection) (*helpers.H, *savedSecrets, *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	store := &savedSecrets{values: map[string]string{
		"UNRELATED_API_KEY":       "synthetic-unrelated-secret",
		"CHANGEDETECTION_API_KEY": "synthetic-existing-key",
		"ADMIN_PASSWORD":          "synthetic-admin-password",
	}}
	var output bytes.Buffer
	log := events.New(events.JSON(&output))
	h := helpers.New(helpers.Config{App: "changedetection", Service: "changedetection", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Secrets: store, Log: log})
	return h, store, &output
}

func validPeer() *fakeChangedetection {
	return &fakeChangedetection{info: `{"version":"0.60.10","uptime":0,"watch_count":0}`, watches: `{}`}
}

func TestSetupAndCheckReuseExplicitCredentialsWithoutChangingData(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			fake := validPeer()
			if populated {
				fake.info = `{"version":"0.60.10","uptime":123.5,"watch_count":3}`
				fake.watches = `{"one":{"last_changed":20,"viewed":false},"two":{"last_changed":30,"viewed":true},"three":{"last_changed":0,"viewed":false}}`
			}
			h, store, output := newVerifiedApp(t, fake)
			before := make(map[string]string)
			for key, value := range store.values {
				before[key] = value
			}
			for range 2 {
				if err := (changedetection.App{}).Setup(t.Context(), h); err != nil {
					t.Fatalf("existing protected App failed verification: %v", err)
				}
			}
			if err := (changedetection.App{}).Check(t.Context(), h); err != nil {
				t.Fatalf("existing protected App failed Check: %v", err)
			}
			if store.writes != 0 || !reflect.DeepEqual(before, store.values) {
				t.Fatal("verification replaced a credential or unrelated secret")
			}
			for _, request := range fake.requests {
				if request != "GET /api/v1/systeminfo" && request != "GET /api/v1/watch" && request != "GET /settings" && request != "GET /login" && request != "POST /login" {
					t.Fatalf("verification performed an unexpected action: %s", request)
				}
			}
			if strings.Contains(output.String(), "synthetic-") {
				t.Fatal("verification emitted private response or credential data")
			}
		})
	}
}

func TestVerificationRejectsIncompleteDataAndUnsafeAuthentication(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*fakeChangedetection, *savedSecrets)
		manual bool
	}{
		{"missing key", func(_ *fakeChangedetection, s *savedSecrets) { delete(s.values, "CHANGEDETECTION_API_KEY") }, true},
		{"missing password", func(_ *fakeChangedetection, s *savedSecrets) { delete(s.values, "ADMIN_PASSWORD") }, true},
		{"rejected key", func(f *fakeChangedetection, _ *savedSecrets) { f.keyRejected = true }, true},
		{"API protection disabled", func(f *fakeChangedetection, _ *savedSecrets) { f.apiUnprotected = true }, true},
		{"web protection disabled", func(f *fakeChangedetection, _ *savedSecrets) { f.uiUnprotected = true }, true},
		{"existing password changed", func(f *fakeChangedetection, _ *savedSecrets) { f.passwordChanged = true }, true},
		{"wrong settings page", func(f *fakeChangedetection, _ *savedSecrets) { f.settingsInvalid = true }, true},
		{"wrong login redirect", func(f *fakeChangedetection, _ *savedSecrets) { f.loginRedirect = "/unrelated" }, true},
		{"redirected API", func(f *fakeChangedetection, _ *savedSecrets) { f.apiRedirect = "/settings" }, false},
		{"null system info", func(f *fakeChangedetection, _ *savedSecrets) { f.info = `null` }, false},
		{"missing count", func(f *fakeChangedetection, _ *savedSecrets) { f.info = `{"version":"0.60.10","uptime":0}` }, false},
		{"null uptime", func(f *fakeChangedetection, _ *savedSecrets) {
			f.info = `{"version":"0.60.10","uptime":null,"watch_count":0}`
		}, false},
		{"negative uptime", func(f *fakeChangedetection, _ *savedSecrets) {
			f.info = `{"version":"0.60.10","uptime":-1,"watch_count":0}`
		}, false},
		{"fractional count", func(f *fakeChangedetection, _ *savedSecrets) {
			f.info = `{"version":"0.60.10","uptime":0,"watch_count":0.5}`
		}, false},
		{"negative count", func(f *fakeChangedetection, _ *savedSecrets) {
			f.info = `{"version":"0.60.10","uptime":0,"watch_count":-1}`
		}, false},
		{"old version", func(f *fakeChangedetection, _ *savedSecrets) {
			f.info = `{"version":"0.59.0","uptime":0,"watch_count":0}`
		}, false},
		{"null watches", func(f *fakeChangedetection, _ *savedSecrets) { f.watches = `null` }, false},
		{"array watches", func(f *fakeChangedetection, _ *savedSecrets) { f.watches = `[]` }, false},
		{"null watch", func(f *fakeChangedetection, _ *savedSecrets) { f.watches = `{"one":null}` }, false},
		{"null changed", func(f *fakeChangedetection, _ *savedSecrets) {
			f.watches = `{"one":{"last_changed":null,"viewed":false}}`
		}, false},
		{"missing viewed", func(f *fakeChangedetection, _ *savedSecrets) { f.watches = `{"one":{"last_changed":0}}` }, false},
		{"negative changed", func(f *fakeChangedetection, _ *savedSecrets) {
			f.watches = `{"one":{"last_changed":-1,"viewed":false}}`
		}, false},
		{"nonfinite changed", func(f *fakeChangedetection, _ *savedSecrets) {
			f.watches = `{"one":{"last_changed":1e999,"viewed":false}}`
		}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake := validPeer()
			h, store, output := newVerifiedApp(t, fake)
			scenario.change(fake, store)
			err := (changedetection.App{}).Setup(t.Context(), h)
			if err == nil || errors.Is(err, helpers.ErrManual) != scenario.manual {
				t.Fatalf("expected rejected verification, manual=%t, got %v", scenario.manual, err)
			}
			if store.writes != 0 {
				t.Fatal("failed verification mutated credentials")
			}
			if (scenario.name == "missing key" || scenario.name == "missing password") && len(fake.requests) != 0 {
				t.Fatal("missing prerequisites reached the peer")
			}
			if strings.Contains(output.String()+err.Error(), "synthetic-") {
				t.Fatal("failed verification exposed credentials or response data")
			}
		})
	}
}

func TestVerificationDoesNotFollowCredentialBearingRedirects(t *testing.T) {
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("verification followed an outside redirect")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer outside.Close()
	for _, phase := range []string{"API", "login"} {
		t.Run(phase, func(t *testing.T) {
			fake := validPeer()
			if phase == "API" {
				fake.apiRedirect = outside.URL + "/api/v1/systeminfo"
			} else {
				fake.loginRedirect = outside.URL + "/login"
			}
			h, store, _ := newVerifiedApp(t, fake)
			if err := (changedetection.App{}).Check(t.Context(), h); err == nil {
				t.Fatal("redirected verification unexpectedly succeeded")
			}
			if store.writes != 0 {
				t.Fatal("redirected verification replaced a credential")
			}
			for _, request := range fake.requests {
				if request == "POST /login" {
					t.Fatal("unsafe redirect reached the password request")
				}
			}
		})
	}
}
