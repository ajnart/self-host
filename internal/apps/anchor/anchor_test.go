package anchor_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/anchor"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

const (
	userJSON  = `{"id":"existing-admin","email":"admin@selfhost.local","isAdmin":true,"status":"active"}`
	loginJSON = `{"access_token":"synthetic-login-jwt","refresh_token":"synthetic-refresh-token","user":` + userJSON + `}`
	noteJSON  = `{"id":"existing-note","title":"private-note-title","content":"private-note-content","updatedAt":"2025-01-02T03:04:05.123Z","isPinned":false,"tagIds":[],"permission":"owner"}`
)

type secrets struct {
	values  map[string]string
	durable map[string]string
	writes  []string
	fail    bool
}

func newSecrets() *secrets {
	values := map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password",
		"ANCHOR_HOMARR_API_KEY": "preserved-permanent-token", "UNRELATED_SECRET": "preserved-secret",
	}
	return &secrets{values: values, durable: maps.Clone(values)}
}

func (s *secrets) Get(key string) string { return s.values[key] }

func (s *secrets) Set(key, value string) error {
	s.values[key] = value
	s.writes = append(s.writes, key)
	if s.fail {
		return errors.New("synthetic persistence failure")
	}
	s.durable = maps.Clone(s.values)
	return nil
}

type reply struct {
	status int
	body   string
	header http.Header
	err    error
}

type fakeAnchor struct {
	replies    map[string]reply
	calls      []string
	unexpected []string
	email      string
	password   string
}

func newFake() *fakeAnchor {
	return &fakeAnchor{
		email: "admin@selfhost.local", password: "synthetic-password",
		replies: map[string]reply{
			"GET /api/health":        {status: http.StatusOK, body: `{"app":"anchor","status":"ok","version":"0.17.2"}`},
			"POST /api/auth/login":   {status: http.StatusOK, body: loginJSON},
			"GET /api/auth/me":       {status: http.StatusOK, body: userJSON},
			"GET /api/notes?limit=1": {status: http.StatusOK, body: "[" + noteJSON + "]"},
		},
	}
}

func (f *fakeAnchor) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.Method + " " + r.URL.RequestURI()
	f.calls = append(f.calls, key)
	response, exists := f.replies[key]
	if !exists || r.URL.Host != "anchor.invalid" {
		f.unexpected = append(f.unexpected, key)
		response = reply{status: http.StatusNotFound}
	}
	if key == "POST /api/auth/login" {
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["email"] != f.email || body["password"] != f.password {
			f.unexpected = append(f.unexpected, "wrong login selection")
			response = reply{status: http.StatusUnauthorized}
		}
	}
	if key == "GET /api/auth/me" || key == "GET /api/notes?limit=1" {
		if r.Header.Get("Authorization") != "Bearer synthetic-login-jwt" {
			f.unexpected = append(f.unexpected, "non-JWT authentication")
			response = reply{status: http.StatusUnauthorized}
		}
	}
	if response.err != nil {
		return nil, response.err
	}
	return &http.Response{
		StatusCode: response.status, Header: response.header,
		Body: io.NopCloser(strings.NewReader(response.body)), Request: r,
	}, nil
}

func (f *fakeAnchor) count(key string) int {
	count := 0
	for _, call := range f.calls {
		if call == key {
			count++
		}
	}
	return count
}

type harness struct {
	h       *helpers.H
	results []helpers.StepResult
	events  []events.Event
}

func newHarness(t *testing.T, fake *fakeAnchor, sec *secrets) *harness {
	t.Helper()
	box := &harness{}
	clock := systemtest.NewClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	box.h = helpers.New(helpers.Config{
		App: "anchor", Service: "anchor", ContainerPort: 3000,
		URL: "http://anchor.invalid", BaseURL: "http://anchor.invalid",
		Secrets: sec, HTTP: &http.Client{Transport: fake},
		Now: clock.Now, Sleep: clock.Sleep,
		Log:    events.New(events.SinkFunc(func(e events.Event) { box.events = append(box.events, e) })),
		Record: func(result helpers.StepResult) { box.results = append(box.results, result) },
	})
	t.Cleanup(func() {
		if len(fake.unexpected) != 0 {
			t.Errorf("unexpected requests: %v", fake.unexpected)
		}
		for _, key := range sec.writes {
			if key != "ANCHOR_ADMIN_IDENTITY" {
				t.Errorf("unexpected private write: %s", key)
			}
		}
	})
	return box
}

func (b *harness) step(id string) helpers.StepResult {
	for i := len(b.results) - 1; i >= 0; i-- {
		if b.results[i].ID == id {
			return b.results[i]
		}
	}
	return helpers.StepResult{}
}

func (b *harness) assertPrivate(t *testing.T, err error) {
	t.Helper()
	output, marshalErr := json.Marshal(struct {
		Results []helpers.StepResult
		Events  []events.Event
	}{b.results, b.events})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	text := string(output)
	if err != nil {
		text += err.Error()
	}
	for _, value := range []string{"synthetic-password", "synthetic-login-jwt", "synthetic-refresh-token", "preserved-permanent-token", "private-note-title", "private-note-content", "upstream-private-error"} {
		if strings.Contains(text, value) {
			t.Errorf("private value appeared in diagnostics: %s", value)
		}
	}
}

func TestOfficialComposeAndPrivateRecoveryContract(t *testing.T) {
	a := anchor.App{}
	info := a.Info()
	if info.ID != "anchor" || info.Service != "anchor" || info.Port != 3000 || info.ContainerPort != 3000 || info.ContainerPortFor(3456) != 3000 || info.Category != "Productivity" || info.Image != "ghcr.io/zhfahim/anchor:latest" {
		t.Fatalf("incorrect official App metadata: %+v", info)
	}
	if len(info.Description) > 100 || len(a.Links()) != 0 || a.AdminEmail("") != "" || a.AdminEmail("admin") != "admin@selfhost.local" {
		t.Fatal("invalid App-only metadata or administrator email contract")
	}
	for _, access := range []struct{ exposure, address string }{{"lan", "${LAN_IP}"}, {"local", "127.0.0.1"}} {
		compose := a.Compose(app.RenderContext{Port: 3456, Settings: map[string]string{"EXPOSURE": access.exposure}})
		for _, want := range []string{"image: " + info.Image, "restart: unless-stopped", "./data:/data", "${BIND_IP}:3456:3000", "APP_URL: http://" + access.address + ":3456"} {
			if !strings.Contains(compose, want) {
				t.Errorf("Compose lacks %q", want)
			}
		}
		for _, forbidden := range []string{"5432", "3001", "docker.sock", "privileged:", "user:", "PUID", "PGID", "JWT_SECRET", "ADMIN_PASSWORD", "USER_SIGNUP", "PG_HOST", "env_file:"} {
			if strings.Contains(compose, forbidden) {
				t.Errorf("Compose introduces unsupported or unnecessary %q", forbidden)
			}
		}
		if strings.Count(compose, "image:") != 1 {
			t.Fatal("the official image already includes its database")
		}
	}
	inputs := a.Inputs()
	if len(inputs) != 2 || inputs[0].Key != "admin_email" || inputs[1].Key != "admin_identity" {
		t.Fatal("unexpected recovery inputs")
	}
	for _, input := range inputs {
		if !input.Private || !input.RecoveryOnly || input.Required || !strings.Contains(input.Help, "preserve saved values") {
			t.Fatalf("unsafe recovery input %s", input.Key)
		}
	}
	for _, want := range []string{"PostgreSQL", "./data", "first administrator", "Registration is enabled by default", "APP_URL"} {
		if !strings.Contains(a.StandaloneGuide(), want) {
			t.Errorf("standalone guidance lacks %q", want)
		}
	}
}

func TestExistingAdministratorIsVerifiedAndSelectedWithoutOwningOrChangingIt(t *testing.T) {
	fake, sec := newFake(), newSecrets()
	before := maps.Clone(sec.values)
	a := anchor.App{}
	box := newHarness(t, fake, sec)
	if err := a.Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	wantIdentity := `{"id":"existing-admin","email":"admin@selfhost.local"}`
	if sec.durable["ANCHOR_ADMIN_IDENTITY"] != wantIdentity || len(sec.writes) != 1 {
		t.Fatal("the verified existing identity was not recorded exactly once")
	}
	meBefore := fake.count("GET /api/auth/me")
	if err := a.Check(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.count("POST /api/auth/login") != 1 || fake.count("GET /api/auth/me") <= meBefore {
		t.Fatal("Check did not reuse the invocation login with fresh identity verification")
	}
	box.assertPrivate(t, nil)
	box = newHarness(t, fake, sec)
	if err := a.Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.count("POST /api/auth/login") != 2 || len(sec.writes) != 1 || box.step("admin-identity").Status != stepstatus.StepSkip {
		t.Fatal("a new invocation reused stale authentication or rewrote the saved selection")
	}
	for key, value := range before {
		if sec.values[key] != value {
			t.Errorf("changed unrelated credential %s", key)
		}
	}
	box.assertPrivate(t, nil)
}

func TestLoginFailuresRemainPersistedManualWithoutFirstAccountOrTokenMutation(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		reply reply
	}{
		{"rejected", reply{status: http.StatusUnauthorized, body: "upstream-private-error"}},
		{"rate-limited", reply{status: http.StatusTooManyRequests}},
		{"unavailable", reply{status: http.StatusServiceUnavailable}},
		{"transport", reply{err: errors.New("upstream-private-error")}},
		{"redirect", reply{status: http.StatusTemporaryRedirect, header: http.Header{"Location": {"http://other.invalid/login"}}}},
		{"html", reply{status: http.StatusOK, body: "<html>upstream-private-error</html>"}},
		{"truncated", reply{status: http.StatusOK, body: `{"access_token":"synthetic-login-jwt",`}},
		{"null", reply{status: http.StatusOK, body: "null"}},
		{"missing-token", reply{status: http.StatusOK, body: `{"user":` + userJSON + `}`}},
		{"missing-user", reply{status: http.StatusOK, body: `{"access_token":"synthetic-login-jwt"}`}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake, sec := newFake(), newSecrets()
			fake.replies["POST /api/auth/login"] = scenario.reply
			before := maps.Clone(sec.values)
			for range 2 {
				box := newHarness(t, fake, sec)
				err := (anchor.App{}).Setup(context.Background(), box.h)
				if !errors.Is(err, helpers.ErrManual) || box.step("admin").Status != stepstatus.StepManual {
					t.Fatalf("rejected login did not persist Manual: %v", err)
				}
				for _, want := range []string{"genuinely new", "restore", "install --set-file", "separately approve replacement", "do not register a replacement", "self-host setup anchor"} {
					if !strings.Contains(box.step("admin").Hint, want) {
						t.Errorf("Manual recovery lacks %q", want)
					}
				}
				box.assertPrivate(t, err)
			}
			box := newHarness(t, fake, sec)
			if err := (anchor.App{}).Check(context.Background(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("rejected login passed Check: %v", err)
			}
			if len(sec.writes) != 0 || !maps.Equal(before, sec.values) || fake.count("GET /api/notes?limit=1") != 0 {
				t.Fatal("failed login changed private state or used notes as identity")
			}
		})
	}
}

func TestIdentityRequiresMatchingExplicitActiveAdministrator(t *testing.T) {
	for _, route := range []string{"POST /api/auth/login", "GET /api/auth/me"} {
		for _, scenario := range []struct{ name, user string }{
			{"missing-id", `{"email":"admin@selfhost.local","isAdmin":true,"status":"active"}`},
			{"blank-id", `{"id":" ","email":"admin@selfhost.local","isAdmin":true,"status":"active"}`},
			{"different-email", `{"id":"existing-admin","email":"someone@selfhost.local","isAdmin":true,"status":"active"}`},
			{"non-admin", `{"id":"existing-admin","email":"admin@selfhost.local","isAdmin":false,"status":"active"}`},
			{"missing-role", `{"id":"existing-admin","email":"admin@selfhost.local","status":"active"}`},
			{"pending", `{"id":"existing-admin","email":"admin@selfhost.local","isAdmin":true,"status":"pending"}`},
			{"missing-status", `{"id":"existing-admin","email":"admin@selfhost.local","isAdmin":true}`},
			{"null", "null"},
		} {
			t.Run(route+"/"+scenario.name, func(t *testing.T) {
				fake, sec := newFake(), newSecrets()
				body := scenario.user
				if route == "POST /api/auth/login" {
					body = `{"access_token":"synthetic-login-jwt","refresh_token":"synthetic-refresh-token","user":` + body + `}`
				}
				fake.replies[route] = reply{status: http.StatusOK, body: body}
				fake.replies["GET /api/notes?limit=1"] = reply{status: http.StatusOK, body: "[]"}
				box := newHarness(t, fake, sec)
				err := (anchor.App{}).Setup(context.Background(), box.h)
				if !errors.Is(err, helpers.ErrManual) || box.step("admin").Status != stepstatus.StepManual || len(sec.writes) != 0 || fake.count("GET /api/notes?limit=1") != 0 {
					t.Fatalf("unverified identity reached an empty notes success: %v", err)
				}
				box.assertPrivate(t, err)
			})
		}
	}
}

func TestSavedIdentityAndPrivateEmailSelectionsArePreserved(t *testing.T) {
	for _, scenario := range []struct{ name, selection, email string }{
		{"malformed", "{", ""},
		{"null", "null", ""},
		{"missing-id", `{"email":"admin@selfhost.local"}`, ""},
		{"different-id", `{"id":"another-account","email":"admin@selfhost.local"}`, ""},
		{"different-email", `{"id":"existing-admin","email":"another@selfhost.local"}`, ""},
		{"invalid-email", "", "not-an-email"},
		{"display-name", "", "Administrator <admin@selfhost.local>"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake, sec := newFake(), newSecrets()
			sec.values["ANCHOR_ADMIN_IDENTITY"] = scenario.selection
			sec.values["ANCHOR_ADMIN_EMAIL"] = scenario.email
			before := maps.Clone(sec.values)
			box := newHarness(t, fake, sec)
			err := (anchor.App{}).Setup(context.Background(), box.h)
			if !errors.Is(err, helpers.ErrManual) || box.step("admin").Status != stepstatus.StepManual || !maps.Equal(before, sec.values) || len(sec.writes) != 0 {
				t.Fatalf("a conflicting private selection was adopted or not persisted as Manual: %v", err)
			}
			if fake.count("GET /api/notes?limit=1") != 0 {
				t.Fatal("conflicting identity reached note access")
			}
		})
	}
	fake, sec := newFake(), newSecrets()
	fake.email = "selected@example.test"
	fake.replies["POST /api/auth/login"] = reply{status: http.StatusOK, body: strings.ReplaceAll(loginJSON, "admin@selfhost.local", fake.email)}
	fake.replies["GET /api/auth/me"] = reply{status: http.StatusOK, body: strings.ReplaceAll(userJSON, "admin@selfhost.local", fake.email)}
	sec.values["ANCHOR_ADMIN_EMAIL"] = fake.email
	sec.values["ANCHOR_ADMIN_IDENTITY"] = `{"id":"existing-admin","email":"selected@example.test"}`
	before := maps.Clone(sec.values)
	box := newHarness(t, fake, sec)
	box.h.Inputs["admin_email"] = "public-input-must-not-select@example.test"
	if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil || len(sec.writes) != 0 || !maps.Equal(before, sec.values) {
		t.Fatalf("known private recovery selection was not reused intact: %v", err)
	}
}

func TestCurrentIdentityMustMatchLoginEvenBeforeInitialSelection(t *testing.T) {
	fake, sec := newFake(), newSecrets()
	fake.replies["GET /api/auth/me"] = reply{status: http.StatusOK, body: strings.ReplaceAll(userJSON, "existing-admin", "different-current-account")}
	box := newHarness(t, fake, sec)
	if err := (anchor.App{}).Setup(context.Background(), box.h); !errors.Is(err, helpers.ErrManual) || len(sec.writes) != 0 {
		t.Fatalf("different current identity was recorded: %v", err)
	}
}

func TestFreshIdentityReadbackRejectsReplacementAfterSuccessfulSetup(t *testing.T) {
	fake, sec := newFake(), newSecrets()
	box := newHarness(t, fake, sec)
	a := anchor.App{}
	if err := a.Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(sec.values)
	notesBefore := fake.count("GET /api/notes?limit=1")
	fake.replies["GET /api/auth/me"] = reply{status: http.StatusOK, body: strings.ReplaceAll(userJSON, "existing-admin", "replacement-account")}
	if err := a.Check(context.Background(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("cached login hid a changed account: %v", err)
	}
	if err := a.Setup(context.Background(), box.h); !errors.Is(err, helpers.ErrManual) || box.step("admin").Status != stepstatus.StepManual {
		t.Fatalf("rerun replaced a recorded identity: %v", err)
	}
	if !maps.Equal(before, sec.values) || len(sec.writes) != 1 || fake.count("GET /api/notes?limit=1") != notesBefore {
		t.Fatal("identity mismatch changed the selection or read notes using the replacement")
	}
}

func TestChangedOrMissingPasswordDoesNotReuseAnEarlierSuccessfulLogin(t *testing.T) {
	for _, password := range []string{"", "changed-password"} {
		t.Run(password, func(t *testing.T) {
			fake, sec := newFake(), newSecrets()
			box := newHarness(t, fake, sec)
			if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil {
				t.Fatal(err)
			}
			sec.values["ADMIN_PASSWORD"] = password
			fake.password = password
			if password != "" {
				fake.replies["POST /api/auth/login"] = reply{status: http.StatusUnauthorized}
			}
			before := maps.Clone(sec.values)
			if err := (anchor.App{}).Check(context.Background(), box.h); !errors.Is(err, helpers.ErrManual) || !maps.Equal(before, sec.values) {
				t.Fatalf("changed login reused earlier authentication: %v", err)
			}
		})
	}
}

func TestHealthMustIdentifyAnchorBeforeSendingCredentials(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		reply reply
	}{
		{"unavailable", reply{status: http.StatusServiceUnavailable, body: "upstream-private-error"}},
		{"redirect", reply{status: http.StatusTemporaryRedirect, header: http.Header{"Location": {"http://other.invalid/health"}}}},
		{"unrelated", reply{status: http.StatusOK, body: `{"app":"not-anchor","status":"ok","version":"0.17.2"}`}},
		{"missing-version", reply{status: http.StatusOK, body: `{"app":"anchor","status":"ok"}`}},
		{"missing-status", reply{status: http.StatusOK, body: `{"app":"anchor","version":"0.17.2"}`}},
		{"invalid", reply{status: http.StatusOK, body: "upstream-private-error"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake, sec := newFake(), newSecrets()
			fake.replies["GET /api/health"] = scenario.reply
			box := newHarness(t, fake, sec)
			err := (anchor.App{}).Setup(context.Background(), box.h)
			if err == nil || fake.count("POST /api/auth/login") != 0 || len(sec.writes) != 0 {
				t.Fatalf("unidentified endpoint received credentials: %v", err)
			}
			box.assertPrivate(t, err)
		})
	}
}

func TestNoteAccessAcceptsExplicitEmptyAndValidMetadataOnly(t *testing.T) {
	for _, scenario := range []struct {
		name string
		body string
		ok   bool
	}{
		{"empty", "[]", true},
		{"owner", "[" + noteJSON + "]", true},
		{"viewer", "[" + strings.ReplaceAll(noteJSON, `"owner"`, `"viewer"`) + "]", true},
		{"editor", "[" + strings.ReplaceAll(noteJSON, `"owner"`, `"editor"`) + "]", true},
		{"untitled", "[" + strings.ReplaceAll(noteJSON, `"private-note-title"`, `""`) + "]", true},
		{"null", "null", false},
		{"missing", "{}", false},
		{"incomplete", "[{}]", false},
		{"null-entry", "[null]", false},
		{"invalid", "upstream-private-error", false},
		{"null-title", "[" + strings.ReplaceAll(noteJSON, `"private-note-title"`, "null") + "]", false},
		{"missing-boolean", "[" + strings.ReplaceAll(noteJSON, `"isPinned":false,`, "") + "]", false},
		{"null-tags", "[" + strings.ReplaceAll(noteJSON, `"tagIds":[]`, `"tagIds":null`) + "]", false},
		{"null-tag", "[" + strings.ReplaceAll(noteJSON, `"tagIds":[]`, `"tagIds":[null]`) + "]", false},
		{"invalid-date", "[" + strings.ReplaceAll(noteJSON, "2025-01-02T03:04:05.123Z", "not-a-date") + "]", false},
		{"unknown-permission", "[" + strings.ReplaceAll(noteJSON, `"owner"`, `"admin"`) + "]", false},
		{"ignored-limit", "[" + noteJSON + "," + noteJSON + "]", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake, sec := newFake(), newSecrets()
			fake.replies["GET /api/notes?limit=1"] = reply{status: http.StatusOK, body: scenario.body}
			box := newHarness(t, fake, sec)
			err := (anchor.App{}).Setup(context.Background(), box.h)
			if (err == nil) != scenario.ok {
				t.Fatalf("Setup acceptance = %v, want %v: %v", err == nil, scenario.ok, err)
			}
			checkErr := (anchor.App{}).Check(context.Background(), box.h)
			if (checkErr == nil) != scenario.ok || fake.count("GET /api/auth/me") == 0 {
				t.Fatalf("Check did not verify identity and note semantics: %v", checkErr)
			}
			box.assertPrivate(t, err)
			box.assertPrivate(t, checkErr)
		})
	}
}

func TestIdentityReadbackAndNotesErrorsArePrivateManualBoundaries(t *testing.T) {
	for _, route := range []string{"GET /api/auth/me", "GET /api/notes?limit=1"} {
		for _, response := range []reply{
			{status: http.StatusForbidden, body: "upstream-private-error private-note-content synthetic-login-jwt"},
			{err: errors.New("upstream-private-error")},
			{status: http.StatusTemporaryRedirect, header: http.Header{"Location": {"http://other.invalid/private"}}},
		} {
			fake, sec := newFake(), newSecrets()
			fake.replies[route] = response
			box := newHarness(t, fake, sec)
			err := (anchor.App{}).Setup(context.Background(), box.h)
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("read failure did not remain Manual: %v", err)
			}
			box.assertPrivate(t, err)
		}
	}
}

func TestIdentityPersistenceFailureStopsThenReverifiesWithoutRemoteMutation(t *testing.T) {
	fake, sec := newFake(), newSecrets()
	sec.fail = true
	box := newHarness(t, fake, sec)
	err := (anchor.App{}).Setup(context.Background(), box.h)
	if !errors.Is(err, helpers.ErrStepFailed) || box.step("admin-identity").Status != stepstatus.StepFail || sec.durable["ANCHOR_ADMIN_IDENTITY"] != "" || fake.count("GET /api/notes?limit=1") != 0 {
		t.Fatalf("failed identity persistence passed Setup: %v", err)
	}
	sec.values = maps.Clone(sec.durable)
	sec.fail = false
	box = newHarness(t, fake, sec)
	if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil || fake.count("POST /api/auth/login") != 2 || sec.durable["ANCHOR_ADMIN_IDENTITY"] == "" {
		t.Fatalf("retry did not independently verify the intended existing identity: %v", err)
	}
}
