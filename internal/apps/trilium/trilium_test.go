package trilium_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/trilium"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

const password = "synthetic-admin-password"

type secrets struct {
	values map[string]string
	writes int
}

func (s *secrets) Get(key string) string { return s.values[key] }
func (s *secrets) Set(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

type fakeTrilium struct {
	mu              sync.Mutex
	state           map[string]any
	before          map[string]any
	after           map[string]any
	info            map[string]any
	anonymousStatus int
	statusCode      int
	loginCode       int
	location        string
	password        string
	setCookie       bool
	requests        []string
}

func newFake() *fakeTrilium {
	return &fakeTrilium{
		state: map[string]any{
			"isInitialized": true, "schemaExists": true, "authRequired": false,
			"secondFactorRequired": false, "hasExistingData": false, "setupOperation": nil, "syncVersion": 39,
		},
		before: map[string]any{
			"dbInitialized": true, "loggedIn": false,
			"login": map[string]any{"ssoEnabled": false, "totpEnabled": false},
		},
		after: map[string]any{"dbInitialized": true, "passwordSet": true, "loggedIn": true},
		info: map[string]any{
			"appVersion": "0.106.1", "dbVersion": 242, "syncVersion": 39,
			"buildDate": "2026-10-23T21:07:46.618Z", "buildRevision": "2be0c6ed7867fca60c3ed315b449952b536efe75",
			"clipperProtocolVersion": "1.0", "utcDateTime": "2026-11-02T12:00:00Z",
		},
		anonymousStatus: http.StatusUnauthorized,
		statusCode:      http.StatusOK,
		loginCode:       http.StatusFound,
		location:        ".",
		password:        password,
		setCookie:       true,
	}
}

func (f *fakeTrilium) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	cookie, err := r.Cookie("trilium.sid")
	signedIn := err == nil && cookie.Value == "synthetic-session"
	switch r.Method + " " + r.URL.Path {
	case "GET /api/health-check":
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "GET /api/setup/status":
		w.WriteHeader(f.statusCode)
		json.NewEncoder(w).Encode(f.state)
	case "GET /bootstrap":
		if signedIn {
			json.NewEncoder(w).Encode(f.after)
			return
		}
		json.NewEncoder(w).Encode(f.before)
	case "GET /api/app-info":
		if !signedIn {
			w.WriteHeader(f.anonymousStatus)
			return
		}
		json.NewEncoder(w).Encode(f.info)
	case "POST /login":
		if r.ParseForm() != nil || len(r.PostForm) != 1 || r.PostForm.Get("password") != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "private-note-content "+password)
			return
		}
		if f.setCookie {
			http.SetCookie(w, &http.Cookie{Name: "trilium.sid", Value: "synthetic-session", Path: "/", HttpOnly: true})
		}
		w.Header().Set("Location", f.location)
		w.WriteHeader(f.loginCode)
	default:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "private-note-content "+password)
	}
}

func (f *fakeTrilium) requestCount(request string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, got := range f.requests {
		if got == request {
			count++
		}
	}
	return count
}

func (f *fakeTrilium) assertReadOnly(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		switch request {
		case "GET /api/health-check", "GET /api/setup/status", "GET /bootstrap", "GET /api/app-info", "POST /login":
		default:
			t.Fatalf("unexpected request: %s", request)
		}
	}
}

type harness struct {
	h       *helpers.H
	secrets *secrets
	records []helpers.StepResult
	events  []events.Event
}

func newHarness(t *testing.T, handler http.Handler) *harness {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	deps := systemtest.NewDeps(t)
	box := &harness{secrets: &secrets{values: map[string]string{
		"ADMIN_USER": "unused-username", "ADMIN_PASSWORD": password,
		"TRILIUM_HOMARR_API_KEY": "existing-unscoped-token", "TRILIUM_HOMARR_API_KEY_ID": "existing-id",
		"TRILIUM_HOMARR_API_KEY_PENDING": "preserved-pending-evidence", "UNRELATED_KEY": "preserved-unrelated-key",
	}}}
	log := events.Discard()
	log.AddSink(events.SinkFunc(func(event events.Event) { box.events = append(box.events, event) }))
	box.h = helpers.New(helpers.Config{
		App: "trilium", Service: "trilium", ContainerPort: 8080,
		BaseURL: server.URL, URL: server.URL, HTTP: server.Client(),
		Secrets: box.secrets, Log: log, Now: deps.Now, Sleep: deps.Sleep,
		Record: func(record helpers.StepResult) { box.records = append(box.records, record) },
	})
	return box
}

func TestAppContractKeepsOfficialVersionedImageAndNarrowDataMount(t *testing.T) {
	var peer app.App = trilium.App{}
	info := peer.Info()
	if info.ID != "trilium" || info.Service != "trilium" || info.Category != "Productivity" || info.Image != "triliumnext/trilium:v0.106.1" || info.Port != 8080 || info.ContainerPortFor(18080) != 8080 || !info.WebSockets {
		t.Fatalf("unexpected App contract: %+v", info)
	}
	if len(info.Description) > 100 || len(peer.Inputs()) != 0 || len(peer.Links()) != 0 {
		t.Fatal("unexpected description, input or consumer Link")
	}
	compose := peer.Compose(app.RenderContext{Port: 18080})
	for _, fragment := range []string{"image: " + info.Image, "restart: unless-stopped", "TZ: ${TZ}", "./data:/home/node/trilium-data", "${BIND_IP}:18080:8080"} {
		if !strings.Contains(compose, fragment) {
			t.Errorf("Compose is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{":latest", "user:", "privileged:", "docker.sock", "ADMIN_PASSWORD", "env_file:", "/etc/timezone", "depends_on:"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("Compose contains unsupported or broad configuration %q", forbidden)
		}
	}
	guide := peer.(app.StandaloneGuide).StandaloneGuide()
	for _, needed := range []string{"sync", "Restore", "entire data folder", "full notebook API access", "not read-only"} {
		if !strings.Contains(guide, needed) {
			t.Errorf("standalone guide is missing %q", needed)
		}
	}
}

func TestExistingPasswordSessionThenCheckAndRerunPreserveCredentials(t *testing.T) {
	fake := newFake()
	box := newHarness(t, fake)
	before := maps.Clone(box.secrets.values)
	peer := trilium.App{}
	for _, run := range []func() error{
		func() error { return peer.Setup(t.Context(), box.h) },
		func() error { return peer.Check(t.Context(), box.h) },
		func() error { return peer.Setup(t.Context(), box.h) },
	} {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	if fake.requestCount("POST /login") != 3 || fake.requestCount("GET /api/app-info") != 6 {
		t.Fatal("verification did not authenticate and read protected application information afresh")
	}
	if box.secrets.writes != 0 || !reflect.DeepEqual(before, box.secrets.values) {
		t.Fatal("read-only verification changed credentials or pending evidence")
	}
	fake.assertReadOnly(t)
}

func TestManualPrerequisitesNeverBootstrapOrIssueTokens(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		alter      func(*fakeTrilium)
		wantLogins int
	}{
		{"new-notebook", func(f *fakeTrilium) { f.state["isInitialized"], f.state["schemaExists"] = false, false }, 0},
		{"partial-sync", func(f *fakeTrilium) { f.state["isInitialized"] = false }, 0},
		{"existing-data", func(f *fakeTrilium) { f.state["hasExistingData"] = true }, 0},
		{"locked-setup", func(f *fakeTrilium) { f.state["authRequired"] = true }, 0},
		{"second-factor-setup", func(f *fakeTrilium) { f.state["secondFactorRequired"] = true }, 0},
		{"setup-operation", func(f *fakeTrilium) { f.state["setupOperation"] = "restore" }, 0},
		{"missing-operation", func(f *fakeTrilium) { delete(f.state, "setupOperation") }, 0},
		{"missing-existing-data", func(f *fakeTrilium) { delete(f.state, "hasExistingData") }, 0},
		{"null-state-flag", func(f *fakeTrilium) { f.state["schemaExists"] = nil }, 0},
		{"missing-password", func(f *fakeTrilium) { f.before = map[string]any{"dbInitialized": true, "passwordSet": false} }, 0},
		{"sso", func(f *fakeTrilium) { f.before["login"].(map[string]any)["ssoEnabled"] = true }, 0},
		{"totp", func(f *fakeTrilium) { f.before["login"].(map[string]any)["totpEnabled"] = true }, 0},
		{"missing-sso-flag", func(f *fakeTrilium) { delete(f.before["login"].(map[string]any), "ssoEnabled") }, 0},
		{"authentication-disabled", func(f *fakeTrilium) { f.anonymousStatus = http.StatusOK }, 0},
		{"rejected-password", func(f *fakeTrilium) { f.password = "different-existing-password" }, 1},
		{"rate-limited", func(f *fakeTrilium) { f.loginCode = http.StatusTooManyRequests }, 1},
		{"missing-session-cookie", func(f *fakeTrilium) { f.setCookie = false }, 1},
		{"missing-authenticated-flag", func(f *fakeTrilium) { delete(f.after, "loggedIn") }, 1},
		{"unprotected-session", func(f *fakeTrilium) { f.after["passwordSet"] = false }, 1},
		{"missing-app-info", func(f *fakeTrilium) { f.info = map[string]any{} }, 1},
		{"incompatible-version", func(f *fakeTrilium) { f.info["appVersion"] = "0.99.5" }, 1},
		{"inconsistent-sync", func(f *fakeTrilium) { f.info["syncVersion"] = 38 }, 1},
		{"missing-database-version", func(f *fakeTrilium) { delete(f.info, "dbVersion") }, 1},
		{"missing-build-revision", func(f *fakeTrilium) { f.info["buildRevision"] = "" }, 1},
		{"invalid-build-date", func(f *fakeTrilium) { f.info["buildDate"] = "not-a-date" }, 1},
		{"invalid-start-date", func(f *fakeTrilium) { f.info["utcDateTime"] = "not-a-date" }, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake := newFake()
			scenario.alter(fake)
			box := newHarness(t, fake)
			before := maps.Clone(box.secrets.values)
			for _, run := range []func() error{
				func() error { return (trilium.App{}).Setup(t.Context(), box.h) },
				func() error { return (trilium.App{}).Check(t.Context(), box.h) },
			} {
				err := run()
				var manual *helpers.ManualError
				if !errors.Is(err, helpers.ErrManual) || !errors.As(err, &manual) || !strings.Contains(manual.Hint, "self-host setup trilium") {
					t.Fatalf("expected actionable Manual, got %v", err)
				}
				if strings.Contains(fmt.Sprint(err, box.records, box.events), password) || strings.Contains(fmt.Sprint(err, box.records, box.events), "private-note-content") {
					t.Fatal("diagnostics exposed a credential or private response")
				}
			}
			if fake.requestCount("POST /login") != 2*scenario.wantLogins {
				t.Fatal("sign-in was retried or attempted across a Manual prerequisite")
			}
			if box.secrets.writes != 0 || !reflect.DeepEqual(before, box.secrets.values) {
				t.Fatal("Manual changed saved credentials or pending evidence")
			}
			fake.assertReadOnly(t)
		})
	}
}

func TestHomarrRemainsManualEvenWithSavedUnscopedCredentials(t *testing.T) {
	fake := newFake()
	box := newHarness(t, fake)
	box.h.Peer = func(id string) *helpers.H {
		if id == "homarr" {
			return &helpers.H{}
		}
		return nil
	}
	err := (trilium.App{}).Setup(t.Context(), box.h)
	if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "positive notebook identity") {
		t.Fatalf("expected explicit native identity/issuance hold, got %v", err)
	}
	last := box.records[len(box.records)-1]
	if last.ID != "homarr-key" || last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, "not read-only") {
		t.Fatal("Homarr hold did not record its real privilege and identity limits")
	}
	if err := (trilium.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("Check hid the unfinished native prerequisite")
	}
	if box.secrets.writes != 0 {
		t.Fatal("native hold changed credentials")
	}
	fake.assertReadOnly(t)
}

func TestMissingProjectPasswordDoesNotAttemptSignIn(t *testing.T) {
	fake := newFake()
	box := newHarness(t, fake)
	delete(box.secrets.values, "ADMIN_PASSWORD")
	err := (trilium.App{}).Setup(t.Context(), box.h)
	if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "Admin password is missing") {
		t.Fatalf("expected a credential recovery hold, got %v", err)
	}
	if fake.requestCount("POST /login") != 0 || box.secrets.writes != 0 {
		t.Fatal("missing Project password caused a credential action")
	}
	fake.assertReadOnly(t)
}

func TestPasswordRedirectCannotReachAnotherOrigin(t *testing.T) {
	requests := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(target.Close)
	fake := newFake()
	fake.location = target.URL
	box := newHarness(t, fake)
	if err := (trilium.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("external login redirect was not held: %v", err)
	}
	if requests != 0 || fake.requestCount("POST /login") != 1 {
		t.Fatal("login followed a redirect or repeated the password submission")
	}
	fake.assertReadOnly(t)
}

func TestSetupFailureDoesNotExposeResponseOrBecomeBootstrap(t *testing.T) {
	fake := newFake()
	fake.statusCode = http.StatusServiceUnavailable
	box := newHarness(t, fake)
	err := (trilium.App{}).Setup(t.Context(), box.h)
	if err == nil || errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("unexpected failed status handling: %v", err)
	}
	if fake.requestCount("POST /login") != 0 || box.secrets.writes != 0 {
		t.Fatal("failed setup state caused a sign-in or credential mutation")
	}
	fake.assertReadOnly(t)
}
