package mealie_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/mealie"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
)

const (
	mealieUserID      = "a2cc03be-55f7-4d96-a7f9-a1f63e084001"
	mealieGroupID     = "a2cc03be-55f7-4d96-a7f9-a1f63e084002"
	mealieHouseholdID = "a2cc03be-55f7-4d96-a7f9-a1f63e084003"
	mealieOtherID     = "a2cc03be-55f7-4d96-a7f9-a1f63e084004"
)

type mealieSecrets struct {
	values      map[string]string
	writes      int
	allowWrites bool
	failAt      int
}

func (s *mealieSecrets) Get(key string) string { return s.values[key] }
func (s *mealieSecrets) Set(key, value string) error {
	s.writes++
	if !s.allowWrites || s.writes == s.failAt {
		return errors.New("synthetic private storage failure: " + value)
	}
	s.values[key] = value
	return nil
}

type mealieFake struct {
	about           map[string]any
	login           map[string]any
	user            map[string]any
	household       map[string]any
	loginStatus     int
	userStatus      int
	householdStatus int
	redirect        string
	calls           map[string]int
	unexpected      []string
	issued          int
	issueBody       string
	issueStatus     int
	onIssue         func()
	rejectKey       bool
}

func newMealieFake() *mealieFake {
	return &mealieFake{
		about: map[string]any{"version": "3.30.0", "production": true, "demoStatus": false, "allowPasswordLogin": true},
		login: map[string]any{"access_token": "fixture-mealie-session", "token_type": "bearer"},
		user: map[string]any{
			"id": mealieUserID, "username": "owner", "email": "owner@example.test", "admin": true,
			"groupId": mealieGroupID, "householdId": mealieHouseholdID, "group": "Existing group", "household": "Existing household",
			"tokens": []any{map[string]any{"id": 7, "name": "self-host Homarr statistics", "createdAt": "2026-11-02T12:00:00Z"}},
		},
		household: map[string]any{"id": mealieHouseholdID, "groupId": mealieGroupID, "name": "Existing household", "group": "Existing group"},
		calls:     map[string]int{},
	}
}

func (f *mealieFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	f.calls[route]++
	switch route {
	case "GET /api/app/about":
		json.NewEncoder(w).Encode(f.about)
	case "POST /api/auth/token":
		if r.ParseForm() != nil || r.Form.Get("password") != "fixture-mealie-password" || r.Form.Get("username") != "owner" && r.Form.Get("username") != "owner@example.test" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			f.unexpected = append(f.unexpected, "wrong login request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.redirect != "" {
			w.Header().Set("Location", f.redirect)
		}
		if f.loginStatus != 0 {
			w.WriteHeader(f.loginStatus)
		}
		json.NewEncoder(w).Encode(f.login)
	case "GET /api/users/self", "GET /api/households/self":
		if f.rejectKey && r.Header.Get("Authorization") == "Bearer existing-unmodified-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-mealie-session" && r.Header.Get("Authorization") != "Bearer existing-unmodified-key" {
			f.unexpected = append(f.unexpected, "wrong bearer credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/users/self" {
			if f.userStatus != 0 {
				w.WriteHeader(f.userStatus)
			}
			json.NewEncoder(w).Encode(f.user)
			return
		}
		if f.householdStatus != 0 {
			w.WriteHeader(f.householdStatus)
		}
		json.NewEncoder(w).Encode(f.household)
	case "POST /api/users/api-tokens":
		var input map[string]string
		if r.Header.Get("Authorization") != "Bearer fixture-mealie-session" || json.NewDecoder(r.Body).Decode(&input) != nil || len(input) != 2 || input["name"] != "self-host Homarr statistics" || input["integrationId"] != "homarr" {
			f.unexpected = append(f.unexpected, "invalid issuance request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.onIssue != nil {
			f.onIssue()
		}
		f.issued++
		f.user["tokens"] = []any{map[string]any{"id": 7, "name": "self-host Homarr statistics", "createdAt": "2026-11-02T12:00:00Z"}}
		code := f.issueStatus
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
		if f.issueBody != "" {
			w.Write([]byte(f.issueBody))
			return
		}
		w.Write([]byte(`{"id":7,"name":"self-host Homarr statistics","createdAt":"2026-11-02T12:00:00Z","token":"existing-unmodified-key"}`))
	default:
		f.unexpected = append(f.unexpected, route)
		w.WriteHeader(http.StatusNotFound)
	}
}

type mealieHarness struct {
	h       *helpers.H
	secrets *mealieSecrets
	records []helpers.StepResult
	output  bytes.Buffer
}

func newMealieHarness(t *testing.T, fake *mealieFake) *mealieHarness {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	box := &mealieHarness{secrets: &mealieSecrets{values: map[string]string{
		"ADMIN_USER": "owner", "ADMIN_PASSWORD": "fixture-mealie-password",
		"MEALIE_ADMIN_USER_ID": mealieUserID, "MEALIE_GROUP_ID": mealieGroupID, "MEALIE_HOUSEHOLD_ID": mealieHouseholdID,
		"MEALIE_HOMARR_API_KEY":       "existing-unmodified-key",
		"MEALIE_HOMARR_KEY_OWNERSHIP": mealieOwnershipJSON(),
	}}}
	box.h = helpers.New(helpers.Config{
		App: "mealie", Service: "mealie", ContainerPort: 9000,
		BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(), Secrets: box.secrets,
		Log:    events.New(events.JSON(&box.output)),
		Record: func(r helpers.StepResult) { box.records = append(box.records, r) },
	})
	t.Cleanup(func() {
		if len(fake.unexpected) != 0 || !box.secrets.allowWrites && box.secrets.writes != 0 {
			t.Errorf("unexpected requests=%v, credential writes=%d", fake.unexpected, box.secrets.writes)
		}
	})
	return box
}

func mealieOwnershipJSON() string {
	sum := sha256.Sum256([]byte("existing-unmodified-key"))
	raw, _ := json.Marshal(map[string]any{
		"intent": map[string]any{"identity": map[string]string{"userId": mealieUserID, "groupId": mealieGroupID, "householdId": mealieHouseholdID}, "name": "self-host Homarr statistics", "integrationId": "homarr", "authority": "full-account-access"},
		"id":     7, "createdAt": "2026-11-02T12:00:00Z", "fingerprint": hex.EncodeToString(sum[:]),
	})
	return string(raw)
}

func TestMealieDeploymentContract(t *testing.T) {
	a := mealie.App{}
	info := a.Info()
	if info.ID != "mealie" || info.Service != "mealie" || info.Port != 9925 || info.ContainerPortFor(19925) != 9000 || info.Image != "ghcr.io/mealie-recipes/mealie:latest" || len(a.Links()) != 0 {
		t.Fatalf("unexpected App metadata: %+v", info)
	}
	compose := a.Compose(app.RenderContext{Port: 19925})
	for _, want := range []string{
		"image: " + info.Image, "${BIND_IP}:19925:9000", "./data:/app/data", "DB_ENGINE: sqlite",
		`ALLOW_SIGNUP: "false"`, "PUID: ${PUID}", "PGID: ${PGID}", "TZ: ${TZ}", "mem_limit: 1000m",
		"restart: unless-stopped", "http://127.0.0.1:9000/api/app/about",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("Compose is missing %q", want)
		}
	}
	for _, forbidden := range []string{"DEFAULT_EMAIL", "DEFAULT_PASSWORD", "SECRET_KEY", "ADMIN_PASSWORD", "MEALIE_HOMARR", "container_name:", "privileged:", "docker.sock", "--insecure"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("Compose includes forbidden %q", forbidden)
		}
	}
	if strings.Count(compose, "image:") != 1 {
		t.Fatal("SQLite App unexpectedly requires another service")
	}
	inputs := a.Inputs()
	if len(inputs) != 5 {
		t.Fatalf("private selections/recovery inputs = %d, want 5", len(inputs))
	}
	for _, input := range inputs {
		if !input.Private || input.Default != "" || input.Required {
			t.Errorf("selection must remain private and permit a truthful Manual first install: %+v", input)
		}
	}
	if !strings.Contains(a.StandaloneGuide(), "manually") || !strings.Contains(a.StandaloneGuide(), "SQLite") {
		t.Fatal("standalone guidance omits persistence or Manual initial-account boundary")
	}
}

func TestMealieSetupReusesExistingIdentityAndPreservesData(t *testing.T) {
	fake := newMealieFake()
	box := newMealieHarness(t, fake)
	before := maps.Clone(box.secrets.values)
	box.h.Dir = t.TempDir()
	data := filepath.Join(box.h.Dir, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(data, "mealie.db")
	retained := []byte("synthetic existing recipes, users and households; not a database fixture")
	if err := os.WriteFile(file, retained, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := (mealie.App{}).Setup(t.Context(), box.h); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	if err := (mealie.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if fake.calls["POST /api/auth/token"] != 1 || fake.calls["GET /api/users/self"] != 9 || fake.calls["GET /api/households/self"] != 6 {
		t.Fatalf("expected one in-memory login and fresh identity on each verification: %v", fake.calls)
	}
	for _, record := range box.records {
		if record.Status != status.StepSkip {
			t.Errorf("verified existing step = %s, want skip", record.Status)
		}
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, retained) || !maps.Equal(before, box.secrets.values) {
		t.Fatal("Setup changed existing storage or credentials")
	}
}

func TestMealieAmbiguousOrRejectedIdentityRemainsManual(t *testing.T) {
	cases := []struct {
		name   string
		change func(*mealieFake, *mealieHarness)
		hint   string
	}{
		{"missing selection", func(_ *mealieFake, b *mealieHarness) { delete(b.secrets.values, "MEALIE_HOUSEHOLD_ID") }, "explicitly selected"},
		{"invalid selection", func(_ *mealieFake, b *mealieHarness) { b.secrets.values["MEALIE_ADMIN_USER_ID"] = "unknown" }, "valid UUIDs"},
		{"missing password", func(_ *mealieFake, b *mealieHarness) { delete(b.secrets.values, "ADMIN_PASSWORD") }, "login is unavailable"},
		{"rejected login", func(f *mealieFake, _ *mealieHarness) { f.loginStatus = 401 }, "no factory credentials were tried"},
		{"locked login", func(f *mealieFake, _ *mealieHarness) { f.loginStatus = 403 }, "no factory credentials were tried"},
		{"factory account", func(f *mealieFake, _ *mealieHarness) { f.user["email"] = "changeme@example.com" }, "factory administrator"},
		{"different account", func(f *mealieFake, _ *mealieHarness) { f.user["id"] = mealieOtherID }, "does not match"},
		{"different username", func(f *mealieFake, _ *mealieHarness) { f.user["username"] = "another-owner" }, "does not match"},
		{"nonadmin", func(f *mealieFake, _ *mealieHarness) { f.user["admin"] = false }, "complete administrator"},
		{"null administrator", func(f *mealieFake, _ *mealieHarness) { f.user["admin"] = nil }, "complete administrator"},
		{"missing account field", func(f *mealieFake, _ *mealieHarness) { delete(f.user, "household") }, "complete administrator"},
		{"wrong user group", func(f *mealieFake, _ *mealieHarness) { f.user["groupId"] = mealieOtherID }, "does not match"},
		{"wrong user household", func(f *mealieFake, _ *mealieHarness) { f.user["householdId"] = mealieOtherID }, "does not match"},
		{"wrong household", func(f *mealieFake, _ *mealieHarness) { f.household["id"] = mealieOtherID }, "household information"},
		{"wrong household group", func(f *mealieFake, _ *mealieHarness) { f.household["groupId"] = mealieOtherID }, "household information"},
		{"wrong household name", func(f *mealieFake, _ *mealieHarness) { f.household["name"] = "Another household" }, "household information"},
		{"missing household group", func(f *mealieFake, _ *mealieHarness) { delete(f.household, "group") }, "household information"},
		{"rejected identity", func(f *mealieFake, _ *mealieHarness) { f.userStatus = 401 }, "authenticated identity"},
		{"rejected household", func(f *mealieFake, _ *mealieHarness) { f.householdStatus = 403 }, "administrator's household"},
		{"missing version", func(f *mealieFake, _ *mealieHarness) { delete(f.about, "version") }, "production API identity"},
		{"old API", func(f *mealieFake, _ *mealieHarness) { f.about["version"] = "2.8.0" }, "production API identity"},
		{"unreviewed major", func(f *mealieFake, _ *mealieHarness) { f.about["version"] = "4.0.0" }, "production API identity"},
		{"demo", func(f *mealieFake, _ *mealieHarness) { f.about["demoStatus"] = true }, "production API identity"},
		{"missing demo flag", func(f *mealieFake, _ *mealieHarness) { delete(f.about, "demoStatus") }, "production API identity"},
		{"password login disabled", func(f *mealieFake, _ *mealieHarness) { f.about["allowPasswordLogin"] = false }, "preserve the selected authentication method"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMealieFake()
			box := newMealieHarness(t, fake)
			tc.change(fake, box)
			before := maps.Clone(box.secrets.values)
			for _, verify := range []func() error{
				func() error { return (mealie.App{}).Setup(t.Context(), box.h) },
				func() error { return (mealie.App{}).Check(t.Context(), box.h) },
			} {
				err := verify()
				var manual *helpers.ManualError
				if !errors.Is(err, helpers.ErrManual) || !errors.As(err, &manual) || !strings.Contains(manual.Hint, tc.hint) || !strings.Contains(manual.Hint, "self-host setup mealie") {
					t.Fatalf("want actionable Manual %q, got %v", tc.hint, err)
				}
			}
			last := box.records[len(box.records)-1]
			if last.Status != status.StepManual || !strings.Contains(last.Hint, tc.hint) || !maps.Equal(before, box.secrets.values) {
				t.Fatal("Manual result lost its recovery hint or changed saved selections")
			}
			if fake.calls["POST /api/auth/token"] > 1 {
				t.Fatal("Manual verification retried the password within one invocation")
			}
		})
	}
}

func TestMealieRereadsIdentityWithAnExistingSession(t *testing.T) {
	fake := newMealieFake()
	box := newMealieHarness(t, fake)
	fake.about["version"] = "3.31.0"
	box.secrets.values["ADMIN_USER"] = "owner@example.test"
	if err := (mealie.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	fake.user["householdId"] = mealieOtherID
	if err := (mealie.App{}).Check(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("changed household passed with a remembered login: %v", err)
	}
	if fake.calls["POST /api/auth/token"] != 1 || fake.calls["GET /api/users/self"] != 4 {
		t.Fatal("identity was cached or login needlessly retried")
	}
}

func TestMealieRejectsMalformedSessionsWithoutLeakingBodies(t *testing.T) {
	for _, response := range []map[string]any{
		{"access_token": "fixture-mealie-session"},
		{"access_token": "fixture-mealie-session", "token_type": "basic"},
		{"access_token": nil, "token_type": "bearer"},
	} {
		fake := newMealieFake()
		box := newMealieHarness(t, fake)
		fake.login = response
		fake.login["detail"] = "remote-private-detail"
		err := (mealie.App{}).Setup(t.Context(), box.h)
		if err == nil || errors.Is(err, helpers.ErrManual) {
			t.Fatalf("malformed login must fail, got %v", err)
		}
		serialized, marshalErr := json.Marshal(box.records)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		output := box.output.String() + err.Error() + string(serialized)
		for _, private := range []string{"fixture-mealie-session", "fixture-mealie-password", "remote-private-detail", mealieUserID, mealieGroupID, mealieHouseholdID} {
			if strings.Contains(output, private) {
				t.Fatalf("private response content reached diagnostics: %q", private)
			}
		}
		if fake.calls["GET /api/users/self"] != 0 {
			t.Fatal("malformed session was used for an authenticated request")
		}
	}
}

func TestMealieDoesNotFollowCredentialRedirects(t *testing.T) {
	var foreignCalls int
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++ }))
	t.Cleanup(foreign.Close)
	fake := newMealieFake()
	fake.loginStatus, fake.redirect = http.StatusTemporaryRedirect, foreign.URL
	box := newMealieHarness(t, fake)
	if err := (mealie.App{}).Setup(t.Context(), box.h); err == nil {
		t.Fatal("redirected login was accepted")
	}
	if foreignCalls != 0 {
		t.Fatal("credentials were sent through a login redirect")
	}
}
