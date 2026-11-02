package tandoor_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/tandoor"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type tandoorSecrets struct {
	values map[string]string
	writes int
}

func (s *tandoorSecrets) Get(key string) string { return s.values[key] }
func (s *tandoorSecrets) Set(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

type tandoorFixture struct {
	login      string
	membership string
	space      string
	statuses   map[string]int
	requests   []string
	location   string
}

func tandoorAccount(id int, username string) string {
	return fmt.Sprintf(`{"id":%d,"username":%q,"is_active":true,"is_superuser":true}`, id, username)
}

func newTandoorFixture() *tandoorFixture {
	user := tandoorAccount(17, "admin")
	return &tandoorFixture{
		login:      `{"status":200,"data":{"user":{"id":17,"username":"admin"}},"meta":{"is_authenticated":true,"session_token":"fixture-session-secret"}}`,
		membership: `[{"id":81,"user":` + user + `,"space":31,"active":true,"groups":[{"name":"admin"}]},{"id":82,"user":` + user + `,"space":44,"active":false,"groups":[{"name":"guest"}]}]`,
		space:      `{"id":31,"created_by":` + user + `,"recipe_count":0,"user_count":1}`,
		statuses:   map[string]int{},
	}
}

func (f *tandoorFixture) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected query, browser cookie or bearer authentication")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body := ""
		switch r.Method + " " + r.URL.Path {
		case "GET /api/server-settings/current/":
			body = `{"version":"2.7.2"}`
		case "POST /_allauth/app/v1/auth/login":
			var login map[string]string
			if json.NewDecoder(r.Body).Decode(&login) != nil || len(login) != 2 || login["username"] != "admin" || login["password"] != "fixture-password" {
				t.Error("unexpected administrator login")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "never-use-browser-session"})
			body = f.login
		case "GET /api/user-space/all_personal/", "GET /api/space/current/":
			if r.Header.Get("X-Session-Token") != "fixture-session-secret" {
				t.Error("missing App session token")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			body = f.membership
			if r.URL.Path == "/api/space/current/" {
				body = f.space
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		code := f.statuses[r.URL.Path]
		if code == 0 {
			code = http.StatusOK
		}
		if f.location != "" {
			w.Header().Set("Location", f.location)
		}
		w.WriteHeader(code)
		w.Write([]byte(body))
	})
}

func tandoorHarness(t *testing.T, fixture *tandoorFixture) (*helpers.H, *tandoorSecrets, *bytes.Buffer, *[]helpers.StepResult) {
	t.Helper()
	server := httptest.NewServer(fixture.handler(t))
	t.Cleanup(server.Close)
	deps := systemtest.NewDeps(t)
	secrets := &tandoorSecrets{values: map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "fixture-password",
		"TANDOOR_SECRET_KEY": "preserved-app-secret", "TANDOOR_DB_PASSWORD": "preserved-database-secret",
		"TANDOOR_HOMARR_API_KEY": "preserved-read-token", "TANDOOR_HOMARR_TOKEN_OWNERSHIP": "preserved-token-record",
	}}
	output := &bytes.Buffer{}
	results := []helpers.StepResult{}
	h := helpers.New(helpers.Config{
		App: "tandoor", Service: "tandoor", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(),
		Secrets: secrets, Log: events.New(events.JSON(output)), Now: deps.Now, Sleep: deps.Sleep,
		Record: func(result helpers.StepResult) { results = append(results, result) },
	})
	return h, secrets, output, &results
}

func TestTandoorExistingOwnerAndSpacesAreVerifiedWithoutMutation(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, output, results := tandoorHarness(t, fixture)
	before := maps.Clone(secrets.values)
	for range 2 {
		if err := (tandoor.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		if err := (tandoor.App{}).Check(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if secrets.writes != 0 || !reflect.DeepEqual(before, secrets.values) {
		t.Fatal("verification changed persisted credentials")
	}
	if len(fixture.requests) != 11 || tandoorLogins(fixture) != 1 {
		t.Fatalf("unexpected verification request count: %d", len(fixture.requests))
	}
	if (*results)[len(*results)-1].Status != status.StepSkip {
		t.Fatal("existing owner did not complete an idempotent verification step")
	}
	for _, private := range []string{"fixture-password", "fixture-session-secret", "preserved-read-token"} {
		if strings.Contains(output.String(), private) {
			t.Fatal("verification exposed private material")
		}
	}
}

func tandoorLogins(fixture *tandoorFixture) int {
	count := 0
	for _, request := range fixture.requests {
		if request == "POST /_allauth/app/v1/auth/login" {
			count++
		}
	}
	return count
}

func TestTandoorSessionReuseKeepsMetadataFreshAndIsOperationLocal(t *testing.T) {
	fixture := newTandoorFixture()
	h, _, _, _ := tandoorHarness(t, fixture)
	if err := (tandoor.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := (tandoor.App{}).Check(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if tandoorLogins(fixture) != 1 {
		t.Fatal("unchanged verification created another session")
	}
	fixture.space = strings.ReplaceAll(fixture.space, `"id":31`, `"id":44`)
	if err := (tandoor.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) || tandoorLogins(fixture) != 1 {
		t.Fatalf("cached session hid changed Space metadata: %v", err)
	}
	fresh := helpers.New(h.Config)
	if err := (tandoor.App{}).Check(t.Context(), fresh); !errors.Is(err, helpers.ErrManual) || tandoorLogins(fixture) != 2 {
		t.Fatalf("new operation did not authenticate and read current metadata: %v", err)
	}
}

func TestTandoorFailedLoginIsNotRemembered(t *testing.T) {
	fixture := newTandoorFixture()
	fixture.statuses["/_allauth/app/v1/auth/login"] = 401
	h, _, _, _ := tandoorHarness(t, fixture)
	if err := (tandoor.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	delete(fixture.statuses, "/_allauth/app/v1/auth/login")
	if err := (tandoor.App{}).Check(t.Context(), h); err != nil || tandoorLogins(fixture) != 2 {
		t.Fatalf("failed login was remembered: %v", err)
	}
}

func TestTandoorThrottleHasRetryHintInsteadOfAccountRepair(t *testing.T) {
	for _, path := range []string{"/_allauth/app/v1/auth/login", "/api/user-space/all_personal/", "/api/space/current/"} {
		t.Run(path, func(t *testing.T) {
			fixture := newTandoorFixture()
			fixture.statuses[path] = http.StatusTooManyRequests
			h, _, _, results := tandoorHarness(t, fixture)
			err := (tandoor.App{}).Setup(t.Context(), h)
			var manual *helpers.ManualError
			if !errors.As(err, &manual) || !strings.Contains(manual.Hint, "HTTP 429") || !strings.Contains(manual.Hint, "Wait at least one minute") || strings.Contains(manual.Hint, "recover the existing account") {
				t.Fatalf("incorrect throttle recovery: %v", err)
			}
			if len(*results) != 2 || (*results)[1].Hint != manual.Hint {
				t.Fatal("recorded throttle lost its retry hint")
			}
		})
	}
}

func TestTandoorOwnerAndSpaceFailuresStayExplicit(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*tandoorFixture)
		manual bool
	}{
		{"missing account", func(f *tandoorFixture) { f.statuses["/_allauth/app/v1/auth/login"] = 401 }, true},
		{"email verification", func(f *tandoorFixture) { f.statuses["/_allauth/app/v1/auth/login"] = 409 }, true},
		{"unauthenticated login", func(f *tandoorFixture) {
			f.login = strings.ReplaceAll(f.login, `"is_authenticated":true`, `"is_authenticated":false`)
		}, true},
		{"missing authentication", func(f *tandoorFixture) { f.login = strings.ReplaceAll(f.login, `"is_authenticated":true,`, ``) }, true},
		{"wrong login account", func(f *tandoorFixture) { f.login = strings.ReplaceAll(f.login, `"admin"`, `"other"`) }, true},
		{"missing login identity", func(f *tandoorFixture) { f.login = strings.ReplaceAll(f.login, `"id":17`, `"id":null`) }, true},
		{"missing login token", func(f *tandoorFixture) { f.login = strings.ReplaceAll(f.login, `"fixture-session-secret"`, `""`) }, true},
		{"malformed login", func(f *tandoorFixture) { f.login = `{"private":"secret-recipe-content",` }, false},
		{"no memberships", func(f *tandoorFixture) { f.membership = `[]` }, true},
		{"null memberships", func(f *tandoorFixture) { f.membership = `null` }, true},
		{"wrong membership owner", func(f *tandoorFixture) { f.membership = strings.ReplaceAll(f.membership, `"id":17`, `"id":18`) }, true},
		{"disabled account", func(f *tandoorFixture) {
			f.membership = strings.ReplaceAll(f.membership, `"is_active":true`, `"is_active":false`)
		}, true},
		{"nonadministrator", func(f *tandoorFixture) {
			f.membership = strings.ReplaceAll(f.membership, `"is_superuser":true`, `"is_superuser":false`)
		}, true},
		{"missing role", func(f *tandoorFixture) { f.membership = strings.ReplaceAll(f.membership, `,"is_superuser":true`, ``) }, true},
		{"no active Space", func(f *tandoorFixture) {
			f.membership = strings.ReplaceAll(f.membership, `"active":true`, `"active":false`)
		}, true},
		{"multiple active Spaces", func(f *tandoorFixture) {
			f.membership = strings.ReplaceAll(f.membership, `"active":false`, `"active":true`)
		}, true},
		{"unknown active state", func(f *tandoorFixture) {
			f.membership = strings.ReplaceAll(f.membership, `"active":true`, `"active":null`)
		}, true},
		{"guest membership", func(f *tandoorFixture) {
			f.membership = strings.ReplaceAll(f.membership, `"name":"admin"`, `"name":"guest"`)
		}, true},
		{"duplicate membership", func(f *tandoorFixture) { f.membership = strings.ReplaceAll(f.membership, `"id":82`, `"id":81`) }, true},
		{"duplicate Space", func(f *tandoorFixture) { f.membership = strings.ReplaceAll(f.membership, `"space":44`, `"space":31`) }, true},
		{"Space changed during read", func(f *tandoorFixture) { f.space = strings.ReplaceAll(f.space, `"id":31`, `"id":44`) }, true},
		{"Space has another owner", func(f *tandoorFixture) { f.space = strings.ReplaceAll(f.space, `"id":17`, `"id":18`) }, true},
		{"rejected session", func(f *tandoorFixture) { f.statuses["/api/user-space/all_personal/"] = 403 }, true},
		{"server error", func(f *tandoorFixture) { f.statuses["/api/space/current/"] = 500 }, false},
		{"malformed Space", func(f *tandoorFixture) { f.space = `{"private":"secret-recipe-content",` }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTandoorFixture()
			test.change(fixture)
			h, secrets, output, _ := tandoorHarness(t, fixture)
			err := (tandoor.App{}).Check(t.Context(), h)
			if err == nil || errors.Is(err, helpers.ErrManual) != test.manual {
				t.Fatalf("unexpected verification result: %v", err)
			}
			if secrets.writes != 0 {
				t.Fatal("failed verification changed persisted credentials")
			}
			for _, private := range []string{"secret-recipe-content", "fixture-session-secret", "fixture-password"} {
				if strings.Contains(err.Error(), private) || strings.Contains(output.String(), private) {
					t.Fatal("failure exposed response data or credentials")
				}
			}
		})
	}
}

func TestTandoorMissingOwnerRemainsManualWithoutBootstrapOrIssuance(t *testing.T) {
	fixture := newTandoorFixture()
	fixture.statuses["/_allauth/app/v1/auth/login"] = 401
	h, secrets, _, results := tandoorHarness(t, fixture)
	err := (tandoor.App{}).Setup(t.Context(), h)
	if !errors.Is(err, helpers.ErrManual) || len(*results) != 2 || (*results)[1].Status != status.StepManual {
		t.Fatalf("missing owner was not recorded as Manual: %v, %v", err, *results)
	}
	var manual *helpers.ManualError
	if !errors.As(err, &manual) || !strings.Contains(manual.Hint, "self-host setup tandoor") || secrets.writes != 0 {
		t.Fatal("Manual recovery lost its actionable hint or changed credentials")
	}
}

func TestTandoorVerificationNeverFollowsAuthenticationRedirects(t *testing.T) {
	for _, path := range []string{"/_allauth/app/v1/auth/login", "/api/user-space/all_personal/", "/api/space/current/"} {
		t.Run(path, func(t *testing.T) {
			followed := false
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
			t.Cleanup(foreign.Close)
			fixture := newTandoorFixture()
			fixture.statuses[path] = 307
			fixture.location = foreign.URL
			h, _, _, _ := tandoorHarness(t, fixture)
			if err := (tandoor.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) || followed {
				t.Fatalf("authentication redirect was not held: %v, followed=%t", err, followed)
			}
		})
	}
}

func TestTandoorMissingCredentialsNeverAttemptLogin(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, _, _ := tandoorHarness(t, fixture)
	secrets.values["ADMIN_PASSWORD"] = ""
	if err := (tandoor.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) || len(fixture.requests) != 0 || secrets.writes != 0 {
		t.Fatalf("missing credentials did not stop verification: %v", err)
	}
}

func TestTandoorComposeAndPublicMetadataMatchOfficialContract(t *testing.T) {
	a := tandoor.App{}
	i := a.Info()
	if i.ID != "tandoor" || i.Service != "tandoor" || i.Image != "docker.io/vabene1111/recipes:latest" || i.Port != 8080 || i.ContainerPort != 80 || len(i.Description) > 100 || len(a.Links()) != 0 {
		t.Fatalf("unexpected App metadata: %+v", i)
	}
	compose := a.Compose(app.RenderContext{Port: 18080, Settings: map[string]string{"EASY_NAMES": "on", "LAN_IP": "192.0.2.3"}, Inputs: map[string]string{"allowed_hosts": "recipes.example.test"}})
	for _, want := range []string{
		"${BIND_IP}:18080:80", "image: docker.io/library/postgres:16-alpine", "POSTGRES_HOST: tandoor-db",
		"DB_ENGINE: django.db.backends.postgresql", "SECRET_KEY: ${TANDOOR_SECRET_KEY}",
		"POSTGRES_PASSWORD: ${TANDOOR_DB_PASSWORD}", "./mediafiles:/opt/recipes/mediafiles",
		"./staticfiles:/opt/recipes/staticfiles", "./database:/var/lib/postgresql/data",
		"condition: service_healthy", "pg_isready -U tandoor -d tandoor", "restart: unless-stopped",
		"localhost,127.0.0.1,[::1],tandoor,${LAN_IP},tandoor.192-0-2-3.sslip.io,recipes.example.test",
		`ENABLE_SIGNUP: "0"`, `ENABLE_METRICS: "0"`, `DISABLE_EXTERNAL_CONNECTORS: "1"`, `SPACE_AI_ENABLED: "0"`,
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("missing official deployment contract %q", want)
		}
	}
	for _, forbidden := range []string{"changeme", "container_name:", "PUID", "PGID", "privileged", "docker.sock", ":5432:5432", "ADMIN_PASSWORD", "ADMIN_USER", "HOMARR_API_KEY", "REMOTE_USER_AUTH", "CORS_ALLOW_ALL_ORIGINS"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("unexpected deployment setting %q", forbidden)
		}
	}
	if strings.Count(compose, "    ports:") != 1 || strings.Count(compose, "    image:") != 2 {
		t.Fatal("unexpected public database port or extra backing services")
	}
	if secrets := a.Secrets(); len(secrets) != 2 || secrets[0].Name != "secret_key" || secrets[0].Length < 48 || secrets[1].Name != "db_password" || secrets[1].Length < 48 {
		t.Fatalf("missing first-start private secrets: %+v", secrets)
	}
	if !strings.Contains(a.StandaloneGuide(), "not available") || !strings.Contains(a.StandaloneGuide(), "ALLOWED_HOSTS") {
		t.Fatal("standalone guidance obscures the partial Setup boundary")
	}
}

func TestTandoorAdditionalHostsCannotDisableHostChecks(t *testing.T) {
	input := (tandoor.App{}).Inputs()[0]
	for _, value := range []string{"", "recipes.example.test", "recipes.example.test,192.0.2.3", "127.0.0.1", "[::1]"} {
		if err := input.Validate(value); err != nil {
			t.Errorf("valid host selection was rejected: %v", err)
		}
	}
	for _, value := range []string{"*", ".example.test", "http://recipes.example.test", "recipes.example.test:80", "recipes.example.test\nOTHER=1", "recipes.example.test,,other.example.test", "[[::1]]", "[127.0.0.1]", "::1"} {
		if err := input.Validate(value); err == nil {
			t.Errorf("unsafe host selection was accepted: %q", value)
		}
	}
}
