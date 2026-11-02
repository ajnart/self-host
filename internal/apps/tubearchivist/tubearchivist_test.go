package tubearchivist_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/tubearchivist"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
	"go.yaml.in/yaml/v3"
)

type tubeArchivistSecrets map[string]string

func (s tubeArchivistSecrets) Get(key string) string { return s[key] }
func (s tubeArchivistSecrets) Set(key, value string) error {
	s[key] = value
	return nil
}

type tubeArchivistService struct {
	Image       string            `yaml:"image"`
	User        string            `yaml:"user"`
	Restart     string            `yaml:"restart"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Command     []string          `yaml:"command"`
	DependsOn   map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
	Healthcheck struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

func tubeArchivistCompose(t *testing.T, rc app.RenderContext) map[string]tubeArchivistService {
	t.Helper()
	var document struct {
		Services map[string]tubeArchivistService `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte((tubearchivist.App{}).Compose(rc)), &document); err != nil {
		t.Fatal(err)
	}
	return document.Services
}

func TestTubeArchivistOfficialServicesAndPersistence(t *testing.T) {
	a := tubearchivist.App{}
	info := a.Info()
	if info.ID != "tubearchivist" || info.Service != info.ID || info.Port != 8000 || info.ContainerPort != 8000 || !info.UsesMedia || len(info.Description) > 100 {
		t.Fatal("incomplete App metadata")
	}
	if len(a.Inputs()) != 0 || len(a.Links()) != 0 {
		t.Fatal("App-only slice introduced unrelated inputs or Links")
	}
	rc := app.RenderContext{Port: 18000, Settings: map[string]string{"EXPOSURE": "local"}}
	services := tubeArchivistCompose(t, rc)
	wantImages := map[string]string{
		"tubearchivist":       "bbilly1/tubearchivist:latest",
		"tubearchivist-redis": "redis:8",
		"tubearchivist-es":    "docker.elastic.co/elasticsearch/elasticsearch:9.5.5",
	}
	if len(services) != len(wantImages) {
		t.Fatal("required backing services were omitted or extra services were added")
	}
	for id, image := range wantImages {
		svc := services[id]
		if svc.Image != image || svc.Restart != "unless-stopped" {
			t.Errorf("%s image or restart contract changed", id)
		}
		if id != "tubearchivist" && len(svc.Ports) != 0 {
			t.Errorf("%s publishes a backing service", id)
		}
	}
	main := services["tubearchivist"]
	if !reflect.DeepEqual(main.Ports, []string{"${BIND_IP}:18000:8000"}) || main.User != "${PUID}:${PGID}" {
		t.Fatal("selected Access, allocated host port or supported identity was lost")
	}
	wantMounts := []string{
		"./cache:/cache", "${MEDIA_ROOT}/tubearchivist:/youtube",
		"./secrets/admin_password:/run/secrets/admin_password:ro",
		"./secrets/elastic_password:/run/secrets/elastic_password:ro",
	}
	if !reflect.DeepEqual(main.Volumes, wantMounts) {
		t.Fatal("archive, account or credential mount changed")
	}
	for key, want := range map[string]string{
		"TA_USERNAME": "${ADMIN_USER}", "TA_PASSWORD_FILE": "/run/secrets/admin_password",
		"ES_URL": "http://tubearchivist-es:9200", "REDIS_CON": "redis://tubearchivist-redis:6379",
		"ELASTIC_PASSWORD_FILE": "/run/secrets/elastic_password", "TZ": "${TZ}",
	} {
		if main.Environment[key] != want {
			t.Errorf("%s = %q, want %q", key, main.Environment[key], want)
		}
	}
	if main.DependsOn["tubearchivist-redis"].Condition != "service_healthy" || main.DependsOn["tubearchivist-es"].Condition != "service_started" {
		t.Fatal("backing service readiness was lost")
	}
	redis, elastic := services["tubearchivist-redis"], services["tubearchivist-es"]
	if !reflect.DeepEqual(redis.Volumes, []string{"./redis:/data"}) || !reflect.DeepEqual(redis.Command, []string{"redis-server", "--appendonly", "yes"}) {
		t.Fatal("dedicated persistent Redis contract changed")
	}
	if elastic.User != "${PUID}:0" || !slices.Contains(elastic.Volumes, "./elasticsearch:/usr/share/elasticsearch/data") || !slices.Contains(elastic.Volumes, "./secrets/elastic_password:/run/secrets/elastic_password:ro") {
		t.Fatal("supported Elasticsearch identity or persistent data changed")
	}
	for key, want := range map[string]string{
		"xpack.security.enabled": "true", "discovery.type": "single-node",
		"ES_JAVA_OPTS": "-Xms1g -Xmx1g", "path.repo": "/usr/share/elasticsearch/data/snapshot",
		"ELASTIC_PASSWORD_FILE": "/run/secrets/elastic_password",
	} {
		if elastic.Environment[key] != want {
			t.Errorf("Elasticsearch %s changed", key)
		}
	}
	for _, key := range []string{"DISABLE_STATIC_AUTH", "TA_AUTO_UPDATE_YTDLP", "DJANGO_DEBUG", "ES_DISABLE_VERIFY_SSL", "TA_ENABLE_AUTH_PROXY", "TA_LDAP", "HOST_UID", "HOST_GID", "TA_BACKEND_PORT"} {
		if _, exists := main.Environment[key]; exists {
			t.Errorf("unexpected authentication, migration or outbound setting %s", key)
		}
	}
	var raw struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(a.Compose(rc)), &raw); err != nil {
		t.Fatal(err)
	}
	for id, svc := range raw.Services {
		for _, key := range []string{"container_name", "privileged", "cap_add", "devices", "sysctls", "network_mode", "ulimits"} {
			if _, exists := svc[key]; exists {
				t.Errorf("%s added a runtime bypass: %s", id, key)
			}
		}
	}
	updated := tubeArchivistCompose(t, app.RenderContext{Port: 28000, Settings: map[string]string{"MEDIA_LAYOUT": "shared"}})
	for id, svc := range services {
		if !reflect.DeepEqual(svc.Volumes, updated[id].Volumes) {
			t.Errorf("%s storage changed with host port or layout", id)
		}
	}
}

func TestTubeArchivistOriginsFollowAccessWithoutWildcards(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     []string
	}{
		{"local", map[string]string{"EXPOSURE": "local"}, []string{"http://127.0.0.1:18000", "http://localhost:18000", "http://tubearchivist:8000"}},
		{"home", map[string]string{"EXPOSURE": "lan"}, []string{"http://127.0.0.1:18000", "http://localhost:18000", "http://tubearchivist:8000", "http://${LAN_IP}:18000"}},
		{"names", map[string]string{"EXPOSURE": "lan", "EASY_NAMES": "on", "LAN_IP": "192.0.2.12"}, []string{"http://127.0.0.1:18000", "http://localhost:18000", "http://tubearchivist:8000", "http://${LAN_IP}:18000", "http://tubearchivist.192-0-2-12.sslip.io"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main := tubeArchivistCompose(t, app.RenderContext{Port: 18000, Settings: tc.settings})["tubearchivist"]
			if got := strings.Fields(main.Environment["TA_HOST"]); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("origins = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTubeArchivistPrivateFilesKeepExistingCredentialsAndData(t *testing.T) {
	a := tubearchivist.App{}
	if got := a.Secrets(); len(got) != 1 || got[0].Name != "elastic_password" || got[0].Length < 32 || !got[0].Hex {
		t.Fatal("missing private Elasticsearch secret declaration")
	}
	files := a.Files(app.RenderContext{}, func(name string) string { return "synthetic-" + name })
	if len(files) != 5 {
		t.Fatal("unexpected persistent configuration files")
	}
	want := map[string]string{
		"cache/.keep": "", "redis/.keep": "", "elasticsearch/.keep": "",
		"secrets/admin_password": "synthetic-ADMIN_PASSWORD\n", "secrets/elastic_password": "synthetic-elastic_password\n",
	}
	for _, file := range files {
		content, found := want[file.Path]
		if !found || file.Content != content || file.Mode != 0o600 || !file.Keep {
			t.Fatalf("file %s does not preserve private data or credentials", file.Path)
		}
		delete(want, file.Path)
	}
	if len(want) != 0 {
		t.Fatal("persistent files omitted")
	}
}

func TestTubeArchivistStartupPrerequisitesCannotBeAcknowledgedAway(t *testing.T) {
	for _, inputs := range []map[string]string{nil, {"approved": "true", "kernel_verified": "true", "resource_exception": "true"}} {
		err := (tubearchivist.App{}).Preflight(t.Context(), &helpers.H{Config: helpers.Config{Inputs: inputs}})
		var manual *helpers.ManualError
		if !errors.As(err, &manual) || !errors.Is(err, helpers.ErrManual) {
			t.Fatal("unverified startup was admitted")
		}
		for _, text := range []string{"1048576", "1 GiB", "automatic schedules", "fixture guards"} {
			if !strings.Contains(manual.Hint, text) {
				t.Errorf("missing prerequisite recovery: %s", text)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (tubearchivist.App{}).Preflight(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was lost")
	}
}

type tubeArchivistFake struct {
	loginStatus   int
	accountStatus int
	pingStatus    int
	account       string
	ping          string
	noCookie      bool
	redirect      string
	requests      []string
}

func (f *tubeArchivistFake) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/health/":
			w.Write([]byte(`"OK"`))
		case "POST /api/user/login/":
			var login map[string]string
			if err := json.NewDecoder(r.Body).Decode(&login); err != nil || login["username"] != "admin" || login["password"] != "synthetic-password" || login["remember_me"] != "off" || len(login) != 3 {
				t.Error("login did not use the intended session contract")
			}
			if f.redirect != "" {
				w.Header().Set("Location", f.redirect)
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
			if !f.noCookie {
				http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "synthetic-session", Path: "/", HttpOnly: true})
			}
			if f.loginStatus != 0 {
				w.WriteHeader(f.loginStatus)
				w.Write([]byte(`{"error":"synthetic-password synthetic-session"}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/user/account/", "GET /api/ping/":
			cookie, err := r.Cookie("sessionid")
			if err != nil || cookie.Value != "synthetic-session" || r.Header.Get("Authorization") != "" {
				t.Error("protected read did not use the origin-bound session")
			}
			if r.URL.Path == "/api/user/account/" {
				if f.accountStatus != 0 {
					w.WriteHeader(f.accountStatus)
				}
				w.Write([]byte(f.account))
				return
			}
			if f.pingStatus != 0 {
				w.WriteHeader(f.pingStatus)
			}
			w.Write([]byte(f.ping))
		default:
			t.Errorf("unapproved account, token, archive, history or task request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		}
	})
}

func newTubeArchivistHarness(t *testing.T, fake *tubeArchivistFake) (*helpers.H, tubeArchivistSecrets, *[]helpers.StepResult) {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	secrets := tubeArchivistSecrets{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password",
		"TUBEARCHIVIST_ELASTIC_PASSWORD": "existing-elastic-secret", "TUBEARCHIVIST_HOMARR_API_KEY": "existing-token",
	}
	var steps []helpers.StepResult
	h := helpers.New(helpers.Config{
		App: "tubearchivist", Service: "tubearchivist", BaseURL: srv.URL, URL: srv.URL,
		HTTP: srv.Client(), Log: events.Discard(), Secrets: secrets, Now: deps.Now, Sleep: deps.Sleep,
		Record: func(step helpers.StepResult) { steps = append(steps, step) },
	})
	return h, secrets, &steps
}

func tubeArchivistValidFake() *tubeArchivistFake {
	return &tubeArchivistFake{
		account: `{"id":42,"name":"admin","is_superuser":true,"is_staff":true}`,
		ping:    `{"response":"pong","user":42,"version":"v0.6.0"}`,
	}
}

func TestTubeArchivistSetupAndRerunVerifyTheIntendedExistingAdministrator(t *testing.T) {
	fake := tubeArchivistValidFake()
	h, secrets, steps := newTubeArchivistHarness(t, fake)
	before := maps.Clone(secrets)
	a := tubearchivist.App{}
	for range 2 {
		if err := a.Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Check(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secrets, before) {
		t.Fatal("existing credentials changed during verification")
	}
	for _, step := range *steps {
		if step.Status != stepstatus.StepSkip {
			t.Fatalf("read-only rerun step %s = %s", step.ID, step.Status)
		}
	}
	if len(fake.requests) != 11 {
		t.Fatalf("unexpected requests: %v", fake.requests)
	}
}

func TestTubeArchivistLaterCompatibleVersionAndCancellation(t *testing.T) {
	fake := tubeArchivistValidFake()
	fake.ping = `{"response":"pong","user":42,"version":"v0.6.1"}`
	h, _, _ := newTubeArchivistHarness(t, fake)
	if err := (tubearchivist.App{}).Check(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(fake.requests)
	if err := (tubearchivist.App{}).Check(ctx, h); !errors.Is(err, context.Canceled) || len(fake.requests) != before {
		t.Fatal("canceled verification reached the App")
	}
}

func TestTubeArchivistMissingOrAmbiguousIdentityStaysManual(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*tubeArchivistFake)
	}{
		{"rejected-login", func(f *tubeArchivistFake) { f.loginStatus = 400 }},
		{"forbidden-login", func(f *tubeArchivistFake) { f.loginStatus = 403 }},
		{"missing-session", func(f *tubeArchivistFake) { f.noCookie = true }},
		{"rejected-account", func(f *tubeArchivistFake) { f.accountStatus = 403 }},
		{"wrong-account", func(f *tubeArchivistFake) { f.account = `{"id":42,"name":"other","is_superuser":true,"is_staff":true}` }},
		{"non-admin", func(f *tubeArchivistFake) {
			f.account = `{"id":42,"name":"admin","is_superuser":false,"is_staff":true}`
		}},
		{"missing-role", func(f *tubeArchivistFake) { f.account = `{"id":42,"name":"admin","is_superuser":true}` }},
		{"missing-id", func(f *tubeArchivistFake) { f.account = `{"name":"admin","is_superuser":true,"is_staff":true}` }},
		{"null-id", func(f *tubeArchivistFake) {
			f.account = `{"id":null,"name":"admin","is_superuser":true,"is_staff":true}`
		}},
		{"zero-id", func(f *tubeArchivistFake) { f.account = `{"id":0,"name":"admin","is_superuser":true,"is_staff":true}` }},
		{"fractional-id", func(f *tubeArchivistFake) {
			f.account = `{"id":42.5,"name":"admin","is_superuser":true,"is_staff":true}`
		}},
		{"malformed-account", func(f *tubeArchivistFake) { f.account = `synthetic-password synthetic-session` }},
		{"wrong-ping-account", func(f *tubeArchivistFake) { f.ping = `{"response":"pong","user":43,"version":"v0.6.0"}` }},
		{"missing-ping-identity", func(f *tubeArchivistFake) { f.ping = `{"response":"pong","version":"v0.6.0"}` }},
		{"wrong-pong", func(f *tubeArchivistFake) { f.ping = `{"response":"OK","user":42,"version":"v0.6.0"}` }},
		{"old-contract", func(f *tubeArchivistFake) { f.ping = `{"response":"pong","user":42,"version":"v0.5.12"}` }},
		{"missing-version", func(f *tubeArchivistFake) { f.ping = `{"response":"pong","user":42}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := tubeArchivistValidFake()
			tc.change(fake)
			h, secrets, steps := newTubeArchivistHarness(t, fake)
			before := maps.Clone(secrets)
			err := (tubearchivist.App{}).Setup(t.Context(), h)
			var manual *helpers.ManualError
			if !errors.As(err, &manual) || !errors.Is(err, helpers.ErrManual) || manual.Hint == "" {
				t.Fatalf("unverified identity was not actionable Manual: %v", err)
			}
			if !reflect.DeepEqual(secrets, before) || len(*steps) != 2 || (*steps)[1].Status != stepstatus.StepManual {
				t.Fatal("credentials changed or Manual was not recorded")
			}
			if strings.Contains(err.Error(), "synthetic-password") || strings.Contains(err.Error(), "synthetic-session") {
				t.Fatal("response or credentials escaped into diagnostics")
			}
			if err := (tubearchivist.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("Check accepted the same unverified identity")
			}
		})
	}
}

func TestTubeArchivistMissingLoginDoesNotSendCredentials(t *testing.T) {
	for _, key := range []string{"ADMIN_USER", "ADMIN_PASSWORD"} {
		t.Run(key, func(t *testing.T) {
			fake := tubeArchivistValidFake()
			h, secrets, _ := newTubeArchivistHarness(t, fake)
			delete(secrets, key)
			if err := (tubearchivist.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) || len(fake.requests) != 0 {
				t.Fatal("missing credentials reached the App")
			}
		})
	}
}

func TestTubeArchivistLoginDoesNotFollowRedirectsOrEchoFailures(t *testing.T) {
	forwarded := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded = true }))
	t.Cleanup(other.Close)
	fake := tubeArchivistValidFake()
	fake.redirect = other.URL + "/capture"
	h, _, _ := newTubeArchivistHarness(t, fake)
	if err := (tubearchivist.App{}).Check(t.Context(), h); err == nil || forwarded || len(fake.requests) != 1 {
		t.Fatal("redirected login was accepted or forwarded")
	}
	for _, change := range []func(*tubeArchivistFake){
		func(f *tubeArchivistFake) { f.loginStatus = 500 },
		func(f *tubeArchivistFake) { f.pingStatus = 500; f.ping = `synthetic-password synthetic-session` },
	} {
		fake := tubeArchivistValidFake()
		change(fake)
		h, _, _ := newTubeArchivistHarness(t, fake)
		err := (tubearchivist.App{}).Check(t.Context(), h)
		if err == nil || strings.Contains(err.Error(), "synthetic-password") || strings.Contains(err.Error(), "synthetic-session") {
			t.Fatal("failed API response was accepted or exposed")
		}
	}
}

type tubeArchivistTransport func(*http.Request) (*http.Response, error)

func (transport tubeArchivistTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestTubeArchivistCheckRedactsTransportDiagnostics(t *testing.T) {
	fake := tubeArchivistValidFake()
	h, _, _ := newTubeArchivistHarness(t, fake)
	h.Config.HTTP = &http.Client{Transport: tubeArchivistTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic-password")
	})}
	err := (tubearchivist.App{}).Check(t.Context(), h)
	if err == nil || strings.Contains(err.Error(), "synthetic-password") || !strings.Contains(err.Error(), "login request failed") {
		t.Fatal("transport diagnostics were lost or exposed a credential")
	}
}
