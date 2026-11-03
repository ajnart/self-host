package thingport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/thingport"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type secrets map[string]string

func (s secrets) Get(key string) string       { return s[key] }
func (s secrets) Set(key, value string) error { s[key] = value; return nil }

type fakeThingport struct {
	mu          sync.Mutex
	loginStatus int
	loginBody   string
	usersStatus int
	usersBody   string
	summary     string
	summaryCode int
	requests    []string
}

func (f *fakeThingport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET /api/health":
		fmt.Fprint(w, `{"ok":true,"auth_required":true,"allow_registrations":true}`)
	case "POST /api/login":
		var body struct{ Email, Password string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Email != "owner@example.test" || body.Password != "synthetic-password" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.loginStatus != 0 {
			w.WriteHeader(f.loginStatus)
		}
		if f.loginBody != "" {
			fmt.Fprint(w, f.loginBody)
			return
		}
		fmt.Fprint(w, `{"token":"synthetic-session","user":{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN"}}`)
	case "GET /api/admin/users":
		if r.Header.Get("Authorization") != "Bearer synthetic-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.usersStatus != 0 {
			w.WriteHeader(f.usersStatus)
		}
		if f.usersBody != "" {
			fmt.Fprint(w, f.usersBody)
			return
		}
		fmt.Fprint(w, `[{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN","is_owner":true},{"id":"fixture-member","email":"member@example.test","role":"MEMBER","is_owner":false}]`)
	case "GET /api/dashboard/summary":
		if r.Header.Get("Authorization") != "Bearer dashboard-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.summaryCode != 0 {
			w.WriteHeader(f.summaryCode)
		}
		if f.summary != "" {
			fmt.Fprint(w, f.summary)
			return
		}
		fmt.Fprint(w, `{"model_count":0,"collection_count":0,"category_count":82,"author_count":0}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type harness struct {
	h       *helpers.H
	secrets secrets
	steps   []helpers.StepResult
	log     []events.Event
}

func newHarness(t *testing.T, fake *fakeThingport, saved secrets) *harness {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	deps := systemtest.NewDeps(t)
	box := &harness{secrets: saved}
	box.h = helpers.New(helpers.Config{
		App: "thingport", Service: "thingport", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(),
		Inputs: map[string]string{"email": "owner@example.test"}, Secrets: saved,
		Now: deps.Now, Sleep: deps.Sleep,
		Log:    events.New(events.SinkFunc(func(event events.Event) { box.log = append(box.log, event) })),
		Record: func(result helpers.StepResult) { box.steps = append(box.steps, result) },
	})
	return box
}

func ownerSecrets() secrets {
	return secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password"}
}

func TestComposeUsesOfficialMinimalServicesAndPrivateBackendAlias(t *testing.T) {
	a := thingport.App{}
	info := a.Info()
	if info.ID != "thingport" || info.Service != "thingport" || info.ContainerPort != 80 || !info.LongTimeouts || len(info.Description) > 100 {
		t.Fatal("incorrect App metadata")
	}
	compose := a.Compose(app.RenderContext{Port: 49181})
	for _, required := range []string{
		"image: ghcr.io/tautvydasderzinskas/thingport-frontend:latest",
		"image: ghcr.io/tautvydasderzinskas/thingport-backend:latest",
		"image: docker.io/library/postgres:16-alpine",
		"${BIND_IP}:49181:80",
		"./database:/var/lib/postgresql/data", "./storage:/app/storage",
		"AUTH_SECRET: ${THINGPORT_AUTH_SECRET}", "POSTGRES_PASSWORD: ${THINGPORT_DB_PASSWORD}",
		"DATABASE_URL: postgresql://thingport:${THINGPORT_DB_PASSWORD}@thingport-database:5432/thingport?schema=public",
		"condition: service_healthy", "aliases:\n          - backend",
		"CHOWN_MODE: minimal", "COLLECTION_SYNC_ENABLED: \"false\"",
		"SYSTEM_NOTIFICATIONS_URL: \"\"", "RELEASE_NOTES_URL: \"\"", "FLARESOLVERR_URL: \"\"",
		"networks:\n  thingport-private:",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("missing deployment contract %q", required)
		}
	}
	if strings.Count(compose, "    image:") != 3 || strings.Count(compose, "    ports:") != 1 {
		t.Fatal("unexpected service or published backend port")
	}
	for _, forbidden := range []string{"flaresolverr:latest", "container_name:", "privileged:", "docker.sock", "/app/consume", "CHOWN_MODE: recursive", "ADMIN_PASSWORD", "ADMIN_USER"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("unexpected deployment grant %q", forbidden)
		}
	}
	backend := strings.Split(strings.Split(compose, "\n  thingport-backend:\n")[1], "\n  thingport-database:\n")[0]
	if strings.Contains(backend, "default:") {
		t.Fatal("backend alias is exposed to the Project-wide network")
	}
	if got := a.Secrets(); len(got) != 2 || got[0].Name != "auth_secret" || got[1].Name != "db_password" || !got[0].Hex || !got[1].Hex {
		t.Fatal("first-start secrets are not narrowly declared")
	}
	if len(a.Links()) != 0 {
		t.Fatal("the peer must not own Homarr's Link")
	}
}

func TestOwnerEmailRequiresAnExplicitPlainEmail(t *testing.T) {
	inputs := (thingport.App{}).Inputs()
	if len(inputs) != 2 || inputs[1].Key != "homarr_api_key" || !inputs[1].Private || inputs[0].Key != "email" || !inputs[0].Required || inputs[0].Default != "" || inputs[0].Validate == nil {
		t.Fatal("owner selection must be explicit")
	}
	for _, invalid := range []string{"", "admin", "Name <owner@example.test>", " owner@example.test", "owner@example.test\n"} {
		if inputs[0].Validate(invalid) == nil {
			t.Errorf("accepted invalid owner selection %q", invalid)
		}
	}
	if err := inputs[0].Validate("owner@example.test"); err != nil {
		t.Fatal(err)
	}
}

func TestSetupBindsVerifiedOwnerAndRerunPreservesCredentials(t *testing.T) {
	fake := &fakeThingport{}
	saved := ownerSecrets()
	saved["THINGPORT_HOMARR_API_KEY"] = "tp_synthetic-existing-key"
	saved["THINGPORT_AUTH_SECRET"] = "synthetic-signing-secret"
	saved["THINGPORT_DB_PASSWORD"] = "synthetic-database-password"
	a := thingport.App{}
	for range 2 {
		box := newHarness(t, fake, saved)
		if err := a.Setup(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
		if err := a.Check(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmt.Sprint(box.steps, box.log), "synthetic-session") {
			t.Fatal("a session reached diagnostics")
		}
	}
	want := ownerSecrets()
	want["THINGPORT_HOMARR_API_KEY"] = "tp_synthetic-existing-key"
	want["THINGPORT_AUTH_SECRET"] = "synthetic-signing-secret"
	want["THINGPORT_DB_PASSWORD"] = "synthetic-database-password"
	want["THINGPORT_OWNER_ID"] = "fixture-owner"
	if !reflect.DeepEqual(saved, want) {
		t.Fatal("Setup replaced an existing credential or owner")
	}
	logins := 0
	for _, request := range fake.requests {
		if request == "POST /api/login" {
			logins++
			continue
		}
		if request != "GET /api/health" && request != "GET /api/admin/users" {
			t.Fatalf("unexpected App request %s", request)
		}
	}
	if logins != 2 {
		t.Fatalf("got %d logins; reuse the session only within each Setup/Check invocation", logins)
	}
}

func TestUnprovedOwnerRemainsManualWithoutRegistrationOrTokenMutation(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		fake  *fakeThingport
		owner string
	}{
		{name: "fresh or wrong password", fake: &fakeThingport{loginStatus: 401, loginBody: `{"detail":"synthetic-password"}`}},
		{name: "captcha or verification", fake: &fakeThingport{loginStatus: 403}},
		{name: "member", fake: &fakeThingport{loginBody: `{"token":"synthetic-session","user":{"id":"fixture-owner","email":"owner@example.test","role":"MEMBER"}}`}},
		{name: "different email", fake: &fakeThingport{loginBody: `{"token":"synthetic-session","user":{"id":"fixture-owner","email":"different@example.test","role":"ADMIN"}}`}},
		{name: "nonowner admin", fake: &fakeThingport{usersBody: `[{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN","is_owner":false}]`}},
		{name: "owner flag missing", fake: &fakeThingport{usersBody: `[{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN"}]`}},
		{name: "foreign inventory identity", fake: &fakeThingport{usersBody: `[{"id":"different-owner","email":"owner@example.test","role":"ADMIN","is_owner":true}]`}},
		{name: "multiple owners", fake: &fakeThingport{usersBody: `[{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN","is_owner":true},{"id":"other-owner","is_owner":true}]`}},
		{name: "duplicate selected identity", fake: &fakeThingport{usersBody: `[{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN","is_owner":true},{"id":"fixture-owner","is_owner":false}]`}},
		{name: "changed owner", fake: &fakeThingport{}, owner: "recorded-owner"},
		{name: "session no longer authorized", fake: &fakeThingport{usersStatus: 403}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			saved := ownerSecrets()
			saved["THINGPORT_OWNER_ID"] = scenario.owner
			saved["THINGPORT_HOMARR_API_KEY"] = "tp_saved-key"
			before := fmt.Sprint(saved)
			box := newHarness(t, scenario.fake, saved)
			err := (thingport.App{}).Setup(t.Context(), box.h)
			if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) {
				t.Fatalf("expected an actionable Manual boundary, got %v", err)
			}
			if fmt.Sprint(saved) != before || strings.Contains(fmt.Sprint(err, box.steps, box.log), "synthetic-password") {
				t.Fatal("Manual recovery changed credentials or exposed a response")
			}
			for _, request := range scenario.fake.requests {
				if request != "GET /api/health" && request != "POST /api/login" && request != "GET /api/admin/users" {
					t.Fatalf("unsafe request during Manual recovery: %s", request)
				}
			}
			last := box.steps[len(box.steps)-1]
			if last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, "self-host setup thingport") {
				t.Fatal("Manual step lost its recovery hint")
			}
		})
	}
}

func TestCheckDoesNotBindAnUnrecordedOwner(t *testing.T) {
	box := newHarness(t, &fakeThingport{}, ownerSecrets())
	if err := (thingport.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unrecorded owner Check = %v", err)
	}
	if box.secrets["THINGPORT_OWNER_ID"] != "" {
		t.Fatal("read-only Check persisted owner identity")
	}
}

func TestCheckRevalidatesOwnerInsteadOfTrustingCachedLogin(t *testing.T) {
	fake := &fakeThingport{}
	box := newHarness(t, fake, ownerSecrets())
	a := thingport.App{}
	if err := a.Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.usersBody = `[{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN","is_owner":false}]`
	fake.mu.Unlock()
	if err := a.Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("changed account inventory was accepted: %v", err)
	}
	if box.secrets["THINGPORT_OWNER_ID"] != "fixture-owner" {
		t.Fatal("Check replaced the recorded owner")
	}
}

func TestHomarrPrerequisiteWaitsForReviewedTokenHelper(t *testing.T) {
	fake := &fakeThingport{}
	box := newHarness(t, fake, ownerSecrets())
	box.secrets["THINGPORT_HOMARR_API_KEY"] = "tp_unattributed-existing-key"
	box.h.Peer = func(id string) *helpers.H {
		if id == "homarr" {
			return helpers.New(helpers.Config{App: "homarr", Log: events.Discard()})
		}
		return nil
	}
	err := (thingport.App{}).Setup(t.Context(), box.h)
	if !errors.Is(err, helpers.ErrManual) || box.secrets["THINGPORT_OWNER_ID"] != "fixture-owner" {
		t.Fatalf("expected verified owner with separate token hold: %v", err)
	}
	last := box.steps[len(box.steps)-1]
	if last.ID != "homarr-token" || last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, "one API token") {
		t.Fatal("single-token prerequisite is not explicit")
	}
	for _, request := range fake.requests {
		if strings.Contains(request, "api-token") || strings.Contains(request, "dashboard") {
			t.Fatal("unreviewed token issuance or count verification was invoked")
		}
	}
	if box.secrets["THINGPORT_HOMARR_API_KEY"] != "tp_unattributed-existing-key" {
		t.Fatal("existing token changed")
	}
}

func TestMalformedSuccessfulResponsesDoNotEstablishOwner(t *testing.T) {
	for _, fake := range []*fakeThingport{
		{loginBody: `{"token":"synthetic-session"}`},
		{loginBody: `{"token":"synthetic-session","user":{"id":null,"email":"owner@example.test","role":"ADMIN"}}`},
		{loginBody: `{"token":null,"user":{"id":"fixture-owner","email":"owner@example.test","role":"ADMIN"}}`},
		{loginBody: `not-json synthetic-session`},
		{usersBody: `null`},
		{usersBody: `{}`},
		{usersBody: `[]`},
	} {
		box := newHarness(t, fake, ownerSecrets())
		if err := (thingport.App{}).Setup(context.Background(), box.h); err == nil {
			t.Fatal("malformed successful response established ownership")
		}
		if box.secrets["THINGPORT_OWNER_ID"] != "" || strings.Contains(fmt.Sprint(box.steps, box.log), "synthetic-session") {
			t.Fatal("malformed response changed ownership or escaped diagnostics")
		}
	}
}

func TestVerifyHomarrRequiresKnownTokenAndExplicitCounts(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		key     string
		fake    *fakeThingport
		wantErr bool
		manual  bool
	}{
		{name: "explicit zeros", key: "dashboard-token", fake: &fakeThingport{}},
		{name: "no saved token", fake: &fakeThingport{}, manual: true, wantErr: true},
		{name: "rejected token", key: "other-token", fake: &fakeThingport{}, manual: true, wantErr: true},
		{name: "forbidden scope", key: "dashboard-token", fake: &fakeThingport{summaryCode: http.StatusForbidden}, manual: true, wantErr: true},
		{name: "missing count", key: "dashboard-token", fake: &fakeThingport{summary: `{"model_count":0,"collection_count":0,"category_count":82}`}, wantErr: true},
		{name: "null count", key: "dashboard-token", fake: &fakeThingport{summary: `{"model_count":null,"collection_count":0,"category_count":82,"author_count":0}`}, wantErr: true},
		{name: "negative count", key: "dashboard-token", fake: &fakeThingport{summary: `{"model_count":-1,"collection_count":0,"category_count":82,"author_count":0}`}, wantErr: true},
		{name: "textual count", key: "dashboard-token", fake: &fakeThingport{summary: `{"model_count":"0","collection_count":0,"category_count":82,"author_count":0}`}, wantErr: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			saved := ownerSecrets()
			saved["THINGPORT_OWNER_ID"] = "fixture-owner"
			if scenario.key != "" {
				saved["THINGPORT_HOMARR_API_KEY"] = scenario.key
			}
			box := newHarness(t, scenario.fake, saved)
			err := thingport.VerifyHomarr(context.Background(), box.h)
			if (err != nil) != scenario.wantErr || scenario.manual != errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}
