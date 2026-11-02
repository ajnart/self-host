package homebox_test

import (
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

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/homebox"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/tools/sitegen/standalone"
	"go.yaml.in/yaml/v3"
)

const (
	homeboxAccountID = "11111111-1111-4111-8111-111111111111"
	homeboxGroupID   = "22222222-2222-4222-8222-222222222222"
	homeboxOtherID   = "33333333-3333-4333-8333-333333333333"
	homeboxPassword  = "synthetic-password+&=only"
	homeboxToken     = "synthetic-homebox-session-token"
	homeboxKey       = "hb_synthetic-existing-key"
)

type homeboxSecrets struct {
	values  map[string]string
	writes  []string
	failSet bool
}

func (s *homeboxSecrets) Get(key string) string { return s.values[key] }

func (s *homeboxSecrets) Set(key, value string) error {
	if s.failSet {
		return errors.New("synthetic secret write failure")
	}
	s.writes = append(s.writes, key)
	s.values[key] = value
	return nil
}

func newHomeboxSecrets() *homeboxSecrets {
	return &homeboxSecrets{values: map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": homeboxPassword,
		"HOMEBOX_API_KEY_PEPPER": strings.Repeat("p", 64), "UNRELATED_KEY": "synthetic-unrelated-key",
	}}
}

func homeboxIdentity() string {
	return `{"accountId":"` + homeboxAccountID + `","email":"admin@selfhost.local","groupId":"` + homeboxGroupID + `"}`
}

type homeboxFixture struct {
	mu          sync.Mutex
	requests    []string
	responses   map[string]any
	statuses    map[string]int
	malformed   string
	authorize   string
	loginEmail  string
	loginToken  string
	redirectURL string
}

func newHomeboxFixture() *homeboxFixture {
	return &homeboxFixture{
		authorize: "Bearer " + homeboxToken, loginEmail: "admin@selfhost.local", loginToken: "Bearer " + homeboxToken,
		statuses: map[string]int{},
		responses: map[string]any{
			"/api/v1/status": map[string]any{"health": true},
			"/api/v1/users/self": map[string]any{"item": map[string]any{
				"id": homeboxAccountID, "email": "admin@selfhost.local", "defaultGroupId": homeboxGroupID, "groupIds": []string{homeboxGroupID},
			}},
			"/api/v1/groups": map[string]any{"id": homeboxGroupID, "currency": "USD"},
			"/api/v1/groups/statistics": map[string]any{
				"totalItems": 0, "totalLocations": 8, "totalTags": 6, "totalWithWarranty": 0, "totalUsers": 1, "totalItemPrice": 0,
			},
		},
	}
}

func (f *homeboxFixture) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.URL.RawQuery != "" || r.Header.Get("X-Tenant") != "" {
			t.Error("verification added a tenant override or query credential")
		}
		if status := f.statuses[r.URL.Path]; status != 0 {
			if status == http.StatusFound {
				w.Header().Set("Location", f.redirectURL)
			}
			w.WriteHeader(status)
			fmt.Fprint(w, homeboxPassword+homeboxToken+homeboxKey)
			return
		}
		if r.URL.Path == f.malformed {
			fmt.Fprint(w, `{"privateInventory":"synthetic-private-item",`)
			return
		}
		if r.URL.Path == "/api/v1/users/login" && r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
				r.Form.Get("username") != f.loginEmail || r.Form.Get("password") != homeboxPassword || len(r.Form) != 2 {
				t.Error("Homebox login did not use the intended form-encoded credentials")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": f.loginToken})
			return
		}
		if r.Method != http.MethodGet {
			t.Error("verification attempted an inventory, account or key mutation")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/api/v1/status" && r.Header.Get("Authorization") != f.authorize {
			t.Error("verification used the wrong authorization scheme or credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		value, ok := f.responses[r.URL.Path]
		if !ok {
			t.Error("verification requested an unexpected Homebox endpoint")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(value)
	})
}

func (f *homeboxFixture) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

type homeboxHarness struct {
	h       *helpers.H
	records []helpers.StepResult
	events  []events.Event
}

func newHomeboxHarness(t *testing.T, fixture *homeboxFixture, secrets *homeboxSecrets) *homeboxHarness {
	t.Helper()
	server := httptest.NewServer(fixture.handler(t))
	t.Cleanup(server.Close)
	box := &homeboxHarness{}
	log := events.Discard()
	log.AddSink(events.SinkFunc(func(event events.Event) { box.events = append(box.events, event) }))
	box.h = helpers.New(helpers.Config{
		App: "homebox", Service: "homebox", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(),
		Secrets: secrets, Log: log, Record: func(record helpers.StepResult) { box.records = append(box.records, record) },
	})
	return box
}

func TestHomeboxOfficialPersistentCompose(t *testing.T) {
	a := homebox.App{}
	var _ app.App = a
	var _ app.SecretDeclarer = a
	var _ app.AdminEmailProvider = a
	var _ app.StandaloneGuide = a
	info := a.Info()
	if info.ID != "homebox" || info.Service != "homebox" || info.Image != "ghcr.io/sysadminsmedia/homebox:latest" || info.Port != 3100 || info.ContainerPort != 7745 || len(info.Description) > 100 {
		t.Fatal("incorrect App deployment metadata")
	}
	var compose struct {
		Services map[string]struct {
			Image       string
			Restart     string
			Environment map[string]string
			Volumes     []string
			Ports       []string
			User        string
		}
	}
	if err := yaml.Unmarshal([]byte(a.Compose(app.RenderContext{Port: 43100})), &compose); err != nil {
		t.Fatal(err)
	}
	service := compose.Services["homebox"]
	if len(compose.Services) != 1 || service.Image != info.Image || service.Restart != "unless-stopped" || service.User != "" ||
		!slices.Equal(service.Volumes, []string{"./data:/data"}) || !slices.Equal(service.Ports, []string{"${BIND_IP}:43100:7745"}) {
		t.Fatal("Homebox lost its data bind, Access, image identity or single-container deployment")
	}
	for _, key := range []string{"HBOX_DEMO", "HBOX_OPTIONS_ALLOW_REGISTRATION", "HBOX_OPTIONS_ALLOW_ANALYTICS", "HBOX_OPTIONS_GITHUB_RELEASE_CHECK", "HBOX_OTEL_ENABLED"} {
		if service.Environment[key] != "false" {
			t.Fatalf("startup default %s was not contained", key)
		}
	}
	if service.Environment["HBOX_AUTH_API_KEY_PEPPER"] != "${HOMEBOX_API_KEY_PEPPER}" || service.Environment["TZ"] != "${TZ}" || len(service.Environment) != 7 {
		t.Fatal("Compose grants unexpected configuration or misses the persistent pepper")
	}
	if !slices.Equal(a.Secrets(), []app.SecretSpec{{Name: "api_key_pepper", Length: 64, Hex: true}}) {
		t.Fatal("Homebox must use the existing persistent secret generator")
	}
	for _, input := range a.Inputs() {
		if !input.Private || !input.RecoveryOnly || input.Required {
			t.Fatal("existing credential and identity recovery must remain optional and private")
		}
	}
	if len(a.Links()) != 0 || !strings.Contains(a.StandaloneGuide(), "Registration starts disabled") || !strings.Contains(a.StandaloneGuide(), "not yet supported") {
		t.Fatal("the independent App slice must not claim automatic bootstrap or Homarr coverage")
	}
	for user, expected := range map[string]string{"admin": "admin@selfhost.local", "OWNER@EXAMPLE.TEST": "owner@example.test", "": ""} {
		if a.AdminEmail(user) != expected {
			t.Fatal("incorrect Admin email mapping")
		}
	}
}

func TestHomeboxExistingLoginRemembersOnlyVerifiedInventory(t *testing.T) {
	secrets := newHomeboxSecrets()
	before := maps.Clone(secrets.values)
	fixture := newHomeboxFixture()
	box := newHomeboxHarness(t, fixture, secrets)
	data := t.TempDir()
	for _, name := range []string{"homebox.db", "attachment.bin", "config.yml"} {
		if err := os.WriteFile(filepath.Join(data, name), []byte("synthetic-existing-data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	box.h.Dir = data
	for range 2 {
		if err := (homebox.App{}).Setup(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
		if err := (homebox.App{}).Check(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
	}
	if secrets.values["HOMEBOX_ACCOUNT_IDENTITY"] != homeboxIdentity() || !slices.Equal(secrets.writes, []string{"HOMEBOX_ACCOUNT_IDENTITY"}) {
		t.Fatal("verified account selection was not saved exactly once")
	}
	for key, value := range before {
		if secrets.values[key] != value {
			t.Fatalf("Setup replaced an existing credential: %s", key)
		}
	}
	loginCalls := 0
	for _, call := range fixture.calls() {
		if call == "POST /api/v1/users/login" {
			loginCalls++
		} else if !strings.HasPrefix(call, "GET ") {
			t.Fatal("Setup changed upstream resources")
		}
	}
	if loginCalls != 1 {
		t.Fatal("Setup and Check did not reuse their invocation's login")
	}
	for _, name := range []string{"homebox.db", "attachment.bin", "config.yml"} {
		contents, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || string(contents) != "synthetic-existing-data" {
			t.Fatal("Setup changed existing App data")
		}
	}
}

func TestHomeboxStandaloneExportKeepsPersistentPrivatePepper(t *testing.T) {
	entry, err := standalone.Project(homebox.App{})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Standalone || !strings.Contains(entry.Compose, "HBOX_OPTIONS_ALLOW_REGISTRATION: \"false\"") ||
		!strings.Contains(entry.Compose, ":/data") || !strings.Contains(entry.Notes, "Registration starts disabled") {
		t.Fatal("standalone export lost persistent storage or its Manual bootstrap boundary")
	}
	pepper := false
	for _, variable := range entry.Env {
		if variable.Key == "HOMEBOX_API_KEY_PEPPER" {
			pepper = variable.Secret && !variable.External && variable.Bytes >= 32
		}
		if variable.Key == "HOMEBOX_API_KEY" || variable.Key == "HOMEBOX_ACCOUNT_IDENTITY" {
			t.Fatal("standalone export exposed Setup-only credentials or identity")
		}
	}
	if !pepper {
		t.Fatal("standalone pepper generation is missing or insecure")
	}
}

func TestHomeboxSuppliedKeyVerifiesItsSelectedOwnerWithoutLogin(t *testing.T) {
	secrets := newHomeboxSecrets()
	secrets.values["HOMEBOX_API_KEY"] = homeboxKey
	secrets.values["HOMEBOX_ACCOUNT_IDENTITY"] = homeboxIdentity()
	delete(secrets.values, "ADMIN_PASSWORD")
	fixture := newHomeboxFixture()
	fixture.authorize = "Bearer " + homeboxKey
	box := newHomeboxHarness(t, fixture, secrets)
	if err := (homebox.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if err := (homebox.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if len(secrets.writes) != 0 {
		t.Fatal("verification replaced an existing API key or identity")
	}
	for _, call := range fixture.calls() {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatal("existing API-key verification attempted login or issuance")
		}
	}
}

func TestHomeboxSavedIdentitySurvivesAdminUsernameChanges(t *testing.T) {
	secrets := newHomeboxSecrets()
	secrets.values["HOMEBOX_ACCOUNT_IDENTITY"] = homeboxIdentity()
	secrets.values["ADMIN_USER"] = "different-admin"
	before := maps.Clone(secrets.values)
	for range 2 {
		fixture := newHomeboxFixture()
		box := newHomeboxHarness(t, fixture, secrets)
		if err := (homebox.App{}).Setup(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
		if err := (homebox.App{}).Check(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
	}
	if !maps.Equal(before, secrets.values) || len(secrets.writes) != 0 {
		t.Fatal("an Admin username change replaced the selected inventory or existing login")
	}
}

func TestHomeboxManualRecoveryNeverRegistersOrReplacesCredentials(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*homeboxFixture, *homeboxSecrets)
		manual bool
	}{
		{name: "missing account", change: func(f *homeboxFixture, _ *homeboxSecrets) { f.statuses["/api/v1/users/login"] = 401 }, manual: true},
		{name: "local login disabled", change: func(f *homeboxFixture, _ *homeboxSecrets) { f.statuses["/api/v1/users/login"] = 403 }, manual: true},
		{name: "rejected API key", change: func(f *homeboxFixture, s *homeboxSecrets) {
			s.values["HOMEBOX_ACCOUNT_IDENTITY"] = homeboxIdentity()
			s.values["HOMEBOX_API_KEY"] = homeboxKey
			f.statuses["/api/v1/users/self"] = 401
		}, manual: true},
		{name: "key lacks intended identity", change: func(_ *homeboxFixture, s *homeboxSecrets) { s.values["HOMEBOX_API_KEY"] = homeboxKey }, manual: true},
		{name: "malformed saved identity", change: func(_ *homeboxFixture, s *homeboxSecrets) { s.values["HOMEBOX_ACCOUNT_IDENTITY"] = "not-json" }, manual: true},
		{name: "missing pepper", change: func(_ *homeboxFixture, s *homeboxSecrets) { delete(s.values, "HOMEBOX_API_KEY_PEPPER") }, manual: true},
		{name: "short pepper", change: func(_ *homeboxFixture, s *homeboxSecrets) { s.values["HOMEBOX_API_KEY_PEPPER"] = "short" }, manual: true},
		{name: "wrong credential owner", change: func(f *homeboxFixture, _ *homeboxSecrets) {
			f.responses["/api/v1/users/self"].(map[string]any)["item"].(map[string]any)["email"] = "someone@example.test"
		}, manual: true},
		{name: "different account same email", change: func(f *homeboxFixture, s *homeboxSecrets) {
			s.values["HOMEBOX_ACCOUNT_IDENTITY"] = homeboxIdentity()
			f.responses["/api/v1/users/self"].(map[string]any)["item"].(map[string]any)["id"] = homeboxOtherID
		}, manual: true},
		{name: "changed default inventory", change: func(f *homeboxFixture, s *homeboxSecrets) {
			s.values["HOMEBOX_ACCOUNT_IDENTITY"] = homeboxIdentity()
			account := f.responses["/api/v1/users/self"].(map[string]any)["item"].(map[string]any)
			account["defaultGroupId"], account["groupIds"] = homeboxOtherID, []string{homeboxOtherID}
		}, manual: true},
		{name: "different effective inventory", change: func(f *homeboxFixture, _ *homeboxSecrets) {
			f.responses["/api/v1/groups"].(map[string]any)["id"] = homeboxOtherID
		}, manual: true},
		{name: "unconfirmed currency", change: func(f *homeboxFixture, _ *homeboxSecrets) {
			delete(f.responses["/api/v1/groups"].(map[string]any), "currency")
		}, manual: true},
		{name: "missing account body", change: func(f *homeboxFixture, _ *homeboxSecrets) { f.responses["/api/v1/users/self"] = map[string]any{} }},
		{name: "missing group membership", change: func(f *homeboxFixture, _ *homeboxSecrets) {
			f.responses["/api/v1/users/self"].(map[string]any)["item"].(map[string]any)["groupIds"] = []string{}
		}},
		{name: "invalid account UUID", change: func(f *homeboxFixture, _ *homeboxSecrets) {
			f.responses["/api/v1/users/self"].(map[string]any)["item"].(map[string]any)["id"] = "not-an-account"
		}},
		{name: "unprefixed session token", change: func(f *homeboxFixture, _ *homeboxSecrets) { f.loginToken = homeboxToken }},
		{name: "malformed private response", change: func(f *homeboxFixture, _ *homeboxSecrets) { f.malformed = "/api/v1/users/self" }},
		{name: "login server error", change: func(f *homeboxFixture, _ *homeboxSecrets) { f.statuses["/api/v1/users/login"] = 500 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture, secrets := newHomeboxFixture(), newHomeboxSecrets()
			scenario.change(fixture, secrets)
			before := maps.Clone(secrets.values)
			box := newHomeboxHarness(t, fixture, secrets)
			err := (homebox.App{}).Setup(t.Context(), box.h)
			if err == nil || errors.Is(err, helpers.ErrManual) != scenario.manual {
				t.Fatalf("unexpected verification result: %v", err)
			}
			if !maps.Equal(before, secrets.values) || len(secrets.writes) != 0 {
				t.Fatal("failed verification changed selected identity or credentials")
			}
			if scenario.manual {
				last := box.records[len(box.records)-1]
				if last.Status != stepstatus.StepManual || last.Hint == "" || last.Error != "" || errors.Is(err, helpers.ErrStepFailed) {
					t.Fatal("Manual recovery was not actionable or became a failed step")
				}
			}
			output := fmt.Sprint(err, box.records, box.events)
			for _, private := range []string{homeboxPassword, homeboxToken, homeboxKey, "synthetic-private-item"} {
				if strings.Contains(output, private) {
					t.Fatal("verification exposed a credential or private inventory body")
				}
			}
			for _, call := range fixture.calls() {
				if strings.HasPrefix(call, "POST ") && call != "POST /api/v1/users/login" {
					t.Fatal("Manual recovery attempted account or key creation")
				}
				if scenario.name == "rejected API key" && call == "POST /api/v1/users/login" {
					t.Fatal("rejected key silently fell back to the Admin password")
				}
			}
		})
	}
}

func TestHomeboxStatisticsRequireRealDataBeforeRememberingIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(map[string]any)
		ok     bool
	}{
		{name: "explicit empty inventory", ok: true},
		{name: "populated inventory", change: func(s map[string]any) { s["totalItems"], s["totalWithWarranty"], s["totalItemPrice"] = 3, 2, 12.50 }, ok: true},
		{name: "legacy labels", change: func(s map[string]any) { delete(s, "totalTags"); s["totalLabels"] = 0 }, ok: true},
		{name: "missing items", change: func(s map[string]any) { delete(s, "totalItems") }},
		{name: "null items", change: func(s map[string]any) { s["totalItems"] = nil }},
		{name: "fractional count", change: func(s map[string]any) { s["totalLocations"] = 0.5 }},
		{name: "negative count", change: func(s map[string]any) { s["totalTags"] = -1 }},
		{name: "missing labels and tags", change: func(s map[string]any) { delete(s, "totalTags") }},
		{name: "missing value", change: func(s map[string]any) { delete(s, "totalItemPrice") }},
		{name: "negative value", change: func(s map[string]any) { s["totalItemPrice"] = -1 }},
		{name: "missing users", change: func(s map[string]any) { delete(s, "totalUsers") }},
		{name: "no authenticated member", change: func(s map[string]any) { s["totalUsers"] = 0 }},
		{name: "warranties exceed items", change: func(s map[string]any) { s["totalWithWarranty"] = 1 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture, secrets := newHomeboxFixture(), newHomeboxSecrets()
			if scenario.change != nil {
				scenario.change(fixture.responses["/api/v1/groups/statistics"].(map[string]any))
			}
			box := newHomeboxHarness(t, fixture, secrets)
			err := (homebox.App{}).Setup(t.Context(), box.h)
			if (err == nil) != scenario.ok || (secrets.values["HOMEBOX_ACCOUNT_IDENTITY"] != "") != scenario.ok {
				t.Fatalf("unexpected statistics verification result: %v", err)
			}
		})
	}
}

func TestHomeboxCheckIsReadOnlyAndIdentityPersistenceFailuresDoNotBootstrap(t *testing.T) {
	fixture, secrets := newHomeboxFixture(), newHomeboxSecrets()
	box := newHomeboxHarness(t, fixture, secrets)
	if err := (homebox.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) || len(fixture.calls()) != 0 || len(secrets.writes) != 0 {
		t.Fatal("Check inferred or stored an unverified inventory selection")
	}
	secrets.failSet = true
	if err := (homebox.App{}).Setup(t.Context(), box.h); err == nil || secrets.values["HOMEBOX_ACCOUNT_IDENTITY"] != "" {
		t.Fatal("failed identity persistence was reported as success")
	}
	secrets.failSet = false
	if err := (homebox.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(secrets.values)
	if err := (homebox.App{}).Check(t.Context(), box.h); err != nil || !maps.Equal(before, secrets.values) || len(secrets.writes) != 1 {
		t.Fatal("Check changed a saved selection")
	}
}

func TestHomeboxAuthenticatedRedirectDoesNotForwardCredential(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Homebox verification followed an authenticated redirect")
	}))
	t.Cleanup(destination.Close)
	fixture, secrets := newHomeboxFixture(), newHomeboxSecrets()
	fixture.statuses["/api/v1/users/self"] = http.StatusFound
	fixture.redirectURL = destination.URL
	box := newHomeboxHarness(t, fixture, secrets)
	if err := (homebox.App{}).Setup(t.Context(), box.h); err == nil || len(secrets.writes) != 0 {
		t.Fatal("redirected account data was accepted")
	}
}
