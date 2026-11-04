package yourspotify_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/yourspotify"
	"github.com/ajnart/self-host/internal/compose"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
	"github.com/ajnart/self-host/tools/sitegen/standalone"
	"go.yaml.in/yaml/v3"
)

type secrets map[string]string

func (s secrets) Get(key string) string    { return s[key] }
func (s secrets) Set(string, string) error { return errors.New("unexpected credential write") }

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const accountID = "507f1f77bcf86cd799439011"
const publicToken = "synthetic-public-token"
const identity = `{"status":true,"user":{"_id":"507f1f77bcf86cd799439011","spotifyId":"synthetic-spotify-account","publicToken":"synthetic-public-token"}}`

func TestOfficialComposePreservesPortsPersistenceAndStartupHold(t *testing.T) {
	var definition struct {
		Services map[string]struct {
			Image, Restart           string
			Profiles, Ports, Volumes []string
			DependsOn                []string `yaml:"depends_on"`
			Environment              map[string]string
			Logging                  struct {
				Driver  string
				Options map[string]string
			}
		}
	}
	a := yourspotify.App{}
	raw := a.Compose(app.RenderContext{Port: 13000, Inputs: map[string]string{
		"api-port": "18080", "client_endpoint": "https://music.example.test", "api_endpoint": "https://api.example.test",
		"spotify_public": "synthetic-client-id", "spotify_secret": "synthetic-client-secret", "homarr_api_key": publicToken,
	}})
	if err := yaml.Unmarshal([]byte(raw), &definition); err != nil {
		t.Fatal(err)
	}
	if len(definition.Services) != 3 {
		t.Fatal("expected the official client, server and Mongo services")
	}
	client := definition.Services["your-spotify"]
	server := definition.Services["your-spotify-server"]
	mongo := definition.Services["your-spotify-mongo"]
	if client.Image != "yooooomi/your_spotify_client:latest" || server.Image != "yooooomi/your_spotify_server:latest" || mongo.Image != "mongo:8" {
		t.Fatal("unexpected image substitution")
	}
	if !reflect.DeepEqual(client.Ports, []string{"${BIND_IP}:13000:3000"}) || !reflect.DeepEqual(server.Ports, []string{"${BIND_IP}:18080:8080"}) || len(mongo.Ports) != 0 {
		t.Fatal("published ports do not preserve selected Access")
	}
	if !reflect.DeepEqual(mongo.Volumes, []string{"./data:/data/db", "./configdb:/data/configdb"}) {
		t.Fatal("Mongo persistence is incomplete")
	}
	if server.Restart != "no" || !reflect.DeepEqual(server.Profiles, []string{"your-spotify-manual"}) || len(client.DependsOn) != 0 {
		t.Fatal("ordinary startup can launch ingestion or history cleanup")
	}
	if !reflect.DeepEqual(server.DependsOn, []string{"your-spotify-mongo"}) || server.Logging.Driver != "local" || server.Logging.Options["max-size"] != "10m" || server.Logging.Options["max-file"] != "3" {
		t.Fatal("backend dependency or bounded logs are missing")
	}
	if server.Environment["LOG_LEVEL"] != "warn" || server.Environment["MONGO_ENDPOINT"] != "mongodb://your-spotify-mongo:27017/your_spotify" || server.Environment["TIMEZONE"] != "${TZ}" {
		t.Fatal("unsafe server defaults")
	}
	if server.Environment["SPOTIFY_PUBLIC"] != "${YOUR_SPOTIFY_SPOTIFY_PUBLIC}" || server.Environment["SPOTIFY_SECRET"] != "${YOUR_SPOTIFY_SPOTIFY_SECRET}" {
		t.Fatal("OAuth credentials must remain private interpolation")
	}
	if client.Environment["API_ENDPOINT"] != "https://api.example.test" || server.Environment["API_ENDPOINT"] != "https://api.example.test" || server.Environment["CLIENT_ENDPOINT"] != "https://music.example.test" {
		t.Fatal("explicit browser Addresses were changed")
	}
	for _, forbidden := range []string{"synthetic-client-id", "synthetic-client-secret", publicToken, "OFFLINE_DEV_ID", "CORS:", "container_name:", "docker.sock", "PUID", "PGID"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("unexpected Compose value %s", forbidden)
		}
	}
	info := a.Info()
	if info.ID != "your-spotify" || info.Service != "your-spotify" || info.ContainerPort != 3000 || info.Port != 3000 || info.UsesMedia || len(a.Links()) != 0 {
		t.Fatal("incorrect App identity or unrelated Link")
	}
	if !reflect.DeepEqual(a.Deferred(nil), []string{"your-spotify-server"}) {
		t.Fatal("backend must remain held without invented freshness authority")
	}
	if _, ok := any(a).(app.SecretDeclarer); ok {
		t.Fatal("externally issued OAuth/public tokens must not be generated")
	}
}

func TestInputsAndDefaultBrowserAddresses(t *testing.T) {
	a := yourspotify.App{}
	inputs := map[string]app.Input{}
	for _, input := range a.Inputs() {
		inputs[input.Key] = input
	}
	for _, key := range []string{"spotify_public", "spotify_secret", "homarr_api_key", "homarr_user_id"} {
		if !inputs[key].Private || inputs[key].Default != "" || inputs[key].Required {
			t.Fatal("external credentials need optional private input and Manual recovery")
		}
	}
	if !reflect.DeepEqual(inputs["api-port"].HostPortProtocols, []string{"tcp"}) {
		t.Fatal("API port must use existing allocation")
	}
	for _, value := range []string{"0", "65536", "-1", "8080\nservices:", "not-a-port"} {
		if inputs["api-port"].Validate(value) == nil {
			t.Fatal("invalid API port accepted")
		}
	}
	for _, key := range []string{"client_endpoint", "api_endpoint"} {
		for _, value := range []string{"http://user:secret@example.test", "http://example.test/?token=secret", "http://example.test/#secret", "http://example.test/#", "ftp://example.test", "http://example.test/extra", "https://example.test\nBAD", "https://example.test:65536", "http://example.test:"} {
			if inputs[key].Validate(value) == nil {
				t.Fatal("unsafe browser Address accepted")
			}
		}
	}
	raw := a.Compose(app.RenderContext{Port: 13001, Settings: map[string]string{"EXPOSURE": "local", "LAN_IP": "192.0.2.10"}})
	if !strings.Contains(raw, `API_ENDPOINT: "http://127.0.0.1:8080"`) || !strings.Contains(raw, `CLIENT_ENDPOINT: "http://127.0.0.1:13001"`) {
		t.Fatal("local Access must not advertise a LAN or Compose Address")
	}
}

func TestStandaloneExportKeepsExternalSecretsBlankAndServerHeld(t *testing.T) {
	entry, err := standalone.Project(yourspotify.App{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"YOUR_SPOTIFY_SPOTIFY_PUBLIC", "YOUR_SPOTIFY_SPOTIFY_SECRET"} {
		found := false
		for _, variable := range entry.Env {
			if variable.Key == key {
				found = true
				if variable.Default != "" || !variable.External || !variable.Secret || variable.Bytes != 0 {
					t.Fatal("external credentials must not be fabricated")
				}
			}
		}
		if !found {
			t.Fatal("missing external OAuth input")
		}
	}
	if !strings.Contains(entry.Compose, "your-spotify-manual") || !strings.Contains(entry.Compose, `restart: "no"`) || !strings.Contains(entry.Notes, "duplicate deletion") {
		t.Fatal("standalone export lost the startup preservation hold")
	}
}

func accountHelper(t *testing.T, body string, code int) (*helpers.H, *systemtest.Runner, *int) {
	t.Helper()
	runner := &systemtest.Runner{}
	runner.On("docker compose", system.Result{Stdout: []byte("warn\n")})
	deps := systemtest.NewDeps(t, systemtest.WithRunner(runner))
	log := events.Discard()
	queries := new(int)
	h := helpers.New(helpers.Config{
		App: "your-spotify", Service: "your-spotify", BaseURL: "http://127.0.0.1:13000", Port: 13000,
		Inputs: map[string]string{"api-port": "18080"}, Settings: map[string]string{"EXPOSURE": "local"},
		Log: log, Exec: compose.New(deps, log, t.TempDir(), "synthetic-project"),
		Secrets: secrets{"YOUR_SPOTIFY_HOMARR_API_KEY": publicToken, "YOUR_SPOTIFY_HOMARR_USER_ID": accountID, "YOUR_SPOTIFY_SPOTIFY_PUBLIC": "synthetic-client-id", "YOUR_SPOTIFY_SPOTIFY_SECRET": "synthetic-client-secret"},
		HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			*queries++
			if r.Method != http.MethodGet || r.URL.Host != "127.0.0.1:18080" || r.URL.Path != "/me" || r.URL.Query().Get("token") != publicToken || r.Header.Get("Cookie") != "" {
				t.Fatal("identity verification left the local read-only contract")
			}
			return &http.Response{StatusCode: code, Header: http.Header{"Location": []string{"https://accounts.spotify.com/?token=" + publicToken}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})},
	})
	return h, runner, queries
}

func TestExistingSelectedPublicTokenIsVerifiedWithoutIssuanceOrCookieFallback(t *testing.T) {
	h, runner, queries := accountHelper(t, identity, http.StatusOK)
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("http://127.0.0.1:18080")
	jar.SetCookies(origin, []*http.Cookie{{Name: "token", Value: "synthetic-session"}})
	h.Config.HTTP.Jar = jar
	before := map[string]string{}
	for k, v := range h.Secrets.(secrets) {
		before[k] = v
	}
	for range 2 {
		if err := yourspotify.VerifyAccount(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if *queries != 2 || !reflect.DeepEqual(before, map[string]string(h.Secrets.(secrets))) {
		t.Fatal("verification did not reuse the selected credentials")
	}
	for _, cmd := range runner.Cmds() {
		if !strings.HasSuffix(systemtest.Line(cmd), "exec -T your-spotify-server printenv LOG_LEVEL") {
			t.Fatal("verification executed more than the logging guard")
		}
	}
}

func TestIdentityRejectsMissingWrongAndMalformedAccounts(t *testing.T) {
	for _, body := range []string{
		`{"status":false}`, `{"user":{}}`, `{"status":true}`, `{"status":true,"user":null}`,
		strings.Replace(identity, accountID, "507f1f77bcf86cd799439012", 1),
		strings.Replace(identity, `"spotifyId":"synthetic-spotify-account",`, "", 1),
		strings.Replace(identity, `"publicToken":"synthetic-public-token"`, `"publicToken":"another-token"`, 1),
		`not-json`,
	} {
		t.Run(body, func(t *testing.T) {
			h, _, _ := accountHelper(t, body, http.StatusOK)
			if err := yourspotify.VerifyAccount(t.Context(), h); err == nil {
				t.Fatal("unverified identity accepted")
			}
		})
	}
}

func TestPrivacyAndMissingInputsStopBeforeQueryAuthentication(t *testing.T) {
	for _, scenario := range []string{"missing-token", "missing-account", "bad-account", "missing-exec", "info-logging", "unknown-logging", "exec-failure", "remote-probe"} {
		t.Run(scenario, func(t *testing.T) {
			h, runner, queries := accountHelper(t, identity, http.StatusOK)
			switch scenario {
			case "missing-token":
				delete(h.Secrets.(secrets), "YOUR_SPOTIFY_HOMARR_API_KEY")
			case "missing-account":
				delete(h.Secrets.(secrets), "YOUR_SPOTIFY_HOMARR_USER_ID")
			case "bad-account":
				h.Secrets.(secrets)["YOUR_SPOTIFY_HOMARR_USER_ID"] = "not-an-account"
			case "missing-exec":
				h.Config.Exec = nil
			case "info-logging":
				runner.On("docker compose", system.Result{Stdout: []byte("info\n")})
			case "unknown-logging":
				runner.On("docker compose", system.Result{})
			case "exec-failure":
				runner.On("docker compose", system.Result{ExitCode: 1, Stderr: []byte(publicToken)})
			case "remote-probe":
				h.BaseURL = "https://api.spotify.com"
			}
			err := yourspotify.VerifyAccount(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) || *queries != 0 || strings.Contains(err.Error(), publicToken) {
				t.Fatal("unsafe or missing prerequisite did not stop privately before requests")
			}
		})
	}
}

func TestTokenTransportStatusAndRedirectErrorsRemainFixedAndPrivate(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h, _, queries := accountHelper(t, publicToken, code)
			err := yourspotify.VerifyAccount(t.Context(), h)
			if err == nil || *queries != 1 || strings.Contains(err.Error(), publicToken) {
				t.Fatal("HTTP error exposed or accepted credential-bearing data")
			}
		})
	}
	h, _, _ := accountHelper(t, identity, http.StatusOK)
	h.Config.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unsafe transport detail: %s", r.URL)
	})
	if err := yourspotify.VerifyAccount(t.Context(), h); err == nil || strings.Contains(err.Error(), publicToken) || strings.Contains(err.Error(), "unsafe transport") {
		t.Fatal("raw query-auth transport error escaped")
	}
}

func TestSetupAndCheckRetainSeparateProviderAndStartupManualBoundaries(t *testing.T) {
	h, _, queries := accountHelper(t, identity, http.StatusOK)
	var records []helpers.StepResult
	h.Config.Record = func(result helpers.StepResult) { records = append(records, result) }
	err := (yourspotify.App{}).Setup(context.Background(), h)
	if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) || *queries != 1 {
		t.Fatal("Setup must verify only the existing local account and remain Manual")
	}
	for _, id := range []string{"startup-review", "spotify-consent", "ingestion"} {
		found := false
		for _, result := range records {
			if result.ID == id && result.Status == status.StepManual && result.Hint != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing actionable Manual boundary %s", id)
		}
	}
	if err := (yourspotify.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("local identity is not provider or ingestion acceptance")
	}
}

func TestCheckRejectsMissingOAuthInputsAndIneligibleCallbackWithoutRequests(t *testing.T) {
	for _, scenario := range []string{"missing-client", "missing-secret", "http-lan", "localhost", "invalid-port"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, queries := accountHelper(t, identity, http.StatusOK)
			switch scenario {
			case "missing-client":
				delete(h.Secrets.(secrets), "YOUR_SPOTIFY_SPOTIFY_PUBLIC")
			case "missing-secret":
				delete(h.Secrets.(secrets), "YOUR_SPOTIFY_SPOTIFY_SECRET")
			case "http-lan":
				h.Inputs["api_endpoint"] = "http://192.0.2.10:18080"
			case "localhost":
				h.Inputs["api_endpoint"] = "http://localhost:18080"
			case "invalid-port":
				h.Inputs["api-port"] = "bad-port"
			}
			if err := (yourspotify.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) || *queries != 0 {
				t.Fatal("invalid deployment prerequisite did not stop locally")
			}
		})
	}
}

func TestSetupRerunsPreserveExistingHistoryAndAccountSettings(t *testing.T) {
	h, runner, _ := accountHelper(t, identity, http.StatusOK)
	h.Dir = t.TempDir()
	for _, name := range []string{"history.fixture", "account-settings.fixture"} {
		path := filepath.Join(h.Dir, name)
		if err := os.WriteFile(path, []byte("synthetic existing data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := (yourspotify.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("existing history does not release startup or provider holds")
		}
	}
	for _, name := range []string{"history.fixture", "account-settings.fixture"} {
		data, err := os.ReadFile(filepath.Join(h.Dir, name))
		if err != nil || string(data) != "synthetic existing data" {
			t.Fatal("Setup changed existing data")
		}
	}
	for _, cmd := range runner.Cmds() {
		if !strings.HasSuffix(systemtest.Line(cmd), "exec -T your-spotify-server printenv LOG_LEVEL") {
			t.Fatal("rerun started a service or attempted data cleanup")
		}
	}
}
