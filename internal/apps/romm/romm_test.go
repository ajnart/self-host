package romm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/romm"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type secrets map[string]string

func (s secrets) Get(key string) string       { return s[key] }
func (s secrets) Set(key, value string) error { s[key] = value; return nil }

type fakeRomM struct {
	mu              sync.Mutex
	user            string
	password        string
	id              int64
	role            string
	heartbeat       string
	identity        string
	createResponse  string
	authStatus      int
	anonymousStatus int
	createStatus    int
	postCreateID    int64
	postCreatePass  string
	missingCSRF     bool
	race            bool
	requests        int
	posts           int
	creates         int
}

func (f *fakeRomM) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		switch r.Method + " " + r.URL.Path {
		case "GET /api/heartbeat":
			if !f.missingCSRF {
				http.SetCookie(w, &http.Cookie{Name: "romm_csrftoken", Value: "synthetic-csrf", Path: "/"})
			}
			if f.heartbeat != "" {
				w.Write([]byte(f.heartbeat))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"SYSTEM": map[string]any{"VERSION": "5.4.1", "SHOW_SETUP_WIZARD": f.id == 0}})
		case "GET /api/users/me":
			user, password, supplied := r.BasicAuth()
			if !supplied {
				status := http.StatusUnauthorized
				if f.anonymousStatus != 0 {
					status = f.anonymousStatus
				}
				w.WriteHeader(status)
				w.Write([]byte(`{"detail":"Not authenticated"}`))
				return
			}
			if f.authStatus != 0 {
				w.Header().Set("Location", "/unapproved")
				w.WriteHeader(f.authStatus)
				w.Write([]byte(`{"detail":"private-upstream-diagnostic"}`))
				return
			}
			if f.id == 0 || user != f.user || password != f.password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if f.identity != "" {
				w.Write([]byte(f.identity))
				return
			}
			json.NewEncoder(w).Encode(f.account())
		case "POST /api/users":
			f.posts++
			if _, _, supplied := r.BasicAuth(); supplied {
				t.Error("first-admin creation must remain anonymous")
			}
			cookie, err := r.Cookie("romm_csrftoken")
			if err != nil || cookie.Value != "synthetic-csrf" || r.Header.Get("X-CSRFToken") != "synthetic-csrf" {
				t.Error("first-admin creation did not preserve the CSRF cookie/header")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if f.race || f.id != 0 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var body struct{ Username, Password, Email, Role string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Username == "" || body.Password == "" || body.Email != "" || body.Role != "admin" {
				t.Error("unexpected first-admin request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.createStatus != 0 {
				w.WriteHeader(f.createStatus)
				w.Write([]byte(`{"detail":"private-upstream-diagnostic"}`))
				return
			}
			f.user, f.password, f.id, f.role = body.Username, body.Password, 37, "admin"
			f.creates++
			w.WriteHeader(http.StatusCreated)
			if f.createResponse != "" {
				w.Write([]byte(f.createResponse))
			} else {
				json.NewEncoder(w).Encode(f.account())
			}
			if f.postCreateID != 0 {
				f.id = f.postCreateID
			}
			if f.postCreatePass != "" {
				f.password = f.postCreatePass
			}
		default:
			t.Errorf("unapproved RomM request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeRomM) account() map[string]any {
	role := f.role
	if role == "" {
		role = "admin"
	}
	return map[string]any{"id": f.id, "username": f.user, "enabled": true, "role": role}
}

type harness struct {
	h       *helpers.H
	results []helpers.StepResult
	output  bytes.Buffer
}

func newHarness(t *testing.T, fake *fakeRomM, sec secrets) *harness {
	t.Helper()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	deps := systemtest.NewDeps(t)
	box := &harness{}
	box.h = helpers.New(helpers.Config{
		App: "romm", Service: "romm", ContainerPort: 8080,
		BaseURL: server.URL, URL: server.URL, HTTP: server.Client(),
		Secrets: sec, Log: events.New(events.JSON(&box.output)), Now: deps.Now, Sleep: deps.Sleep,
		Record: func(result helpers.StepResult) { box.results = append(box.results, result) },
	})
	return box
}

func adminSecrets() secrets {
	return secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-admin-password", "ROMM_AUTH_SECRET": "unchanged-signing-key"}
}

func requireManual(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("expected actionable Manual recovery, got %v", err)
	}
	var manual *helpers.ManualError
	if !errors.As(err, &manual) || !strings.Contains(manual.Hint, "self-host") {
		t.Fatalf("missing Manual recovery hint: %v", err)
	}
}

func TestOfficialDefinitionKeepsSecretsStorageAndAccessScoped(t *testing.T) {
	a := romm.App{}
	info := a.Info()
	if info.ID != "romm" || info.Image != "rommapp/romm:latest" || info.Service != "romm" || info.ContainerPort != 8080 || !info.UsesMedia {
		t.Fatalf("unexpected RomM metadata: %+v", info)
	}
	compose := a.Compose(app.RenderContext{Port: 18080})
	for _, expected := range []string{
		"image: rommapp/romm:latest", "image: mariadb:11", "${BIND_IP}:18080:8080",
		"ROMM_AUTH_SECRET_KEY: ${ROMM_AUTH_SECRET}", "DB_PASSWD: ${ROMM_DB_PASSWORD}",
		"MARIADB_PASSWORD: ${ROMM_DB_PASSWORD}", "MARIADB_ROOT_PASSWORD: ${ROMM_DB_ROOT_PASSWORD}",
		"./data:/romm", "./redis:/redis-data", "./database:/var/lib/mysql", "source: ${MEDIA_ROOT}/romm",
		"target: /romm/library", "create_host_path: false", "condition: service_healthy", "--innodb_initialized",
		"KIOSK_MODE: \"false\"", "DISABLE_DOWNLOAD_ENDPOINT_AUTH: \"false\"", "DISABLE_CSRF_PROTECTION: \"false\"",
		"ENABLE_SCHEDULED_RESCAN: \"false\"", "ENABLE_RESCAN_ON_FILESYSTEM_CHANGE: \"false\"",
		"ENABLE_SYNC_PUSH_PULL: \"false\"", "ENABLE_SCHEDULED_UPDATE_SWITCH_TITLEDB: \"false\"", "SENTRY_DSN: \"\"",
	} {
		if !strings.Contains(compose, expected) {
			t.Errorf("missing deployment contract %q", expected)
		}
	}
	for _, forbidden := range []string{"container_name:", "PUID", "PGID", "privileged:", "docker.sock", "6379:", "3306:", "env_file:"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("unexpected deployment grant %q", forbidden)
		}
	}
	if strings.Count(compose, "image:") != 2 || strings.Count(compose, "    ports:") != 1 || len(a.Links()) != 0 {
		t.Fatal("RomM must publish only its App port and must not add peer Links")
	}
	if got := a.Secrets(); !reflect.DeepEqual(got, []app.SecretSpec{{Name: "auth_secret", Length: 64, Hex: true}, {Name: "db_password", Length: 48}, {Name: "db_root_password", Length: 48}}) {
		t.Fatalf("unexpected first-start secret declarations: %+v", got)
	}
}

func TestLibraryPreflightDoesNotCreateOrChangeDirectories(t *testing.T) {
	root := t.TempDir()
	h := &helpers.H{Config: helpers.Config{Settings: map[string]string{"MEDIA_ROOT": root}}}
	requireManual(t, (romm.App{}).Preflight(t.Context(), h))
	path := filepath.Join(root, "romm")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preflight created the library directory")
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (romm.App{}).Preflight(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("preflight changed the existing library directory")
	}
}

func TestSetupCreatesFirstAdminWithSourcedAbsenceAndCSRFThenReusesIt(t *testing.T) {
	fake := &fakeRomM{}
	sec := adminSecrets()
	before := map[string]string{}
	for key, value := range sec {
		before[key] = value
	}
	box := newHarness(t, fake, sec)
	a := romm.App{}
	if err := a.Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if err := a.Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	box.results = nil
	if err := a.Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.posts != 1 || fake.creates != 1 || fake.id != 37 {
		t.Fatalf("creation attempts=%d creates=%d id=%d", fake.posts, fake.creates, fake.id)
	}
	for _, result := range box.results {
		if result.Status != stepstatus.StepSkip {
			t.Errorf("rerun changed step %s: %s", result.ID, result.Status)
		}
	}
	if !reflect.DeepEqual(map[string]string(sec), before) {
		t.Fatal("Setup changed saved credentials or added a freshness journal")
	}
	if strings.Contains(box.output.String(), sec["ADMIN_PASSWORD"]) {
		t.Fatal("Setup exposed credentials")
	}
}

func TestExistingAdministratorIsVerifiedWithoutMutation(t *testing.T) {
	sec := adminSecrets()
	fake := &fakeRomM{id: 912, user: "admin", password: sec["ADMIN_PASSWORD"]}
	box := newHarness(t, fake, sec)
	if err := (romm.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if err := (romm.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.posts != 0 || fake.id != 912 {
		t.Fatal("Setup adopted or changed an existing administrator")
	}
}

func TestUnknownOrProtectedFirstRunNeverAuthorizesCreation(t *testing.T) {
	for name, heartbeat := range map[string]string{
		"existing admin": `{"SYSTEM":{"VERSION":"5.4.1","SHOW_SETUP_WIZARD":false}}`,
		"missing flag":   `{"SYSTEM":{"VERSION":"5.4.1"}}`,
		"null flag":      `{"SYSTEM":{"VERSION":"5.4.1","SHOW_SETUP_WIZARD":null}}`,
		"string flag":    `{"SYSTEM":{"VERSION":"5.4.1","SHOW_SETUP_WIZARD":"true"}}`,
		"missing system": `{}`,
		"html":           `<html>first-run page</html>`,
		"older version":  `{"SYSTEM":{"VERSION":"4.0.0","SHOW_SETUP_WIZARD":true}}`,
		"new major":      `{"SYSTEM":{"VERSION":"6.0.0","SHOW_SETUP_WIZARD":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeRomM{heartbeat: heartbeat}
			box := newHarness(t, fake, adminSecrets())
			requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
			if fake.posts != 0 {
				t.Fatal("ambiguous first-run response authorized account creation")
			}
		})
	}
}

func TestAuthenticationUncertaintyStopsBeforeCreation(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake := &fakeRomM{authStatus: status}
			box := newHarness(t, fake, adminSecrets())
			err := (romm.App{}).Setup(t.Context(), box.h)
			requireManual(t, err)
			if fake.posts != 0 || strings.Contains(err.Error()+box.output.String(), "private-upstream-diagnostic") {
				t.Fatal("authentication uncertainty triggered creation or exposed a response")
			}
		})
	}
}

func TestAnonymousOrKioskAccessCannotProveAnAdministrator(t *testing.T) {
	fake := &fakeRomM{anonymousStatus: http.StatusOK}
	box := newHarness(t, fake, adminSecrets())
	requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
	requireManual(t, (romm.App{}).Check(t.Context(), box.h))
	if fake.posts != 0 {
		t.Fatal("anonymous access authorized creation")
	}
}

func TestIdentityRequiresExplicitEnabledAdminAndPositiveID(t *testing.T) {
	for name, identity := range map[string]string{
		"missing id":      `{"username":"admin","enabled":true,"role":"admin"}`,
		"zero id":         `{"id":0,"username":"admin","enabled":true,"role":"admin"}`,
		"fractional id":   `{"id":1.5,"username":"admin","enabled":true,"role":"admin"}`,
		"wrong username":  `{"id":37,"username":"other","enabled":true,"role":"admin"}`,
		"disabled":        `{"id":37,"username":"admin","enabled":false,"role":"admin"}`,
		"missing enabled": `{"id":37,"username":"admin","role":"admin"}`,
		"regular user":    `{"id":37,"username":"admin","enabled":true,"role":"user"}`,
		"empty":           `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			sec := adminSecrets()
			fake := &fakeRomM{id: 37, user: "admin", password: sec["ADMIN_PASSWORD"], identity: identity}
			box := newHarness(t, fake, sec)
			requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
			requireManual(t, (romm.App{}).Check(t.Context(), box.h))
			if fake.posts != 0 {
				t.Fatal("an unacceptable identity triggered account creation")
			}
		})
	}
}

func TestMissingCredentialsAndRejectedExistingLoginRemainManual(t *testing.T) {
	for _, sec := range []secrets{
		{"ADMIN_USER": "admin"},
		{"ADMIN_PASSWORD": "synthetic-admin-password"},
		adminSecrets(),
	} {
		fake := &fakeRomM{id: 37, user: "admin", password: "a-different-existing-password"}
		box := newHarness(t, fake, sec)
		requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
		requireManual(t, (romm.App{}).Check(t.Context(), box.h))
		if fake.posts != 0 || fake.password != "a-different-existing-password" {
			t.Fatal("recovery replaced an existing account or credential")
		}
	}
}

func TestMissingCSRFAndCreationRacePreserveTheAccountBoundary(t *testing.T) {
	for _, fake := range []*fakeRomM{{missingCSRF: true}, {race: true}} {
		box := newHarness(t, fake, adminSecrets())
		requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
		if fake.creates != 0 || fake.missingCSRF && fake.posts != 0 {
			t.Fatal("missing CSRF or an administrator race crossed the account boundary")
		}
	}
}

func TestUncertainCreationReusesAcceptedCredentialsWithoutReissuing(t *testing.T) {
	fake := &fakeRomM{createResponse: `{"private-upstream-diagnostic":true}`}
	box := newHarness(t, fake, adminSecrets())
	requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
	if err := (romm.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.posts != 1 || fake.creates != 1 {
		t.Fatal("Setup repeated account creation after an uncertain response")
	}
}

func TestRejectedCreationDoesNotExposeTheResponseOrResetCredentials(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusInternalServerError} {
		fake := &fakeRomM{createStatus: status}
		box := newHarness(t, fake, adminSecrets())
		err := (romm.App{}).Setup(t.Context(), box.h)
		requireManual(t, err)
		if fake.posts != 1 || fake.creates != 0 || strings.Contains(err.Error()+box.output.String(), "private-upstream-diagnostic") {
			t.Fatal("rejected creation changed an account or exposed the upstream response")
		}
	}
}

func TestPostCreationIdentityMustMatchTheReturnedAccount(t *testing.T) {
	for _, fake := range []*fakeRomM{{postCreateID: 88}, {postCreatePass: "externally-changed-password"}} {
		box := newHarness(t, fake, adminSecrets())
		requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
		if fake.posts != 1 {
			t.Fatal("Setup attempted to replace the changed identity")
		}
	}
}

func TestUnsupportedNewCredentialsDoNotTriggerAccountCreation(t *testing.T) {
	for _, input := range []struct{ user, password string }{
		{"a", "synthetic-password"}, {"admin@example.test", "synthetic-password"},
		{"admin", "short"}, {"admin", "      "}, {"admin", "non-ascii-\u00e9"},
	} {
		fake := &fakeRomM{}
		box := newHarness(t, fake, secrets{"ADMIN_USER": input.user, "ADMIN_PASSWORD": input.password})
		requireManual(t, (romm.App{}).Setup(t.Context(), box.h))
		if fake.posts != 0 {
			t.Fatal("invalid new credentials triggered account creation")
		}
	}
}

func TestUsernameNormalizationKeepsTheSharedLoginUnchanged(t *testing.T) {
	sec := adminSecrets()
	sec["ADMIN_USER"] = "Owner"
	fake := &fakeRomM{}
	box := newHarness(t, fake, sec)
	if err := (romm.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.user != "owner" || sec["ROMM_ADMIN_USER"] != "owner" || sec["ADMIN_USER"] != "Owner" {
		t.Fatal("RomM username normalization changed the shared Admin login")
	}
}

func TestCheckNeverBootstrapsAndCancellationIsPreserved(t *testing.T) {
	fake := &fakeRomM{}
	box := newHarness(t, fake, adminSecrets())
	requireManual(t, (romm.App{}).Check(t.Context(), box.h))
	if fake.posts != 0 {
		t.Fatal("Check attempted account creation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (romm.App{}).Check(ctx, box.h); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check did not preserve cancellation: %v", err)
	}
}
