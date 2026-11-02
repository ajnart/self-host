package healthchecks_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/healthchecks"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
	"github.com/ajnart/self-host/tools/sitegen/standalone"
	"go.yaml.in/yaml/v3"
)

const projectID = "27d76ba7-a070-4fe1-89ba-20c425a30579"
const badgeURL = "http://healthchecks.example.invalid/badge/selected-project/abcDEF12-2.svg"
const apiKey = "synthetic-read-only-project-key"

func TestOfficialComposeAndAccess(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		settings map[string]string
		inputs   map[string]string
		root     string
		ping     string
		host     string
	}{
		{"home", map[string]string{"LAN_IP": "192.168.1.20"}, nil, "http://192.168.1.20:8765", "http://192.168.1.20:8765/ping/", "192.168.1.20"},
		{"local", map[string]string{"LAN_IP": "192.168.1.20", "EXPOSURE": "local", "EASY_NAMES": "on"}, nil, "http://127.0.0.1:8765", "http://127.0.0.1:8765/ping/", "127.0.0.1"},
		{"easy names", map[string]string{"LAN_IP": "192.168.1.20", "EASY_NAMES": "on"}, nil, "http://healthchecks.192-168-1-20.sslip.io", "http://healthchecks.192-168-1-20.sslip.io/ping/", "healthchecks.192-168-1-20.sslip.io"},
		{"IPv6", map[string]string{"LAN_IP": "::1"}, nil, "http://[::1]:8765", "http://[::1]:8765/ping/", "[::1]"},
		{"explicit", nil, map[string]string{"site_root": "https://checks.example.invalid/", "ping_endpoint": "https://ping.example.invalid/jobs/"}, "https://checks.example.invalid", "https://ping.example.invalid/jobs/", "checks.example.invalid"},
		{"standalone", nil, nil, "http://${LAN_IP}:8765", "http://${LAN_IP}:8765/ping/", "${LAN_IP}"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var definition struct {
				Services map[string]struct {
					Image       string
					Restart     string
					Environment map[string]string
					Ports       []string
					Volumes     []string
					Command     any
					DependsOn   map[string]struct{ Condition string } `yaml:"depends_on"`
					Healthcheck struct{ Test []string }
				}
			}
			fragment := (healthchecks.App{}).Compose(app.RenderContext{Port: 8765, Settings: scenario.settings, Inputs: scenario.inputs})
			if err := yaml.Unmarshal([]byte(fragment), &definition); err != nil {
				t.Fatal(err)
			}
			web, database := definition.Services["healthchecks"], definition.Services["healthchecks-db"]
			if len(definition.Services) != 2 || web.Image != "healthchecks/healthchecks:latest" || database.Image != "postgres:16" || web.Command != nil || database.Command != nil {
				t.Fatal("deployment must use only the official App entrypoint and its database")
			}
			if web.Restart != "unless-stopped" || database.Restart != "unless-stopped" || !reflect.DeepEqual(web.Ports, []string{"${BIND_IP}:8765:8000"}) || len(database.Ports) != 0 {
				t.Fatal("restart or selected bind/database isolation changed")
			}
			if !reflect.DeepEqual(database.Volumes, []string{"./database:/var/lib/postgresql/data"}) || len(web.Volumes) != 0 || web.DependsOn["healthchecks-db"].Condition != "service_healthy" || !reflect.DeepEqual(database.Healthcheck.Test, []string{"CMD-SHELL", "pg_isready -U healthchecks -d healthchecks"}) {
				t.Fatal("persistence, official non-root App layout or database readiness changed")
			}
			for key, value := range map[string]string{
				"DB": "postgres", "DB_HOST": "healthchecks-db", "DB_PORT": "5432", "DB_NAME": "healthchecks", "DB_USER": "healthchecks",
				"DB_PASSWORD": "${HEALTHCHECKS_DB_PASSWORD}", "SECRET_KEY": "${HEALTHCHECKS_SECRET_KEY}", "DEBUG": "False", "REGISTRATION_OPEN": "False",
				"INTEGRATIONS_ALLOW_PRIVATE_IPS": "False", "SITE_ROOT": scenario.root, "PING_ENDPOINT": scenario.ping,
			} {
				if web.Environment[key] != value {
					t.Errorf("environment %s = %q, want %q", key, web.Environment[key], value)
				}
			}
			if database.Environment["POSTGRES_PASSWORD"] != "${HEALTHCHECKS_DB_PASSWORD}" || !strings.Contains(web.Environment["ALLOWED_HOSTS"], scenario.host) || strings.Contains(web.Environment["ALLOWED_HOSTS"], "*") {
				t.Fatal("database credentials or explicit App hosts changed")
			}
			for _, forbidden := range []string{"EMAIL_HOST", "SMTPD_PORT", "REMOTE_USER_HEADER", "HEALTHCHECKS_HOMARR_API_KEY", "ADMIN_PASSWORD", "PUID", "PGID"} {
				if strings.Contains(fragment, forbidden) {
					t.Fatalf("deployment unexpectedly supplies %s", forbidden)
				}
			}
		})
	}
	info := (healthchecks.App{}).Info()
	if info.ID != "healthchecks" || info.ContainerPort != 8000 || info.Port != 8000 || info.Service != "healthchecks" || len(info.Description) > 100 || len((healthchecks.App{}).Links()) != 0 {
		t.Fatal("App metadata or independent App-only ownership changed")
	}
	if !reflect.DeepEqual((healthchecks.App{}).Secrets(), []app.SecretSpec{{Name: "db_password", Length: 48}, {Name: "secret_key", Length: 64}}) {
		t.Fatal("first-start secrets must be App-owned and stable through the existing declarer")
	}
}

func TestInputsRejectUnsafeAddressesAndProjectIDs(t *testing.T) {
	for _, scenario := range []struct{ key, value string }{
		{"project_id", "../settings"}, {"site_root", "https://user:password@example.invalid"},
		{"site_root", "https://example.invalid/subpath"}, {"site_root", "https://example.invalid?token=secret"},
		{"site_root", "https://example.invalid#fragment"}, {"site_root", "https://example.invalid:65536"},
		{"site_root", "file:///private/secret"}, {"site_root", "http://${ADMIN_PASSWORD}"},
		{"site_root", "http://example.invalid\nother: value"}, {"ping_endpoint", "https://example.invalid/ping"},
		{"ping_endpoint", "https://user:password@example.invalid/ping/"}, {"ping_endpoint", "https://example.invalid/ping/?key=secret"},
	} {
		t.Run(scenario.key+"/"+scenario.value, func(t *testing.T) {
			h := helpers.New(helpers.Config{App: "healthchecks", Log: events.Discard(), Inputs: map[string]string{scenario.key: scenario.value}})
			if err := (healthchecks.App{}).Preflight(t.Context(), h); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	for _, input := range (healthchecks.App{}).Inputs() {
		if input.Key == "homarr_api_key" && (!input.Private || !input.RecoveryOnly || input.Required) {
			t.Fatal("issued key must remain optional private recovery input, not a generated or public value")
		}
	}
}

func TestStandaloneProjectionKeepsOnlyDeploymentSecrets(t *testing.T) {
	entry, err := standalone.Project(healthchecks.App{})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Standalone || !strings.Contains(entry.Notes, "interactive createsuperuser") || !strings.Contains(entry.Notes, "never sends test pings") {
		t.Fatal("standalone guidance must name the real Manual boundaries")
	}
	generated := map[string]bool{}
	for _, value := range entry.Env {
		if strings.Contains(value.Key, "HOMARR") || value.Key == "ADMIN_PASSWORD" {
			t.Fatal("standalone deployment must not receive account or issued read-only credentials")
		}
		if value.Secret {
			generated[value.Key] = value.Bytes > 0 && !value.External
		}
	}
	if len(generated) != 2 || !generated["HEALTHCHECKS_SECRET_KEY"] || !generated["HEALTHCHECKS_DB_PASSWORD"] {
		t.Fatal("standalone deployment must generate only its database password and stable signing key")
	}
}

type secrets map[string]string

func (s secrets) Get(key string) string       { return s[key] }
func (s secrets) Set(key, value string) error { s[key] = value; return nil }

type fakeHealthchecks struct {
	mu                                      sync.Mutex
	t                                       *testing.T
	checks, badges, email, owned, badgePage string
	loginStatus, profileStatus, apiStatus   int
	channelsStatus                          int
	requests                                []string
	logins                                  int
	redirect                                string
	key                                     string
	slotPopulated                           bool
	slotReads, populateAt, issuances        int
	issuanceStatus                          int
	issuanceBody                            string
}

func newFake(t *testing.T) *fakeHealthchecks {
	return &fakeHealthchecks{
		t: t, email: "admin@selfhost.local", owned: projectID,
		checks:      `{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":0}]}`,
		badges:      `{"badges":{"*":{"svg":"` + badgeURL + `"}}}`,
		loginStatus: http.StatusFound, profileStatus: http.StatusOK, apiStatus: http.StatusOK, channelsStatus: http.StatusUnauthorized,
		key: apiKey, slotPopulated: true, issuanceStatus: http.StatusOK,
	}
}

func (f *fakeHealthchecks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Method == http.MethodPost && r.URL.Path == "/projects/"+projectID+"/settings/" {
		if r.ParseForm() != nil || r.Form.Get("create_key") != "api_key_readonly" || r.Form.Get("csrfmiddlewaretoken") != "synthetic-csrf" || len(r.Form) != 2 {
			f.t.Error("issuance must submit only the selected read-only key form")
		}
		if cookie, err := r.Cookie("csrftoken"); err != nil || cookie.Value != "synthetic-csrf" {
			f.t.Error("issuance did not retain the documented CSRF session")
		}
		if f.slotPopulated {
			f.t.Error("issuance replaced a populated read-only key slot")
		}
		f.issuances++
		f.slotPopulated = true
		f.key = "hcr_0123456789012345678901234567"
		w.WriteHeader(f.issuanceStatus)
		if f.issuanceBody != "" {
			io.WriteString(w, f.issuanceBody)
		} else {
			io.WriteString(w, healthchecksCreatedModal(f.key))
		}
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/accounts/login/" {
		f.logins++
		if r.ParseForm() != nil || r.Form.Get("action") != "login" || r.Form.Get("email") != "admin@selfhost.local" || r.Form.Get("password") != "synthetic-password" || r.Form.Get("csrfmiddlewaretoken") != "synthetic-csrf" {
			f.t.Error("login did not use the documented form fields and saved credentials")
		}
		if cookie, err := r.Cookie("csrftoken"); err != nil || cookie.Value != "synthetic-csrf" || !strings.HasSuffix(r.Header.Get("Referer"), "/accounts/login/") {
			f.t.Error("login did not retain CSRF cookie or referer")
		}
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "synthetic-session", Path: "/"})
		w.Header().Set("Location", "/projects/"+projectID+"/checks/")
		w.WriteHeader(f.loginStatus)
		return
	}
	if r.Method != http.MethodGet {
		f.t.Error("Setup attempted a write other than password authentication")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/health":
		io.WriteString(w, "OK")
	case "/accounts/login/":
		http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: "synthetic-csrf", Path: "/"})
		io.WriteString(w, `<input type="hidden" name="csrfmiddlewaretoken" value="synthetic-csrf">`)
	case "/accounts/profile/":
		if cookie, err := r.Cookie("sessionid"); err != nil || cookie.Value != "synthetic-session" {
			f.t.Error("profile read was not authenticated")
		}
		w.WriteHeader(f.profileStatus)
		fmt.Fprintf(w, `<h1 class="settings-title">Settings <small>%s</small></h1><table id="my-projects"><tr><td><a href="/projects/%s/settings/">Settings</a></td><td>Owner</td></tr></table>`, f.email, f.owned)
	case "/projects/" + projectID + "/badges/":
		if cookie, err := r.Cookie("sessionid"); err != nil || cookie.Value != "synthetic-session" {
			f.t.Error("selected project identity read was not authenticated")
		}
		if f.badgePage != "" {
			io.WriteString(w, f.badgePage)
			return
		}
		io.WriteString(w, `<div id="preview" class="col-md-8"><h2>Preview</h2><img src="`+badgeURL+`"></div>`)
	case "/projects/" + projectID + "/settings/":
		f.slotReads++
		if f.populateAt != 0 && f.slotReads >= f.populateAt {
			f.slotPopulated = true
		}
		http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: "synthetic-csrf", Path: "/"})
		io.WriteString(w, `<input type="hidden" name="csrfmiddlewaretoken" value="synthetic-csrf"><table id="api-keys"><tr><td>API key</td><td><code>unrelated-write-key</code></td></tr><tr><td>API key (read-only)</td><td>`)
		if f.slotPopulated {
			io.WriteString(w, `<code>masked-read-only-key</code><a data-revoke-key="api_key_readonly">Revoke</a>`)
		} else {
			io.WriteString(w, `<span class="not-set">not set</span><a data-create-key="api_key_readonly">Create</a>`)
		}
		io.WriteString(w, `</td></tr></table>`)
	case "/api/v3/checks/", "/api/v3/badges/", "/api/v3/channels/":
		if r.Header.Get("X-Api-Key") != f.key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.redirect != "" {
			w.Header().Set("Location", f.redirect)
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/api/v3/channels/" {
			w.WriteHeader(f.channelsStatus)
			return
		}
		w.WriteHeader(f.apiStatus)
		if r.URL.Path == "/api/v3/checks/" {
			io.WriteString(w, f.checks)
		} else {
			io.WriteString(w, f.badges)
		}
	default:
		f.t.Error("unexpected endpoint; checks, ping URLs and notification jobs must not be changed")
		w.WriteHeader(http.StatusNotFound)
	}
}

func harness(t *testing.T, fake *fakeHealthchecks) (*helpers.H, secrets, *[]helpers.StepResult) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	deps := systemtest.NewDeps(t)
	stored := secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password", "HEALTHCHECKS_HOMARR_API_KEY": apiKey, "HEALTHCHECKS_SECRET_KEY": "synthetic-app-secret"}
	stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] = healthchecksOwnership(apiKey)
	var records []helpers.StepResult
	h := helpers.New(helpers.Config{
		App: "healthchecks", Service: "healthchecks", BaseURL: server.URL, URL: server.URL,
		HTTP: server.Client(), Secrets: stored, Log: events.Discard(),
		Inputs: map[string]string{"project_id": projectID},
		Now:    deps.Now, Sleep: deps.Sleep, Record: func(result helpers.StepResult) { records = append(records, result) },
	})
	return h, stored, &records
}

func TestSetupCheckAndRerunPreserveChecksAndCredentials(t *testing.T) {
	for _, checks := range []string{
		`{"checks":[]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":0}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":0,"last_ping":null,"next_ping":null}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"paused","n_pings":7}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"paused","n_pings":0,"last_ping":null,"next_ping":null}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"up","n_pings":1}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"down","n_pings":7},{"unique_key":"1123456789012345678901234567890123456789","status":"grace","n_pings":8}]}`,
	} {
		var decoded struct{ Checks []struct{ Status string } }
		if err := json.Unmarshal([]byte(checks), &decoded); err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprint(decoded.Checks), func(t *testing.T) {
			fake := newFake(t)
			fake.checks = checks
			h, stored, records := harness(t, fake)
			before := maps.Clone(stored)
			for range 2 {
				if err := (healthchecks.App{}).Setup(t.Context(), h); err != nil {
					t.Fatal(err)
				}
				if err := (healthchecks.App{}).Check(t.Context(), h); err != nil {
					t.Fatal(err)
				}
			}
			fake.mu.Lock()
			logins, preserved := fake.logins, fake.checks == checks
			fake.mu.Unlock()
			if !maps.Equal(stored, before) || logins != 1 || !preserved || len(*records) != 4 {
				t.Fatal("rerun changed credentials/checks, repeated rate-limited login or lost steps")
			}
			for _, result := range *records {
				if result.Status != stepstatus.StepSkip {
					t.Fatal("verified existing state should skip without mutation")
				}
			}
		})
	}
}

func TestMissingOrAmbiguousPrerequisitesRemainManual(t *testing.T) {
	for _, scenario := range []struct {
		name, hint string
		change     func(*fakeHealthchecks, *helpers.H, secrets)
	}{
		{"fresh or rejected login", "account bootstrap", func(f *fakeHealthchecks, _ *helpers.H, _ secrets) { f.loginStatus = http.StatusOK }},
		{"missing login", "account bootstrap", func(_ *fakeHealthchecks, _ *helpers.H, s secrets) { delete(s, "ADMIN_PASSWORD") }},
		{"MFA", "account bootstrap", func(f *fakeHealthchecks, _ *helpers.H, _ secrets) { f.profileStatus = http.StatusFound }},
		{"wrong account", "account bootstrap", func(f *fakeHealthchecks, _ *helpers.H, _ secrets) { f.email = "different@example.invalid" }},
		{"no chosen project", "exact project", func(_ *fakeHealthchecks, h *helpers.H, _ secrets) { delete(h.Inputs, "project_id") }},
		{"other owned project", "exact project", func(f *fakeHealthchecks, _ *helpers.H, _ secrets) { f.owned = "37d76ba7-a070-4fe1-89ba-20c425a30579" }},
		{"missing key", "issuance", func(_ *fakeHealthchecks, _ *helpers.H, s secrets) { delete(s, "HEALTHCHECKS_HOMARR_API_KEY") }},
		{"rejected key", "issuance", func(_ *fakeHealthchecks, _ *helpers.H, s secrets) { s["HEALTHCHECKS_HOMARR_API_KEY"] = "rejected-key" }},
		{"write key on empty project", "read-only", func(f *fakeHealthchecks, _ *helpers.H, _ secrets) {
			f.checks = `{"checks":[]}`
			f.channelsStatus = http.StatusOK
		}},
		{"wrong API project", "selected project's", func(f *fakeHealthchecks, _ *helpers.H, _ secrets) {
			f.badges = `{"badges":{"*":{"svg":"http://healthchecks.example.invalid/badge/another-project/abcDEF12-2.svg"}}}`
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake := newFake(t)
			h, stored, records := harness(t, fake)
			scenario.change(fake, h, stored)
			before := maps.Clone(stored)
			err := (healthchecks.App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) || len(*records) != 2 || (*records)[1].Status != stepstatus.StepManual || !strings.Contains((*records)[1].Hint, scenario.hint) {
				t.Fatalf("missing prerequisite did not retain its targeted Manual recovery: %v", err)
			}
			if err := (healthchecks.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("Check did not retain Manual: %v", err)
			}
			if !maps.Equal(stored, before) {
				t.Fatal("Manual recovery changed stored credentials")
			}
		})
	}
}

func TestRejectsMissingMalformedAndWriteCapableData(t *testing.T) {
	valid := `{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":0}`
	for _, checks := range []string{
		`{}`, `null`, `{"checks":null}`, `{"checks":[],"error":"synthetic-password"}`, `<html>synthetic-password</html>`,
		`{"checks":[{}]}`, `{"checks":[{"unique_key":"invalid","status":"new","n_pings":0}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"unknown","n_pings":0}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"up","n_pings":-1}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"up"}]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":null}]}`,
		`{"checks":[` + valid + `,` + valid + `]}`,
		`{"checks":[{"unique_key":"0123456789012345678901234567890123456789","status":"new","n_pings":0,"ping_url":"synthetic-password"}]}`,
	} {
		t.Run(fmt.Sprintf("checks-%d", len(checks)), func(t *testing.T) {
			fake := newFake(t)
			fake.checks = checks
			h, _, records := harness(t, fake)
			if err := (healthchecks.App{}).Setup(t.Context(), h); err == nil || strings.Contains(err.Error(), "synthetic-password") {
				t.Fatal("invalid data was accepted or leaked")
			}
			if strings.Contains(fmt.Sprint(*records), "synthetic-password") {
				t.Fatal("step records leaked response data")
			}
		})
	}
	for _, badges := range []string{`{}`, `null`, `{"badges":null}`, `{"badges":{}}`, `{"badges":{"*":{"svg":"` + badgeURL + `"}},"error":"synthetic-password"}`} {
		fake := newFake(t)
		fake.badges = badges
		h, _, _ := harness(t, fake)
		if err := (healthchecks.App{}).Check(t.Context(), h); err == nil || strings.Contains(err.Error(), "synthetic-password") {
			t.Fatal("invalid badge identity was accepted or leaked")
		}
	}
}

func TestAPIAndSessionRedirectsDoNotProveAcceptance(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusFound, http.StatusInternalServerError} {
		fake := newFake(t)
		fake.apiStatus = status
		h, _, _ := harness(t, fake)
		if err := (healthchecks.App{}).Check(t.Context(), h); err == nil {
			t.Fatalf("HTTP %d incorrectly proved authenticated data", status)
		}
	}
	for _, body := range []string{`<html>login</html>`, `<div id="preview"><img src="file:///private/secret"></div>`, `<div id="preview"><img src="http://healthchecks.example.invalid/ping/real-job"></div>`} {
		fake := newFake(t)
		fake.badgePage = body
		h, _, _ := harness(t, fake)
		if err := (healthchecks.App{}).Check(t.Context(), h); err == nil {
			t.Fatal("unidentified project page was accepted")
		}
	}
	forwarded := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded = true }))
	defer other.Close()
	fake := newFake(t)
	fake.redirect = other.URL
	h, _, _ := harness(t, fake)
	if err := (healthchecks.App{}).Check(t.Context(), h); err == nil || forwarded {
		t.Fatal("redirect forwarded credentials or was accepted")
	}
}
