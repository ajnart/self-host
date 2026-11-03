package komga_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/komga"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
	"go.yaml.in/yaml/v3"
)

type komgaSecrets struct {
	values map[string]string
	fail   bool
}

func (s *komgaSecrets) Get(key string) string { return s.values[key] }

func (s *komgaSecrets) Set(key, value string) error {
	if s.fail {
		return errors.New("private storage unavailable")
	}
	s.values[key] = value
	return nil
}

type komgaFake struct {
	mu            sync.Mutex
	claimed       bool
	claimReads    int
	claimBodies   []string
	claimStatus   int
	claimResponse string
	claims        int
	meResponse    string
	meStatus      int
	userID        string
	password      string
}

func (f *komgaFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/claim":
		index := f.claimReads
		f.claimReads++
		if f.claimStatus != 0 {
			w.WriteHeader(f.claimStatus)
		}
		if len(f.claimBodies) > 0 {
			if index >= len(f.claimBodies) {
				index = len(f.claimBodies) - 1
			}
			w.Write([]byte(f.claimBodies[index]))
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"isClaimed": f.claimed})
	case "POST /api/v1/claim":
		f.claims++
		if f.claimed || r.Header.Get("X-Komga-Email") != "admin@selfhost.local" || r.Header.Get("X-Komga-Password") != f.password || r.ContentLength > 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.claimed = true
		if f.claimResponse != "" {
			w.Write([]byte(f.claimResponse))
			return
		}
		f.writeIdentity(w)
	case "GET /api/v2/users/me":
		user, password, ok := r.BasicAuth()
		if !f.claimed || !ok || user != "admin@selfhost.local" || password != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.meStatus != 0 {
			w.WriteHeader(f.meStatus)
		}
		if f.meResponse != "" {
			w.Write([]byte(f.meResponse))
			return
		}
		f.writeIdentity(w)
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

func (f *komgaFake) writeIdentity(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(map[string]any{
		"id": f.userID, "email": "admin@selfhost.local", "roles": []string{"USER", "ADMIN", "FILE_DOWNLOAD", "PAGE_STREAMING"},
	})
}

func (f *komgaFake) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

type komgaHarness struct {
	h       *helpers.H
	secrets *komgaSecrets
	records []helpers.StepResult
	logs    bytes.Buffer
}

func newKomgaHarness(t *testing.T, handler http.Handler) *komgaHarness {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	box := &komgaHarness{secrets: &komgaSecrets{values: map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "shared-private-password", "UNRELATED_SECRET": "keep-me",
	}}}
	logger := events.New(events.JSON(&box.logs))
	for _, secret := range box.secrets.values {
		logger.Secret(secret)
	}
	box.h = helpers.New(helpers.Config{
		App: "komga", Service: "komga", BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(),
		Secrets: box.secrets, Log: logger, Now: deps.Now, Sleep: deps.Sleep,
		Record: func(result helpers.StepResult) { box.records = append(box.records, result) },
	})
	return box
}

func requireKomgaManual(t *testing.T, box *komgaHarness, err error) {
	t.Helper()
	if !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("expected Manual recovery, got %v", err)
	}
	if len(box.records) == 0 {
		t.Fatal("Setup did not record its Manual step")
	}
	last := box.records[len(box.records)-1]
	if last.Status != status.StepManual || !strings.Contains(last.Hint, "self-host setup komga") || !strings.Contains(last.Hint, "preserve") {
		t.Fatalf("missing actionable preservation recovery: %+v", last)
	}
}

func TestAppContract(t *testing.T) {
	var a app.App = komga.App{}
	info := a.Info()
	if info.ID != "komga" || info.Service != "komga" || info.Image != "gotson/komga:1.x" || info.Port != 25600 || info.ContainerPortFor(9999) != 25600 || !info.UsesMedia {
		t.Fatalf("unexpected App metadata: %+v", info)
	}
	if len(a.Inputs()) != 5 || len(a.Links()) != 0 || (komga.App{}).AdminEmail("") != "" || (komga.App{}).AdminEmail("reader") != "reader@selfhost.local" {
		t.Fatal("unexpected inputs, Links or Admin email alias")
	}
	for _, input := range a.Inputs() {
		if !input.Private || !input.RecoveryOnly || input.Required {
			t.Fatal("credential selection and recovery inputs must stay optional and private")
		}
	}
	var compose struct {
		Services map[string]struct {
			Image       string
			Restart     string
			User        string
			Environment map[string]string
			Volumes     []string
			Ports       []string
		}
	}
	if err := yaml.Unmarshal([]byte(a.Compose(app.RenderContext{Port: 45678})), &compose); err != nil {
		t.Fatal(err)
	}
	service := compose.Services["komga"]
	if len(compose.Services) != 1 || service.Image != info.Image || service.Restart != "unless-stopped" || service.User != "${PUID}:${PGID}" {
		t.Fatalf("unexpected official-image deployment: %+v", service)
	}
	if !reflect.DeepEqual(service.Environment, map[string]string{"TZ": "${TZ}"}) || !reflect.DeepEqual(service.Volumes, []string{"./config:/config", "${MEDIA_ROOT}/books:/data:ro"}) || !reflect.DeepEqual(service.Ports, []string{"${BIND_IP}:45678:25600"}) {
		t.Fatalf("unexpected persistence, credential scope or Access: %+v", service)
	}
	guide := (komga.App{}).StandaloneGuide()
	if !strings.Contains(guide, "local filesystem") || !strings.Contains(guide, "read-only") {
		t.Fatal("standalone guidance omitted storage limitations")
	}
}

func TestSetupClaimsOnlyAnAffirmativelyEmptyServerAndReusesIdentity(t *testing.T) {
	fake := &komgaFake{userID: "intended-admin", password: "shared-private-password"}
	box := newKomgaHarness(t, fake)
	a := komga.App{}
	if err := a.Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if box.secrets.Get("KOMGA_ADMIN_USER_ID") != fake.userID || fake.claimCount() != 1 {
		t.Fatal("first administrator was not claimed and recorded exactly once")
	}
	if err := a.Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	box.records = nil
	if err := a.Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	for _, record := range box.records {
		if record.Status != status.StepSkip {
			t.Fatalf("rerun did not skip accepted Setup: %+v", record)
		}
	}
	if fake.claimCount() != 1 || box.secrets.Get("UNRELATED_SECRET") != "keep-me" {
		t.Fatal("rerun mutated existing accounts or unrelated credentials")
	}
	if strings.Contains(box.logs.String(), "shared-private-password") {
		t.Fatal("Setup logged the shared Admin password")
	}
}

func TestExistingIntendedAdministratorWorksWithoutMutation(t *testing.T) {
	fake := &komgaFake{claimed: true, userID: "existing-admin", password: "shared-private-password"}
	box := newKomgaHarness(t, fake)
	before := make(map[string]string)
	for key, value := range box.secrets.values {
		before[key] = value
	}
	if err := (komga.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, box.secrets.values) {
		t.Fatal("Check changed private state")
	}
	if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.claimCount() != 0 || box.secrets.Get("KOMGA_ADMIN_USER_ID") != "existing-admin" {
		t.Fatal("existing intended login was not preserved")
	}
}

func TestAmbiguousClaimNeverCreatesAnAccount(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[]`, `{"isClaimed":null}`, `{"isClaimed":"false"}`, `{"isClaimed":0}`, `not-json`, `{"isClaimed":true}`} {
		t.Run(body, func(t *testing.T) {
			fake := &komgaFake{userID: "admin-id", password: "shared-private-password", claimBodies: []string{body}}
			box := newKomgaHarness(t, fake)
			requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
			if fake.claimCount() != 0 || box.secrets.Get("KOMGA_ADMIN_USER_ID") != "" {
				t.Fatal("ambiguous claim state authorized account creation")
			}
		})
	}
}

func TestClaimIsRecheckedImmediatelyBeforeCreation(t *testing.T) {
	fake := &komgaFake{
		userID: "admin-id", password: "shared-private-password",
		claimBodies: []string{`{"isClaimed":false}`, `{"isClaimed":false}`, `{"isClaimed":true}`},
	}
	box := newKomgaHarness(t, fake)
	requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
	if fake.claimCount() != 0 {
		t.Fatal("changed claim state was ignored")
	}
}

func TestUnsupportedClaimAPIIsManualNotAnEmptyServer(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			fake := &komgaFake{userID: "admin-id", password: "shared-private-password", claimStatus: code, claimBodies: []string{`{"isClaimed":false}`}}
			box := newKomgaHarness(t, fake)
			requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
			if fake.claimCount() != 0 {
				t.Fatal("an incompatible claim API authorized registration")
			}
		})
	}
}

func TestRejectedOrChangedAdministratorNeverRegistersAgain(t *testing.T) {
	for _, test := range []struct {
		name     string
		claimed  bool
		password string
		savedID  string
	}{
		{name: "wrong-password", claimed: true, password: "different-password"},
		{name: "changed-identity", claimed: true, password: "shared-private-password", savedID: "original-admin"},
		{name: "missing-recorded-server", password: "shared-private-password", savedID: "original-admin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &komgaFake{claimed: test.claimed, userID: "different-admin", password: test.password}
			box := newKomgaHarness(t, fake)
			box.secrets.values["KOMGA_ADMIN_USER_ID"] = test.savedID
			requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
			if fake.claimCount() != 0 || box.secrets.Get("KOMGA_ADMIN_USER_ID") != test.savedID {
				t.Fatal("existing account or recorded identity was replaced")
			}
			if !errors.Is((komga.App{}).Check(t.Context(), box.h), helpers.ErrManual) {
				t.Fatal("Check accepted a rejected or changed administrator")
			}
		})
	}
}

func TestIdentityRequiresAuthenticatedIntendedEmailIDAndAdminRole(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{}`, `not-json`,
		`{"id":"","email":"admin@selfhost.local","roles":["ADMIN"]}`,
		`{"id":" ","email":"admin@selfhost.local","roles":["ADMIN"]}`,
		`{"id":"admin-id","email":"another@selfhost.local","roles":["ADMIN"]}`,
		`{"id":"admin-id","email":"admin@selfhost.local","roles":["USER"]}`,
		`{"id":"admin-id","email":"admin@selfhost.local","roles":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			fake := &komgaFake{claimed: true, userID: "admin-id", password: "shared-private-password", meResponse: body}
			box := newKomgaHarness(t, fake)
			requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
			if fake.claimCount() != 0 || box.secrets.Get("KOMGA_ADMIN_USER_ID") != "" {
				t.Fatal("incomplete identity was accepted or caused registration")
			}
			if !errors.Is((komga.App{}).Check(t.Context(), box.h), helpers.ErrManual) {
				t.Fatal("Check accepted an incomplete identity")
			}
		})
	}
}

func TestRejectedHTTPResponsesNeverReplaceAccountsOrExposeBodies(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			fake := &komgaFake{
				claimed: true, userID: "admin-id", password: "shared-private-password",
				meStatus: code, meResponse: `{"error":"shared-private-password"}`,
			}
			box := newKomgaHarness(t, fake)
			requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
			if fake.claimCount() != 0 || strings.Contains(box.logs.String(), "shared-private-password") {
				t.Fatal("rejected authentication replaced an account or exposed its response")
			}
			if !errors.Is((komga.App{}).Check(t.Context(), box.h), helpers.ErrManual) {
				t.Fatal("Check accepted a rejected HTTP response")
			}
		})
	}
}

func TestMissingOrInvalidAdminCredentialsNeverClaim(t *testing.T) {
	for _, test := range []struct {
		name     string
		username string
		password string
	}{
		{name: "missing-user", password: "shared-private-password"},
		{name: "invalid-email", username: "invalid@name", password: "shared-private-password"},
		{name: "missing-password", username: "admin"},
		{name: "blank-password", username: "admin", password: " "},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &komgaFake{userID: "admin-id", password: "shared-private-password"}
			box := newKomgaHarness(t, fake)
			box.secrets.values["ADMIN_USER"] = test.username
			box.secrets.values["ADMIN_PASSWORD"] = test.password
			if !errors.Is((komga.App{}).Setup(t.Context(), box.h), helpers.ErrManual) || fake.claimCount() != 0 {
				t.Fatal("invalid intended credentials authorized registration")
			}
		})
	}
}

func TestUncertainClaimRecoversOnlyThroughTheIntendedLogin(t *testing.T) {
	fake := &komgaFake{userID: "admin-id", password: "shared-private-password", claimResponse: `{"error":"shared-private-password"}`}
	box := newKomgaHarness(t, fake)
	requireKomgaManual(t, box, (komga.App{}).Setup(t.Context(), box.h))
	if strings.Contains(box.logs.String(), "shared-private-password") {
		t.Fatal("claim failure exposed a secret-bearing response")
	}
	if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.claimCount() != 1 || box.secrets.Get("KOMGA_ADMIN_USER_ID") != "admin-id" {
		t.Fatal("uncertain claim was replayed instead of verifying the intended login")
	}
}

func TestClaimPersistenceFailureDoesNotReplayRegistration(t *testing.T) {
	fake := &komgaFake{userID: "admin-id", password: "shared-private-password"}
	box := newKomgaHarness(t, fake)
	box.secrets.fail = true
	if err := (komga.App{}).Setup(t.Context(), box.h); err == nil {
		t.Fatal("private persistence failure was ignored")
	}
	box.secrets.fail = false
	if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.claimCount() != 1 {
		t.Fatal("registration was replayed after private persistence failure")
	}
}

func TestReadOnlySliceNeverCallsLibraryOrCredentialIssuanceEndpoints(t *testing.T) {
	fake := &komgaFake{claimed: true, userID: "admin-id", password: "shared-private-password"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/claim" && r.URL.Path != "/api/v2/users/me" {
			t.Errorf("App-only slice called an unsupported action: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fake.ServeHTTP(w, r)
	})
	box := newKomgaHarness(t, handler)
	if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if err := (komga.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
}
