package miniflux_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
	"go.yaml.in/yaml/v3"
)

var (
	_ app.App             = miniflux.App{}
	_ app.SecretDeclarer  = miniflux.App{}
	_ app.StandaloneGuide = miniflux.App{}
)

type privateValues struct {
	values map[string]string
	writes int
	fail   bool
}

func (s *privateValues) Get(key string) string { return s.values[key] }

func (s *privateValues) Set(key, value string) error {
	if s.fail {
		return errors.New("private store unavailable")
	}
	s.writes++
	s.values[key] = value
	return nil
}

type readerFixture struct {
	h          *helpers.H
	secrets    *privateValues
	steps      []helpers.StepResult
	events     []events.Event
	requests   []string
	identity   string
	feeds      string
	meStatus   int
	feedStatus int
	redirect   string
}

func newReaderFixture(t *testing.T) *readerFixture {
	t.Helper()
	f := &readerFixture{
		identity:   `{"id":27,"username":"admin","is_admin":true}`,
		feeds:      `[{"id":41,"user_id":27,"title":"Existing feed","feed_url":"https://feed.invalid/atom"}]`,
		meStatus:   http.StatusOK,
		feedStatus: http.StatusOK,
		secrets: &privateValues{values: map[string]string{
			"ADMIN_USER": "admin", "ADMIN_PASSWORD": "existing-private-password",
			"MINIFLUX_DB_PASSWORD": "existing-database-password",
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			t.Errorf("unexpected mutating or parameterized request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/healthcheck" {
			w.Write([]byte("OK"))
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "admin" || password != "existing-private-password" {
			t.Errorf("reader request did not use the existing Admin login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/me":
			if f.redirect != "" {
				w.Header().Set("Location", f.redirect)
			}
			w.WriteHeader(f.meStatus)
			w.Write([]byte(f.identity))
		case "/v1/feeds":
			w.WriteHeader(f.feedStatus)
			w.Write([]byte(f.feeds))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	f.h = helpers.New(helpers.Config{
		App: "miniflux", Service: "miniflux", ContainerPort: 8080,
		BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(), Secrets: f.secrets,
		Log: events.New(events.SinkFunc(func(event events.Event) { f.events = append(f.events, event) })),
		Now: deps.Now, Sleep: deps.Sleep,
		Record: func(result helpers.StepResult) { f.steps = append(f.steps, result) },
	})
	return f
}

func TestOfficialComposeAndAppContract(t *testing.T) {
	a := miniflux.App{}
	info := a.Info()
	if info.ID != "miniflux" || info.Service != "miniflux" || info.Category != "Productivity" || info.Port != 8080 || info.ContainerPort != 8080 || info.ContainerPortIsPort || info.UsesMedia || info.Image != "docker.io/miniflux/miniflux:latest" {
		t.Fatalf("unexpected App metadata: %+v", info)
	}
	inputs := a.Inputs()
	if len(inputs) != 1 || inputs[0].Key != "reader_id" || !inputs[0].Private || !inputs[0].RecoveryOnly || inputs[0].Required || a.Links() != nil || !reflect.DeepEqual(a.Secrets(), []app.SecretSpec{{Name: "db_password", Length: 48}}) {
		t.Fatal("App-only slice introduced non-recovery inputs, Links or unexpected generated credentials")
	}
	type healthcheck struct {
		Test        []string
		Interval    string
		Timeout     string
		StartPeriod string `yaml:"start_period"`
		Retries     int
	}
	type composeService struct {
		Image         string
		Restart       string
		Environment   map[string]string
		Ports         []string
		Volumes       []string
		User          string
		Privileged    bool
		ContainerName string                                `yaml:"container_name"`
		DependsOn     map[string]struct{ Condition string } `yaml:"depends_on"`
		Healthcheck   healthcheck
	}
	var compose struct {
		Services map[string]composeService
		Volumes  map[string]any
	}
	if err := yaml.Unmarshal([]byte(a.Compose(app.RenderContext{Port: 18080})), &compose); err != nil {
		t.Fatal(err)
	}
	if len(compose.Services) != 2 || len(compose.Volumes) != 0 {
		t.Fatalf("unexpected persistent service boundary: %+v", compose)
	}
	main := compose.Services["miniflux"]
	if main.Image != info.Image || !reflect.DeepEqual(main.Ports, []string{"${BIND_IP}:18080:8080"}) || len(main.Volumes) != 0 || main.DependsOn["miniflux-postgres"].Condition != "service_healthy" {
		t.Fatalf("unexpected Miniflux definition: %+v", main)
	}
	if !reflect.DeepEqual(main.Environment, map[string]string{
		"DATABASE_URL":   "postgres://miniflux:${MINIFLUX_DB_PASSWORD}@miniflux-postgres:5432/miniflux?sslmode=disable",
		"RUN_MIGRATIONS": "1", "CREATE_ADMIN": "0", "DISABLE_SCHEDULER_SERVICE": "1", "TZ": "${TZ}",
	}) {
		t.Fatalf("unsafe or incomplete startup settings: %+v", main.Environment)
	}
	db := compose.Services["miniflux-postgres"]
	if db.Image != "docker.io/library/postgres:18" || len(db.Ports) != 0 || !reflect.DeepEqual(db.Volumes, []string{"./database:/var/lib/postgresql"}) || !reflect.DeepEqual(db.Environment, map[string]string{
		"POSTGRES_USER": "miniflux", "POSTGRES_DB": "miniflux", "POSTGRES_PASSWORD": "${MINIFLUX_DB_PASSWORD}",
	}) {
		t.Fatalf("unexpected PostgreSQL definition: %+v", db)
	}
	for name, service := range compose.Services {
		if service.Restart != "unless-stopped" || service.User != "" || service.Privileged || service.ContainerName != "" || service.Healthcheck.Interval == "" || service.Healthcheck.Timeout == "" || service.Healthcheck.StartPeriod == "" || service.Healthcheck.Retries <= 0 {
			t.Errorf("unsafe identity, lifecycle or readiness settings for %s: %+v", name, service)
		}
	}
	if !reflect.DeepEqual(main.Healthcheck.Test, []string{"CMD", "/usr/bin/miniflux", "-healthcheck", "auto"}) || !reflect.DeepEqual(db.Healthcheck.Test, []string{"CMD", "pg_isready", "-U", "miniflux", "-d", "miniflux"}) {
		t.Fatal("healthchecks do not use the supported native commands")
	}
	guide := a.StandaloneGuide()
	for _, required := range []string{"independently confirming", "-create-admin", "real terminal", "DISABLE_SCHEDULER_SERVICE", "review and approve"} {
		if !strings.Contains(guide, required) {
			t.Errorf("standalone guide omitted %q", required)
		}
	}
}

func TestExistingReaderSetupCheckAndRerunPreserveIdentity(t *testing.T) {
	for _, feeds := range []string{"[]", `[{"id":41,"user_id":27},{"id":43,"user_id":27}]`} {
		t.Run(feeds, func(t *testing.T) {
			f := newReaderFixture(t)
			f.feeds = feeds
			before := map[string]string{}
			for key, value := range f.secrets.values {
				before[key] = value
			}
			if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			if f.secrets.values["MINIFLUX_READER_ID"] != "27" || f.secrets.writes != 1 {
				t.Fatalf("unexpected reader recording: %+v", f.secrets)
			}
			if err := (miniflux.App{}).Check(t.Context(), f.h); err != nil {
				t.Fatalf("Check: %v", err)
			}
			f.steps = nil
			if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil {
				t.Fatalf("rerun Setup: %v", err)
			}
			if f.secrets.writes != 1 || f.feeds != feeds {
				t.Fatal("rerun rewrote identity or feed state")
			}
			for _, step := range f.steps {
				if step.Status != stepstatus.StepSkip {
					t.Errorf("rerun did not skip %s: %s", step.ID, step.Status)
				}
			}
			for key, value := range before {
				if f.secrets.values[key] != value {
					t.Errorf("changed existing credential %s", key)
				}
			}
		})
	}
}

func TestUncertainBootstrapAndIdentityStayManualWithoutWrites(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*readerFixture)
	}{
		{name: "no username", change: func(f *readerFixture) { f.secrets.values["ADMIN_USER"] = "" }},
		{name: "no password", change: func(f *readerFixture) { f.secrets.values["ADMIN_PASSWORD"] = "" }},
		{name: "unauthenticated", change: func(f *readerFixture) { f.meStatus = 401 }},
		{name: "forbidden", change: func(f *readerFixture) { f.meStatus = 403 }},
		{name: "different reader", change: func(f *readerFixture) { f.identity = `{"id":27,"username":"another","is_admin":true}` }},
		{name: "not administrator", change: func(f *readerFixture) { f.identity = `{"id":27,"username":"admin","is_admin":false}` }},
		{name: "replacement account", change: func(f *readerFixture) { f.secrets.values["MINIFLUX_READER_ID"] = "26" }},
		{name: "malformed record", change: func(f *readerFixture) { f.secrets.values["MINIFLUX_READER_ID"] = "unknown" }},
		{name: "noncanonical record", change: func(f *readerFixture) { f.secrets.values["MINIFLUX_READER_ID"] = "027" }},
		{name: "feed access denied", change: func(f *readerFixture) { f.feedStatus = 403 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newReaderFixture(t)
			scenario.change(f)
			saved := f.secrets.values["MINIFLUX_READER_ID"]
			for range 2 {
				err := (miniflux.App{}).Setup(t.Context(), f.h)
				var manual *helpers.ManualError
				if !errors.Is(err, helpers.ErrManual) || !errors.As(err, &manual) || !strings.Contains(manual.Hint, "self-host setup miniflux") {
					t.Fatalf("expected actionable Manual recovery, got %v", err)
				}
			}
			if f.secrets.writes != 0 || f.secrets.values["MINIFLUX_READER_ID"] != saved {
				t.Fatal("uncertain bootstrap or identity rewrote private records")
			}
			last := f.steps[len(f.steps)-1]
			if last.Status != stepstatus.StepManual || last.ID != "reader" || !strings.Contains(last.Hint, "self-host setup miniflux") {
				t.Fatalf("Manual recovery was not recorded: %+v", last)
			}
			if err := (miniflux.App{}).Check(t.Context(), f.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("Check lost the Manual boundary: %v", err)
			}
		})
	}
}

func TestMalformedIdentityAndFeedDataCannotCompleteSetup(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		identity string
		feeds    string
	}{
		{name: "missing role", identity: `{"id":27,"username":"admin"}`},
		{name: "null role", identity: `{"id":27,"username":"admin","is_admin":null}`},
		{name: "missing identity", identity: `null`},
		{name: "nonpositive ID", identity: `{"id":0,"username":"admin","is_admin":true}`},
		{name: "fractional ID", identity: `{"id":27.5,"username":"admin","is_admin":true}`},
		{name: "missing feed list", feeds: `null`},
		{name: "object feed list", feeds: `{}`},
		{name: "missing feed ID", feeds: `[{"user_id":27}]`},
		{name: "missing feed owner", feeds: `[{"id":41}]`},
		{name: "another reader's feeds", feeds: `[{"id":41,"user_id":28}]`},
		{name: "duplicate feed IDs", feeds: `[{"id":41,"user_id":27},{"id":41,"user_id":27}]`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newReaderFixture(t)
			if scenario.identity != "" {
				f.identity = scenario.identity
			}
			if scenario.feeds != "" {
				f.feeds = scenario.feeds
			}
			if err := (miniflux.App{}).Setup(t.Context(), f.h); err == nil || errors.Is(err, helpers.ErrManual) {
				t.Fatalf("malformed data should fail without becoming bootstrap authority: %v", err)
			}
			if f.secrets.writes != 0 || f.secrets.values["MINIFLUX_READER_ID"] != "" {
				t.Fatal("malformed data recorded reader identity")
			}
		})
	}
}

func TestCheckDoesNotWriteMissingIdentityAndRejectsReplacement(t *testing.T) {
	f := newReaderFixture(t)
	if err := (miniflux.App{}).Check(t.Context(), f.h); !errors.Is(err, helpers.ErrManual) || f.secrets.writes != 0 {
		t.Fatalf("Check must require Setup to record identity without writing: %v", err)
	}
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil {
		t.Fatal(err)
	}
	f.identity = `{"id":99,"username":"admin","is_admin":true}`
	if err := (miniflux.App{}).Check(t.Context(), f.h); !errors.Is(err, helpers.ErrManual) || f.secrets.writes != 1 || f.secrets.values["MINIFLUX_READER_ID"] != "27" {
		t.Fatalf("Check silently selected a replacement reader: %v", err)
	}
}

func TestFailedIdentityPersistenceDoesNotCompleteOrMutateRemoteState(t *testing.T) {
	f := newReaderFixture(t)
	f.secrets.fail = true
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err == nil || f.secrets.writes != 0 {
		t.Fatalf("failed identity persistence completed Setup: %v", err)
	}
	if err := (miniflux.App{}).Check(t.Context(), f.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unrecorded identity completed Check: %v", err)
	}
	f.secrets.fail = false
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil || f.secrets.writes != 1 {
		t.Fatalf("read-only retry did not record existing identity: %v", err)
	}
}

func TestAuthenticatedRedirectIsNotFollowed(t *testing.T) {
	redirected := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(destination.Close)
	f := newReaderFixture(t)
	f.meStatus, f.redirect = http.StatusFound, destination.URL
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err == nil || redirected != 0 || f.secrets.writes != 0 {
		t.Fatalf("authenticated redirect was followed or accepted: %v, requests %d", err, redirected)
	}
}

func TestResponseBodiesNeverEnterDiagnostics(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			f := newReaderFixture(t)
			f.meStatus = code
			f.identity = `{"id":"unregistered-response-secret","password":"existing-private-password"}`
			err := (miniflux.App{}).Setup(t.Context(), f.h)
			if err == nil {
				t.Fatal("unexpected success")
			}
			diagnostic, marshalErr := json.Marshal([]any{f.steps, f.events, err.Error()})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{"unregistered-response-secret", "existing-private-password", "existing-database-password"} {
				if strings.Contains(string(diagnostic), forbidden) {
					t.Fatalf("diagnostics leaked %q", forbidden)
				}
			}
		})
	}
}
