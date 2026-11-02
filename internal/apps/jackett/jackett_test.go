package jackett_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/jackett"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
	"go.yaml.in/yaml/v3"
)

const (
	adminPassword = "fixture-existing-admin-password"
	apiKey        = "fixture+jackett/key&value=private"
	privateMarker = "fixture-unrelated-proxy-password"
)

type fixtureSecrets struct {
	values map[string]string
	writes int
	err    error
}

func (s *fixtureSecrets) Get(key string) string { return s.values[key] }

func (s *fixtureSecrets) Set(key, value string) error {
	s.writes++
	if s.err != nil {
		return s.err
	}
	s.values[key] = value
	return nil
}

type fixture struct {
	mu                   sync.Mutex
	password             string
	config               map[string]any
	configBody           string
	caps                 string
	indexers             string
	anonymousAllowed     bool
	anonymousDestination string
	cookieWithoutAccess  bool
	apiStatus            int
	forbidden            []string
	requests             int
}

func newFixture() *fixture {
	return &fixture{
		password: adminPassword,
		config: map[string]any{
			"api_key": apiKey, "app_version": "0.24.2994", "password": "hashprefix",
			"proxy_password": privateMarker, "basepathoverride": "", "blackholedir": "/keep-existing",
		},
		caps: `<caps><server title="Jackett"/><searching><search available="yes"/></searching></caps>`,
		indexers: `<indexers><indexer id="person-selected" configured="true"><title>Existing tracker</title>` +
			`<link>https://tracker.invalid/</link><caps/></indexer></indexers>`,
		apiStatus: http.StatusOK,
	}
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	switch r.Method + " " + r.URL.Path {
	case "GET /health":
		w.Write([]byte(`{"status":"OK"}`))
	case "POST /UI/Dashboard":
		if err := r.ParseForm(); err != nil || len(r.PostForm) != 1 || r.PostForm.Get("password") == "" {
			f.forbidden = append(f.forbidden, "unexpected login fields")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.password == "" || r.PostForm.Get("password") == f.password {
			http.SetCookie(w, &http.Cookie{Name: "Jackett", Value: "fixture-session", Path: "/"})
		}
		w.Header().Set("Location", "Dashboard")
		w.WriteHeader(http.StatusFound)
	case "GET /api/v2.0/server/Config":
		cookie, err := r.Cookie("Jackett")
		if !f.anonymousAllowed && (err != nil || cookie.Value != "fixture-session" || f.cookieWithoutAccess) {
			destination := "/UI/Login?ReturnUrl=%2Fapi%2Fv2.0%2Fserver%2FConfig"
			if f.anonymousDestination != "" {
				destination = f.anonymousDestination
			}
			w.Header().Set("Location", destination)
			w.WriteHeader(http.StatusFound)
			return
		}
		if f.configBody != "" {
			w.Write([]byte(f.configBody))
			return
		}
		json.NewEncoder(w).Encode(f.config)
	case "GET /api/v2.0/indexers/all/results/torznab/api":
		query := r.URL.Query()
		action := query.Get("t")
		if action != "caps" && action != "indexers" || action == "indexers" && (query.Get("configured") != "true" || len(query) != 3) || action == "caps" && len(query) != 2 {
			f.forbidden = append(f.forbidden, "unsafe Torznab action")
			w.WriteHeader(http.StatusTeapot)
			return
		}
		if query.Get("apikey") != apiKey {
			w.Write([]byte(`<error code="100" description="Invalid API Key"/>`))
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(f.apiStatus)
		if action == "caps" {
			w.Write([]byte(f.caps))
			return
		}
		w.Write([]byte(f.indexers))
	default:
		f.forbidden = append(f.forbidden, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func (f *fixture) assertPreserved(t *testing.T, originalConfig, originalIndexers string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	config, err := json.Marshal(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if string(config) != originalConfig || f.indexers != originalIndexers || f.password != adminPassword || len(f.forbidden) != 0 {
		t.Fatalf("Jackett configuration changed or unsafe requests occurred: %v", f.forbidden)
	}
}

func fixtureH(t *testing.T, f *fixture, secrets *fixtureSecrets) (*helpers.H, *[]helpers.StepResult, *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	var records []helpers.StepResult
	var output bytes.Buffer
	h := helpers.New(helpers.Config{
		App: "jackett", Service: "jackett", ContainerPort: 9117,
		BaseURL: server.URL, URL: server.URL + "/UI/Dashboard", HTTP: server.Client(),
		Secrets: secrets, Log: events.New(events.JSON(&output)),
		Record: func(result helpers.StepResult) { records = append(records, result) },
	})
	return h, &records, &output
}

func credentials() *fixtureSecrets {
	return &fixtureSecrets{values: map[string]string{
		"ADMIN_USER": "fixture-admin", "ADMIN_PASSWORD": adminPassword,
	}}
}

func TestDeploymentUsesTheExistingSupportedDCMImageAndPrivateAppStorage(t *testing.T) {
	a := jackett.App{}
	info := a.Info()
	if info.ID != "jackett" || info.Service != "jackett" || info.Image != "ghcr.io/hotio/jackett:latest" || info.Port != 9117 || info.ContainerPort != 9117 || info.ContainerPortIsPort || info.UsesMedia || info.Path != "/UI/Dashboard" {
		t.Fatalf("unexpected Jackett metadata: %+v", info)
	}
	var definition struct {
		Services map[string]struct {
			Image       string
			Restart     string
			Environment map[string]string
			Volumes     []string
			Ports       []string
		} `yaml:"services"`
	}
	compose := a.Compose(app.RenderContext{Port: 19117})
	if err := yaml.Unmarshal([]byte(compose), &definition); err != nil {
		t.Fatal(err)
	}
	service := definition.Services["jackett"]
	if len(definition.Services) != 1 || service.Image != info.Image || service.Restart != "unless-stopped" || len(service.Ports) != 1 || service.Ports[0] != "${BIND_IP}:19117:9117" || len(service.Volumes) != 1 || service.Volumes[0] != "./config:/config" {
		t.Fatalf("unexpected Compose definition: %s", compose)
	}
	for key, value := range map[string]string{"PUID": "${PUID}", "PGID": "${PGID}", "UMASK": "${UMASK}", "TZ": "${TZ}"} {
		if service.Environment[key] != value {
			t.Errorf("environment %s = %q, want %q", key, service.Environment[key], value)
		}
	}
	if len(service.Environment) != 4 || strings.Contains(compose, "container_name") || strings.Contains(compose, "ADMIN_") || len(a.Links()) != 0 {
		t.Fatal("deployment introduced credentials, dependencies or unrelated Links")
	}
	for _, input := range a.Inputs() {
		if !input.Private || !input.RecoveryOnly || input.Required {
			t.Errorf("input %s must be private optional recovery", input.Key)
		}
	}
	if !strings.Contains(a.StandaloneGuide(), "first password") || !strings.Contains(a.StandaloneGuide(), "trackers") {
		t.Fatal("standalone guide must disclose the password and tracker prerequisites")
	}
}

func TestSetupSavesOnlyTheAcceptedExistingKeyAndRerunsWithoutMutation(t *testing.T) {
	for _, indexers := range []string{"<indexers/>", newFixture().indexers} {
		t.Run(indexers, func(t *testing.T) {
			f := newFixture()
			f.indexers = indexers
			originalConfig, err := json.Marshal(f.config)
			if err != nil {
				t.Fatal(err)
			}
			secrets := credentials()
			h, records, output := fixtureH(t, f, secrets)
			for attempt := range 2 {
				*records = nil
				if err := (jackett.App{}).Setup(t.Context(), h); err != nil {
					t.Fatalf("Setup %d: %v", attempt, err)
				}
				if err := (jackett.App{}).Check(t.Context(), h); err != nil {
					t.Fatalf("Check %d: %v", attempt, err)
				}
				if attempt == 1 {
					for _, record := range *records {
						if record.Status != status.StepSkip {
							t.Errorf("rerun step %s = %s, want skip", record.ID, record.Status)
						}
					}
				}
			}
			if secrets.Get("JACKETT_API_KEY") != apiKey || secrets.writes != 1 {
				t.Fatal("Setup must store the verified existing key once")
			}
			f.assertPreserved(t, string(originalConfig), indexers)
			for _, secret := range []string{apiKey, adminPassword, privateMarker} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("Setup output exposed a secret")
				}
			}
		})
	}
}

func TestExistingPrivatePasswordAndKeyAreReused(t *testing.T) {
	f := newFixture()
	secrets := credentials()
	secrets.values["ADMIN_PASSWORD"] = "fixture-other-app-password"
	secrets.values["JACKETT_ADMIN_PASSWORD"] = adminPassword
	secrets.values["JACKETT_API_KEY"] = apiKey
	h, _, _ := fixtureH(t, f, secrets)
	if err := (jackett.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if secrets.writes != 0 || secrets.Get("ADMIN_PASSWORD") != "fixture-other-app-password" {
		t.Fatal("Setup replaced an existing credential")
	}
}

func TestUnsafeOrIncompleteCredentialsAndDataNeverBecomeAccepted(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*fixture, *fixtureSecrets)
		manual bool
	}{
		{"first password absent", func(f *fixture, _ *fixtureSecrets) { f.password = ""; f.config["password"] = "" }, true},
		{"wrong administrator password", func(_ *fixture, s *fixtureSecrets) { s.values["ADMIN_PASSWORD"] = "fixture-wrong-password" }, true},
		{"cookie without authenticated data", func(f *fixture, _ *fixtureSecrets) { f.cookieWithoutAccess = true }, true},
		{"anonymous settings readable", func(f *fixture, _ *fixtureSecrets) { f.anonymousAllowed = true }, true},
		{"external anonymous redirect", func(f *fixture, _ *fixtureSecrets) { f.anonymousDestination = "https://untrusted.invalid/UI/Login" }, true},
		{"saved key mismatch", func(_ *fixture, s *fixtureSecrets) { s.values["JACKETT_API_KEY"] = "fixture-stale-key" }, true},
		{"unsupported version", func(f *fixture, _ *fixtureSecrets) { f.config["app_version"] = "1.0.0" }, true},
		{"older version", func(f *fixture, _ *fixtureSecrets) { f.config["app_version"] = "0.23.0" }, true},
		{"missing identity", func(f *fixture, _ *fixtureSecrets) { delete(f.config, "app_version") }, true},
		{"null key", func(f *fixture, _ *fixtureSecrets) { f.config["api_key"] = nil }, true},
		{"empty key", func(f *fixture, _ *fixtureSecrets) { f.config["api_key"] = " " }, true},
		{"null password state", func(f *fixture, _ *fixtureSecrets) { f.config["password"] = nil }, true},
		{"wrong password type", func(f *fixture, _ *fixtureSecrets) { f.config["password"] = false }, true},
		{"secret bearing malformed config", func(f *fixture, _ *fixtureSecrets) { f.configBody = `{"api_key":"` + privateMarker + `",bad` }, true},
		{"key unauthorized", func(f *fixture, _ *fixtureSecrets) { f.apiStatus = http.StatusUnauthorized }, true},
		{"HTTP 200 key error", func(f *fixture, _ *fixtureSecrets) {
			f.caps = `<error code="100" description="` + privateMarker + `"/>`
		}, true},
		{"HTTP 200 HTML", func(f *fixture, _ *fixtureSecrets) { f.caps = `<html>login</html>` }, true},
		{"empty capabilities", func(f *fixture, _ *fixtureSecrets) { f.caps = `<caps/>` }, true},
		{"wrong capability identity", func(f *fixture, _ *fixtureSecrets) { f.caps = strings.ReplaceAll(f.caps, "Jackett", "Other") }, true},
		{"missing capability availability", func(f *fixture, _ *fixtureSecrets) { f.caps = `<caps><server title="Jackett"/></caps>` }, true},
		{"text instead of capabilities", func(f *fixture, _ *fixtureSecrets) { f.caps = `<caps>missing</caps>` }, true},
		{"nested capability error", func(f *fixture, _ *fixtureSecrets) {
			f.caps = strings.Replace(f.caps, "</caps>", `<error code="100"/></caps>`, 1)
		}, true},
		{"malformed XML", func(f *fixture, _ *fixtureSecrets) { f.caps = `<caps>` }, true},
		{"trailing XML root", func(f *fixture, _ *fixtureSecrets) { f.caps += `<error code="100"/>` }, false},
		{"trailing malformed XML", func(f *fixture, _ *fixtureSecrets) { f.caps += `<` }, false},
		{"indexer error", func(f *fixture, _ *fixtureSecrets) { f.indexers = `<error code="100"/>` }, true},
		{"text instead of empty catalog", func(f *fixture, _ *fixtureSecrets) { f.indexers = `<indexers>missing</indexers>` }, false},
		{"nested indexer error", func(f *fixture, _ *fixtureSecrets) { f.indexers = `<indexers><error code="100"/></indexers>` }, false},
		{"missing indexer ID", func(f *fixture, _ *fixtureSecrets) {
			f.indexers = `<indexers><indexer configured="true"><title>Keep</title></indexer></indexers>`
		}, false},
		{"missing configured state", func(f *fixture, _ *fixtureSecrets) {
			f.indexers = `<indexers><indexer id="keep"><title>Keep</title></indexer></indexers>`
		}, false},
		{"unconfigured indexer", func(f *fixture, _ *fixtureSecrets) {
			f.indexers = `<indexers><indexer id="keep" configured="false"><title>Keep</title></indexer></indexers>`
		}, false},
		{"missing indexer title", func(f *fixture, _ *fixtureSecrets) {
			f.indexers = `<indexers><indexer id="keep" configured="true"/></indexers>`
		}, false},
		{"duplicate indexer ID", func(f *fixture, _ *fixtureSecrets) {
			f.indexers = `<indexers>` + strings.Repeat(`<indexer id="keep" configured="true"><title>Keep</title></indexer>`, 2) + `</indexers>`
		}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture()
			secrets := credentials()
			secrets.values["JACKETT_API_KEY"] = apiKey
			scenario.change(f, secrets)
			saved := secrets.Get("JACKETT_API_KEY")
			h, records, output := fixtureH(t, f, secrets)
			err := (jackett.App{}).Setup(t.Context(), h)
			if err == nil || errors.Is(err, helpers.ErrManual) != scenario.manual {
				t.Fatalf("Setup error = %v, want Manual=%v", err, scenario.manual)
			}
			if secrets.Get("JACKETT_API_KEY") != saved || secrets.writes != 0 {
				t.Fatal("failed Setup replaced or saved a key")
			}
			wantStatus := status.StepFail
			if scenario.manual {
				wantStatus = status.StepManual
			}
			last := (*records)[len(*records)-1]
			if last.Status != wantStatus || !strings.Contains(last.Hint, "self-host setup jackett") {
				t.Fatalf("Setup did not retain actionable recovery: %+v", last)
			}
			if check := (jackett.App{}).Check(t.Context(), h); check == nil {
				t.Fatal("Check accepted invalid credentials or data")
			}
			for _, secret := range []string{apiKey, privateMarker, secrets.Get("ADMIN_PASSWORD"), saved} {
				if strings.Contains(err.Error()+output.String(), secret) {
					t.Fatal("failure exposed credentials or private response data")
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.forbidden) != 0 {
				t.Fatalf("unsafe requests: %v", f.forbidden)
			}
		})
	}
}

func TestMissingKeyCheckIsReadOnlyAndSetupVerifiesBeforeSaving(t *testing.T) {
	f := newFixture()
	f.caps = `<error code="100" description="Invalid API Key"/>`
	secrets := credentials()
	h, _, _ := fixtureH(t, f, secrets)
	if err := (jackett.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Check = %v, want Manual", err)
	}
	f.mu.Lock()
	requests := f.requests
	f.mu.Unlock()
	if requests != 0 {
		t.Fatal("Check without a saved key contacted Jackett")
	}
	if err := (jackett.App{}).Setup(t.Context(), h); err == nil || secrets.writes != 0 || secrets.Get("JACKETT_API_KEY") != "" {
		t.Fatal("Setup saved an unaccepted discovered credential")
	}
}

func TestFailedSecretPersistenceDoesNotChangeThePeerOrIssueAnotherKey(t *testing.T) {
	f := newFixture()
	secrets := credentials()
	secrets.err = errors.New("fixture secret storage unavailable")
	h, _, _ := fixtureH(t, f, secrets)
	if err := (jackett.App{}).Setup(t.Context(), h); err == nil || secrets.Get("JACKETT_API_KEY") != "" {
		t.Fatal("Setup accepted failed secret persistence")
	}
	secrets.err = nil
	if err := (jackett.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if secrets.Get("JACKETT_API_KEY") != apiKey {
		t.Fatal("retry did not retain the existing instance key")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forbidden) != 0 {
		t.Fatalf("retry mutated the peer: %v", f.forbidden)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(privateMarker + " " + adminPassword + " " + apiKey)
}

func TestTransportErrorsDoNotExposeCredentialsAndCancellationSurvives(t *testing.T) {
	secrets := credentials()
	secrets.values["JACKETT_API_KEY"] = apiKey
	h := helpers.New(helpers.Config{
		App: "jackett", BaseURL: "https://jackett.invalid", Secrets: secrets,
		HTTP: &http.Client{Transport: failingTransport{}}, Log: events.Discard(),
	})
	err := (jackett.App{}).Check(t.Context(), h)
	if err == nil || strings.Contains(err.Error(), privateMarker) || strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), adminPassword) {
		t.Fatal("transport failure was swallowed or exposed credentials")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (jackett.App{}).Check(ctx, h); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Check = %v", err)
	}
}
