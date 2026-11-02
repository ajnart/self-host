package karakeep_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/karakeep"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"go.yaml.in/yaml/v3"
)

type secrets struct {
	values map[string]string
	writes []string
	fail   string
	failAt int
	calls  map[string]int
}

func (s *secrets) Get(key string) string { return s.values[key] }

func (s *secrets) Set(key, value string) error {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[key]++
	if key == s.fail && (s.failAt == 0 || s.calls[key] == s.failAt) {
		return errors.New("synthetic private storage failure")
	}
	s.writes = append(s.writes, key)
	s.values[key] = value
	return nil
}

type fakeKarakeep struct {
	mu             sync.Mutex
	requests       []string
	email          string
	ownerID        string
	loginCode      int
	loginBody      string
	session        string
	profile        string
	logoutCode     int
	keys           []map[string]any
	inventoryBody  string
	afterInventory string
	issued         int
	createCode     int
	createBody     string
	dropCreate     bool
	keyCode        int
	keyOwner       string
}

const karakeepTestKey = "ak2_aaaaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const karakeepTestKeyID = "aaaaaaaaaaaaaaaaaaaa"

func karakeepTestInventory() map[string]any {
	return map[string]any{"id": "key-resource-1", "name": "self-host Homarr statistics", "keyId": karakeepTestKeyID, "scopes": []string{"users:read"}}
}

func (f *fakeKarakeep) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET /api/health":
		fmt.Fprint(w, `{"status":"ok","message":"Web app is working"}`)
	case "POST /api/auth/sign-in/email":
		var body struct{ Email, Password string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Email != f.email || body.Password != "synthetic-password" || r.Header.Get("Origin") != "http://"+r.Host {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.loginCode != 0 {
			w.Header().Set("Location", "/unexpected")
			w.WriteHeader(f.loginCode)
			fmt.Fprint(w, `{"message":"synthetic-private-response"}`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "better-auth.session_token", Value: "synthetic-cookie", Path: "/", HttpOnly: true})
		if f.loginBody != "" {
			fmt.Fprint(w, f.loginBody)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "synthetic-token", "user": map[string]any{"id": f.ownerID, "email": f.email, "role": "admin"}})
	case "GET /api/auth/get-session":
		if !f.authenticated(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.session != "" {
			fmt.Fprint(w, f.session)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"userId": f.ownerID, "token": "synthetic-token", "expiresAt": "2026-11-03T00:00:00Z"},
			"user":    map[string]any{"id": f.ownerID, "email": f.email, "role": "admin"},
		})
	case "GET /api/v1/users/me":
		ownerID := f.ownerID
		if r.Header.Get("Authorization") != "" {
			if r.Header.Get("Authorization") != "Bearer "+karakeepTestKey || f.keyCode != 0 {
				code := f.keyCode
				if code == 0 {
					code = http.StatusUnauthorized
				}
				w.WriteHeader(code)
				return
			}
			if f.keyOwner != "" {
				ownerID = f.keyOwner
			}
		} else if !f.authenticated(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.profile != "" {
			fmt.Fprint(w, f.profile)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": ownerID, "email": f.email, "localUser": true})
	case "GET /api/trpc/apiKeys.list":
		if !f.authenticated(r) || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if f.issued > 0 && f.afterInventory != "" {
			fmt.Fprint(w, f.afterInventory)
			return
		}
		if f.inventoryBody != "" {
			fmt.Fprint(w, f.inventoryBody)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{"keys": f.keys}}}})
	case "POST /api/trpc/apiKeys.create":
		var input struct {
			JSON struct {
				Name   string
				Scopes []string
			} `json:"json"`
		}
		if !f.authenticated(r) || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != "http://"+r.Host || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.Name != "self-host Homarr statistics" || !slices.Equal(input.JSON.Scopes, []string{"users:read"}) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.issued++
		f.keys = append(f.keys, karakeepTestInventory())
		if f.dropCreate {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
			return
		}
		if f.createCode != 0 {
			w.WriteHeader(f.createCode)
		}
		if f.createBody != "" {
			fmt.Fprint(w, f.createBody)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{"id": "key-resource-1", "name": "self-host Homarr statistics", "key": karakeepTestKey, "scopes": []string{"users:read"}}}}})
	case "POST /api/auth/sign-out":
		if !f.authenticated(r) || r.Header.Get("Origin") != "http://"+r.Host {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.logoutCode != 0 {
			w.WriteHeader(f.logoutCode)
			fmt.Fprint(w, `{"error":"synthetic-private-response"}`)
			return
		}
		fmt.Fprint(w, `{"success":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (*fakeKarakeep) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie("better-auth.session_token")
	return err == nil && cookie.Value == "synthetic-cookie"
}

type harness struct {
	h       *helpers.H
	secrets *secrets
	steps   []helpers.StepResult
	events  []events.Event
	fake    *fakeKarakeep
}

func newHarness(t *testing.T, fake *fakeKarakeep) *harness {
	t.Helper()
	if fake.email == "" {
		fake.email = "admin@selfhost.local"
	}
	if fake.ownerID == "" {
		fake.ownerID = "selected-owner"
	}
	if fake.keys == nil {
		fake.keys = []map[string]any{karakeepTestInventory()}
	}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	box := &harness{fake: fake, secrets: &secrets{values: map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password", "KARAKEEP_AUTH_SECRET": "synthetic-signing-secret",
		"KARAKEEP_HOMARR_API_KEY": karakeepTestKey, "UNRELATED_PRIVATE_VALUE": "preserved-unrelated",
	}}}
	fingerprint := sha256.Sum256([]byte(karakeepTestKey))
	ownership, err := json.Marshal(map[string]any{"id": "key-resource-1", "keyId": karakeepTestKeyID, "name": "self-host Homarr statistics", "scopes": []string{"users:read"}, "accountId": fake.ownerID, "fingerprint": hex.EncodeToString(fingerprint[:])})
	if err != nil {
		t.Fatal(err)
	}
	box.secrets.values["KARAKEEP_HOMARR_KEY_OWNERSHIP"] = string(ownership)
	log := events.Discard()
	log.AddSink(events.SinkFunc(func(event events.Event) { box.events = append(box.events, event) }))
	box.h = helpers.New(helpers.Config{
		App: "karakeep", Service: "karakeep", ContainerPort: 3000,
		BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Secrets: box.secrets, Log: log,
		Now:    func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) },
		Record: func(result helpers.StepResult) { box.steps = append(box.steps, result) },
	})
	return box
}

func TestKarakeepOfficialMinimalComposeAndContainedDefaults(t *testing.T) {
	a := karakeep.App{}
	info := a.Info()
	if info.ID != "karakeep" || info.Service != "karakeep" || info.Image != "ghcr.io/karakeep-app/karakeep:release" || info.ContainerPortFor(33003) != 3000 || info.Port != 3000 {
		t.Fatalf("unexpected App metadata: %+v", info)
	}
	for _, exposure := range []string{"lan", "local"} {
		t.Run(exposure, func(t *testing.T) {
			var document struct {
				Services map[string]struct {
					Image, Restart string
					Environment    map[string]string
					Volumes, Ports []string
				} `yaml:"services"`
			}
			compose := a.Compose(app.RenderContext{Port: 33003, Settings: map[string]string{"EXPOSURE": exposure}})
			if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
				t.Fatal(err)
			}
			service := document.Services["karakeep"]
			if len(document.Services) != 1 || service.Image != info.Image || service.Restart != "unless-stopped" || !slices.Equal(service.Volumes, []string{"./data:/data"}) || !slices.Equal(service.Ports, []string{"${BIND_IP}:33003:3000"}) {
				t.Fatalf("unexpected minimal deployment: %s", compose)
			}
			for key, want := range map[string]string{
				"DATA_DIR": "/data", "NEXTAUTH_SECRET": "${KARAKEEP_AUTH_SECRET}", "DISABLE_SIGNUPS": "true", "DISABLE_NEW_RELEASE_CHECK": "true",
				"INFERENCE_ENABLE_AUTO_TAGGING": "false", "INFERENCE_ENABLE_AUTO_SUMMARIZATION": "false", "EMBEDDING_ENABLE_AUTO_INDEXING": "false", "OCR_LANGS": "",
			} {
				if got, present := service.Environment[key]; !present || got != want {
					t.Errorf("%s = %q (present %v), want %q", key, got, present, want)
				}
			}
			enabled := strings.Split(service.Environment["WORKERS_ENABLED_WORKERS"], ",")
			disabled := strings.Split(service.Environment["WORKERS_DISABLED_WORKERS"], ",")
			if !slices.Equal(enabled, []string{"backup"}) || !slices.Equal(disabled, []string{"backup"}) {
				t.Fatal("contained worker selection changed")
			}
			for _, worker := range []string{"crawler", "lowPriorityCrawler", "inference", "embeddings", "search", "adminMaintenance", "video", "feed", "assetPreprocessing", "webhook", "ruleEngine", "backup", "import", "future-worker"} {
				if slices.Contains(enabled, worker) && !slices.Contains(disabled, worker) {
					t.Errorf("background worker %s is not contained", worker)
				}
			}
			for _, key := range []string{"OPENAI_API_KEY", "OLLAMA_BASE_URL", "MEILI_ADDR", "BROWSER_WEB_URL", "BROWSER_WEBSOCKET_URL", "ASSETS_DIR", "AUTH_TRUSTED_ORIGINS", "DISABLE_PASSWORD_AUTH"} {
				if _, present := service.Environment[key]; present {
					t.Errorf("minimal App unexpectedly sets %s", key)
				}
			}
			wantAddress := "http://${LAN_IP}:33003"
			if exposure == "local" {
				wantAddress = "http://127.0.0.1:33003"
			}
			if service.Environment["NEXTAUTH_URL"] != wantAddress {
				t.Errorf("browser Address = %q, want %q", service.Environment["NEXTAUTH_URL"], wantAddress)
			}
		})
	}
	if got := a.Secrets(); len(got) != 1 || got[0].Name != "auth_secret" || got[0].Length < 32 {
		t.Fatalf("unexpected first-start secrets: %+v", got)
	}
	for _, input := range a.Inputs() {
		if !input.Private || input.Required {
			t.Fatalf("owner input must be optional and private: %+v", input)
		}
	}
	if len(a.Links()) != 0 || !strings.Contains(a.StandaloneGuide(), "DISABLE_SIGNUPS") {
		t.Fatal("App slice must leave Homarr wiring pending and explain first-account recovery")
	}
}

func TestKarakeepExistingOwnerIsVerifiedPinnedAndReused(t *testing.T) {
	box := newHarness(t, &fakeKarakeep{})
	box.h.Dir = t.TempDir()
	for _, name := range []string{"db.db", "tags.fixture", "attachment.fixture"} {
		if err := os.WriteFile(filepath.Join(box.h.Dir, name), []byte("preserved synthetic data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := maps.Clone(box.secrets.values)
	for attempt := range 2 {
		box.steps = nil
		if err := (karakeep.App{}).Setup(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 {
			for _, step := range box.steps {
				if step.Status != stepstatus.StepSkip {
					t.Fatalf("rerun did not skip verified step: %+v", step)
				}
			}
		}
	}
	if box.secrets.Get("KARAKEEP_ACCOUNT_ID") != "selected-owner" || box.secrets.Get("KARAKEEP_ADMIN_EMAIL") != "admin@selfhost.local" {
		t.Fatal("verified owner identity was not retained")
	}
	for key, value := range before {
		if box.secrets.Get(key) != value {
			t.Fatalf("existing private value %s changed", key)
		}
	}
	writes := len(box.secrets.writes)
	if err := (karakeep.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if len(box.secrets.writes) != writes {
		t.Fatal("Check wrote private state")
	}
	for _, name := range []string{"db.db", "tags.fixture", "attachment.fixture"} {
		data, err := os.ReadFile(filepath.Join(box.h.Dir, name))
		if err != nil || string(data) != "preserved synthetic data" {
			t.Fatalf("local App data %s changed: %v", name, err)
		}
	}
	for _, private := range []string{"synthetic-password", "synthetic-token", "synthetic-cookie"} {
		if box.h.Log.Redact(private) == private || strings.Contains(fmt.Sprint(box.events, box.steps), private) {
			t.Fatal("verification credentials were not kept private")
		}
	}
	for _, request := range box.fake.requests {
		if strings.Contains(request, "sign-up") || strings.Contains(request, "apiKeys.") && request != "GET /api/trpc/apiKeys.list" || strings.Contains(request, "stats") || strings.Contains(request, "bookmarks") {
			t.Fatalf("App-only verification performed an unapproved action: %s", request)
		}
	}
}

func TestKarakeepExplicitExistingOwnerEmail(t *testing.T) {
	box := newHarness(t, &fakeKarakeep{email: "owner@example.test"})
	box.secrets.values["KARAKEEP_ADMIN_EMAIL"] = "owner@example.test"
	if err := (karakeep.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if box.secrets.Get("KARAKEEP_ACCOUNT_ID") != "selected-owner" {
		t.Fatal("selected owner was not pinned")
	}
}

func TestKarakeepMissingOrRejectedOwnerNeverCreatesAnAccount(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			box := newHarness(t, &fakeKarakeep{loginCode: status})
			before := maps.Clone(box.secrets.values)
			err := (karakeep.App{}).Setup(t.Context(), box.h)
			var manual *helpers.ManualError
			if !errors.As(err, &manual) || !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) || !strings.Contains(manual.Hint, "self-host setup karakeep") {
				t.Fatalf("missing actionable Manual boundary: %v", err)
			}
			if !maps.Equal(before, box.secrets.values) || len(box.secrets.writes) != 0 {
				t.Fatal("rejected login changed private state")
			}
			for _, request := range box.fake.requests {
				if request != "GET /api/health" && request != "POST /api/auth/sign-in/email" {
					t.Fatalf("login rejection caused an unexpected request: %s", request)
				}
			}
			if strings.Contains(fmt.Sprint(err, box.events, box.steps), "synthetic-private-response") {
				t.Fatal("upstream body escaped into diagnostics")
			}
		})
	}
}

func TestKarakeepRejectsIncompleteOrDifferentAuthenticatedIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		login   string
		session string
		profile string
	}{
		{name: "empty login", login: `{}`},
		{name: "blank token", login: `{"token":" ","user":{"id":"selected-owner","email":"admin@selfhost.local","role":"admin"}}`},
		{name: "login error with data", login: `{"token":"synthetic-token","user":{"id":"selected-owner","email":"admin@selfhost.local","role":"admin"},"error":"synthetic-private-response"}`},
		{name: "ordinary user", login: `{"token":"synthetic-token","user":{"id":"selected-owner","email":"admin@selfhost.local","role":"user"}}`},
		{name: "wrong login email", login: `{"token":"synthetic-token","user":{"id":"selected-owner","email":"other@example.test","role":"admin"}}`},
		{name: "null session", session: `null`},
		{name: "session without session metadata", session: `{"user":{"id":"selected-owner","email":"admin@selfhost.local","role":"admin"}}`},
		{name: "expired session", session: `{"user":{"id":"selected-owner","email":"admin@selfhost.local","role":"admin"},"session":{"userId":"selected-owner","token":"synthetic-token","expiresAt":"2026-11-01T00:00:00Z"}}`},
		{name: "different session", session: `{"user":{"id":"selected-owner","email":"admin@selfhost.local","role":"admin"},"session":{"userId":"different-owner","token":"synthetic-token","expiresAt":"2026-11-03T00:00:00Z"}}`},
		{name: "different profile", profile: `{"id":"different-owner","email":"admin@selfhost.local","localUser":true}`},
		{name: "missing local account flag", profile: `{"id":"selected-owner","email":"admin@selfhost.local"}`},
		{name: "SSO-only profile", profile: `{"id":"selected-owner","email":"admin@selfhost.local","localUser":false}`},
		{name: "body error", profile: `{"id":"selected-owner","email":"admin@selfhost.local","localUser":true,"error":"synthetic-private-response"}`},
		{name: "HTML response", profile: `<html>synthetic-private-response</html>`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			box := newHarness(t, &fakeKarakeep{loginBody: scenario.login, session: scenario.session, profile: scenario.profile})
			before := maps.Clone(box.secrets.values)
			err := (karakeep.App{}).Setup(t.Context(), box.h)
			if !errors.Is(err, helpers.ErrManual) || !maps.Equal(before, box.secrets.values) {
				t.Fatalf("invalid identity was accepted or changed private state: %v", err)
			}
			if strings.Contains(fmt.Sprint(err, box.events, box.steps), "synthetic-private-response") {
				t.Fatal("upstream body escaped into diagnostics")
			}
		})
	}
}

func TestKarakeepRecordedOwnerDoesNotFollowAccountReplacement(t *testing.T) {
	box := newHarness(t, &fakeKarakeep{ownerID: "replacement-owner"})
	box.secrets.values["KARAKEEP_ACCOUNT_ID"] = "original-owner"
	box.secrets.values["KARAKEEP_ADMIN_EMAIL"] = "admin@selfhost.local"
	before := maps.Clone(box.secrets.values)
	if err := (karakeep.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("replacement owner was accepted: %v", err)
	}
	if err := (karakeep.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Check accepted a replacement owner: %v", err)
	}
	if !maps.Equal(before, box.secrets.values) || len(box.secrets.writes) != 0 {
		t.Fatal("recorded owner or other credentials were changed")
	}
}

func TestKarakeepUnrecordedCheckAndMissingCredentialsRemainManual(t *testing.T) {
	box := newHarness(t, &fakeKarakeep{})
	if err := (karakeep.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Check accepted unrecorded identity: %v", err)
	}
	delete(box.secrets.values, "ADMIN_PASSWORD")
	if err := (karakeep.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Setup without credentials = %v", err)
	}
	for _, request := range box.fake.requests {
		if request != "GET /api/health" {
			t.Fatalf("missing credentials were submitted: %s", request)
		}
	}
}

func TestKarakeepPartialIdentityPersistenceDoesNotReplaceTheOwner(t *testing.T) {
	box := newHarness(t, &fakeKarakeep{})
	box.secrets.fail = "KARAKEEP_ADMIN_EMAIL"
	if err := (karakeep.App{}).Setup(t.Context(), box.h); err == nil {
		t.Fatal("private storage failure was ignored")
	}
	if box.secrets.Get("KARAKEEP_ACCOUNT_ID") != "selected-owner" {
		t.Fatal("positively verified account ID was not retained")
	}
	box.secrets.fail = ""
	box.fake.mu.Lock()
	box.fake.ownerID = "replacement-owner"
	box.fake.mu.Unlock()
	if err := (karakeep.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("partial local persistence allowed replacement: %v", err)
	}
	if box.secrets.Get("KARAKEEP_ACCOUNT_ID") != "selected-owner" {
		t.Fatal("pinned account changed")
	}
}

func TestKarakeepSessionCleanupFailureIsNotAcceptance(t *testing.T) {
	box := newHarness(t, &fakeKarakeep{logoutCode: http.StatusInternalServerError})
	if err := (karakeep.App{}).Setup(t.Context(), box.h); err == nil {
		t.Fatal("session cleanup failure was ignored")
	}
	if len(box.secrets.writes) != 0 {
		t.Fatal("identity was saved after incomplete verification")
	}
}

func TestKarakeepInvalidPrivateSelectionIsNotSubmitted(t *testing.T) {
	for _, email := range []string{"not-an-email", "Display <owner@example.test>", "owner@example.test\nInjected: value"} {
		t.Run(email, func(t *testing.T) {
			box := newHarness(t, &fakeKarakeep{})
			box.secrets.values["KARAKEEP_ADMIN_EMAIL"] = email
			box.secrets.values["KARAKEEP_ACCOUNT_ID"] = "selected-owner"
			if err := (karakeep.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("invalid private selection = %v", err)
			}
			if len(box.fake.requests) != 0 || len(box.secrets.writes) != 0 {
				t.Fatal("invalid selection was submitted or saved")
			}
		})
	}
}
