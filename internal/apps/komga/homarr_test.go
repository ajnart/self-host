package komga_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/apps/komga"
	"github.com/ajnart/self-host/internal/helpers"
)

var komgaTestJournal = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending",
}

type komgaKeyStore struct {
	mu     sync.Mutex
	values map[string]string
	writes int
	failAt int
}

func (s *komgaKeyStore) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *komgaKeyStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	s.values[key] = value
	if s.writes == s.failAt {
		return errors.New("private-store-canary: " + value)
	}
	return nil
}

func (s *komgaKeyStore) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.values)
}

type komgaKeyFake struct {
	mu        sync.Mutex
	admin     *komgaFake
	user      map[string]any
	keys      []map[string]any
	posts     int
	response  string
	status    int
	transport bool
	rejected  bool
}

func komgaLimitedUser() map[string]any {
	return map[string]any{
		"id": "limited-user", "email": "metrics@selfhost.local", "roles": []string{"USER"},
		"sharedAllLibraries": true, "sharedLibrariesIds": []string{}, "labelsAllow": []string{}, "labelsExclude": []string{},
	}
}

func komgaIssuedMetadata() map[string]any {
	return map[string]any{
		"id": "managed-key", "userId": "limited-user", "comment": "self-host Homarr statistics",
		"createdDate": "2026-11-03T08:00:00Z", "lastModifiedDate": "2026-11-03T08:00:00Z",
	}
}

func newKomgaKeyFake() *komgaKeyFake {
	return &komgaKeyFake{
		admin: &komgaFake{claimed: true, userID: "intended-admin", password: "shared-private-password"},
		user:  komgaLimitedUser(), keys: []map[string]any{},
	}
}

func (f *komgaKeyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, basic := r.BasicAuth()
	if r.URL.Path == "/api/v1/claim" || r.URL.Path == "/api/v2/users/me" && basic && username == "admin@selfhost.local" {
		f.admin.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	limitedLogin := basic && username == "metrics@selfhost.local" && password == "limited-private-password"
	keyLogin := !f.rejected && r.Header.Get("X-API-Key") == "selected-one-time-key"
	if !limitedLogin && !keyLogin {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v2/users/me":
		json.NewEncoder(w).Encode(f.user)
	case "GET /api/v2/users/me/api-keys":
		json.NewEncoder(w).Encode(f.keys)
	case "POST /api/v2/users/me/api-keys":
		if !limitedLogin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var request struct {
			Comment string `json:"comment"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Comment != "self-host Homarr statistics" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.posts++
		key := komgaIssuedMetadata()
		key["key"] = "******"
		f.keys = append(f.keys, key)
		if f.transport {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		if f.response != "" {
			w.Write([]byte(f.response))
			return
		}
		created := maps.Clone(key)
		created["key"] = "selected-one-time-key"
		json.NewEncoder(w).Encode(created)
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

func (f *komgaKeyFake) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

func newKomgaKeyHarness(t *testing.T, fake *komgaKeyFake) (*komgaHarness, *komgaKeyStore) {
	t.Helper()
	box := newKomgaHarness(t, fake)
	store := &komgaKeyStore{values: maps.Clone(box.secrets.values)}
	store.values["KOMGA_ADMIN_USER_ID"] = "intended-admin"
	store.values["KOMGA_HOMARR_USER_ID"] = "limited-user"
	store.values["KOMGA_HOMARR_USER_EMAIL"] = "metrics@selfhost.local"
	store.values["KOMGA_HOMARR_USER_PASSWORD"] = "limited-private-password"
	box.h.Secrets = store
	return box, store
}

func TestKomgaKeyIssuanceReusesTheSelectedLimitedAccountAndExactKey(t *testing.T) {
	fake := newKomgaKeyFake()
	box, store := newKomgaKeyHarness(t, fake)
	if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.postCount() != 1 || store.Get("KOMGA_HOMARR_API_KEY") != "selected-one-time-key" || store.Get("KOMGA_HOMARR_KEY_PENDING") != "" || store.Get("KOMGA_HOMARR_KEY_OWNERSHIP") == "" || fake.admin.claimCount() != 0 {
		t.Fatal("issuance did not complete exactly one verified selected-user key")
	}
	store.values["KOMGA_HOMARR_USER_PASSWORD"] = ""
	before := store.snapshot()
	if err := (komga.App{}).Check(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.postCount() != 1 || !reflect.DeepEqual(before, store.snapshot()) {
		t.Fatal("valid credentials or unrelated private state changed on rerun")
	}
	for _, secret := range []string{"selected-one-time-key", "limited-private-password", "shared-private-password"} {
		if strings.Contains(box.logs.String(), secret) {
			t.Fatal("credential material escaped Setup logs")
		}
	}
}

func TestKomgaKeyPermissionsAndExplicitSelectionAreRequiredBeforeIssuance(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*komgaKeyFake, *komgaKeyStore)
	}{
		{"missing selection", func(_ *komgaKeyFake, s *komgaKeyStore) { s.values["KOMGA_HOMARR_USER_ID"] = "" }},
		{"wrong selected ID", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["id"] = "another-user" }},
		{"wrong selected email", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["email"] = "another@selfhost.local" }},
		{"administrator", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["roles"] = []string{"USER", "ADMIN"} }},
		{"download", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["roles"] = []string{"USER", "FILE_DOWNLOAD"} }},
		{"stream", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["roles"] = []string{"USER", "PAGE_STREAMING"} }},
		{"sync", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["roles"] = []string{"USER", "KOBO_SYNC"} }},
		{"unknown role", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["roles"] = []string{"USER", "READONLY"} }},
		{"missing sharing flag", func(f *komgaKeyFake, _ *komgaKeyStore) { delete(f.user, "sharedAllLibraries") }},
		{"restricted libraries", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["sharedAllLibraries"] = false }},
		{"label restriction", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["labelsExclude"] = []string{"private"} }},
		{"age restriction", func(f *komgaKeyFake, _ *komgaKeyStore) {
			f.user["ageRestriction"] = map[string]any{"age": 12, "restriction": "ALLOW_ONLY"}
		}},
		{"null restrictions", func(f *komgaKeyFake, _ *komgaKeyStore) { f.user["labelsAllow"] = nil }},
		{"wrong selected password", func(_ *komgaKeyFake, s *komgaKeyStore) { s.values["KOMGA_HOMARR_USER_PASSWORD"] = "wrong" }},
		{"changed intended administrator", func(f *komgaKeyFake, _ *komgaKeyStore) { f.admin.userID = "another-admin" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newKomgaKeyFake()
			box, store := newKomgaKeyHarness(t, fake)
			test.change(fake, store)
			if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("selection prerequisite should remain Manual: %v", err)
			}
			if fake.postCount() != 0 || store.Get("KOMGA_HOMARR_KEY_PENDING") != "" {
				t.Fatal("unverified selection authorized key issuance")
			}
		})
	}
}

func TestKomgaUnownedCommentCollisionNeverAdoptsOrIssues(t *testing.T) {
	fake := newKomgaKeyFake()
	fake.keys = append(fake.keys, komgaIssuedMetadata())
	box, store := newKomgaKeyHarness(t, fake)
	before := store.snapshot()
	if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unowned name collision should remain Manual: %v", err)
	}
	if fake.postCount() != 0 || !reflect.DeepEqual(before, store.snapshot()) {
		t.Fatal("an unowned key was adopted or replaced")
	}
}

func TestKomgaSavedCredentialRejectionAndIncompleteOwnershipNeverReissue(t *testing.T) {
	for _, scenario := range []string{"rejected", "missing secret", "missing ownership", "different secret", "changed role", "changed key metadata"} {
		t.Run(scenario, func(t *testing.T) {
			fake := newKomgaKeyFake()
			box, store := newKomgaKeyHarness(t, fake)
			if err := (komga.App{}).Setup(t.Context(), box.h); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			switch scenario {
			case "rejected":
				fake.rejected = true
			case "missing secret":
				store.values["KOMGA_HOMARR_API_KEY"] = ""
			case "missing ownership":
				store.values["KOMGA_HOMARR_KEY_OWNERSHIP"] = ""
			case "different secret":
				store.values["KOMGA_HOMARR_API_KEY"] = "another-private-key"
			case "changed role":
				fake.user["roles"] = []string{"USER", "FILE_DOWNLOAD"}
			case "changed key metadata":
				fake.keys[0]["comment"] = "person-edited"
			}
			fake.mu.Unlock()
			before := store.snapshot()
			if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("rejected ownership should remain Manual: %v", err)
			}
			if fake.postCount() != 1 || !reflect.DeepEqual(before, store.snapshot()) {
				t.Fatal("a saved credential failure authorized replacement or discarded state")
			}
		})
	}
}

func TestKomgaCapturesSelectedSecretBeforeStatusOrSemanticAcceptance(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		status   int
		response string
	}{
		{"failed status", 500, `{"key":"selected-one-time-key","id":"managed-key"}`},
		{"malformed later field", 200, `{"key":"selected-one-time-key","id":`},
		{"wrong typed ID", 200, `{"key":"selected-one-time-key","id":42,"userId":"limited-user"}`},
		{"wrong owner", 200, `{"key":"selected-one-time-key","id":"managed-key","userId":"another-user","comment":"self-host Homarr statistics","createdDate":"2026-11-03T08:00:00Z","lastModifiedDate":"2026-11-03T08:00:00Z"}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake := newKomgaKeyFake()
			fake.status, fake.response = scenario.status, scenario.response
			box, store := newKomgaKeyHarness(t, fake)
			if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unconfirmed issuance should remain Manual: %v", err)
			}
			pending, exists, err := komgaTestJournal.Pending(box.h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != "selected-one-time-key" || store.Get("KOMGA_HOMARR_API_KEY") != "" {
				t.Fatal("selected one-time secret was not retained before semantic acceptance")
			}
			if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) || fake.postCount() != 1 {
				t.Fatalf("uncertainty authorized a second issuance: %v", err)
			}
			if strings.Contains(box.logs.String(), "selected-one-time-key") {
				t.Fatal("captured key escaped diagnostics")
			}
		})
	}
}

func TestKomgaTransportUncertaintyRetainsIntentWithoutReplay(t *testing.T) {
	fake := newKomgaKeyFake()
	fake.transport = true
	box, _ := newKomgaKeyHarness(t, fake)
	if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("uncertain transport should remain Manual: %v", err)
	}
	pending, exists, err := komgaTestJournal.Pending(box.h)
	if err != nil || !exists || pending.Issued != nil {
		t.Fatal("transport failure did not retain only its original intent")
	}
	if err := (komga.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) || fake.postCount() != 1 {
		t.Fatalf("intent-only uncertainty was replayed: %v", err)
	}
}

func TestKomgaJournalWriteFailuresNeverIssueAgain(t *testing.T) {
	for failAt := 1; failAt <= 6; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			fake := newKomgaKeyFake()
			box, store := newKomgaKeyHarness(t, fake)
			store.failAt = failAt
			err := (komga.App{}).Setup(t.Context(), box.h)
			if err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "selected-one-time-key") {
				t.Fatalf("failed write was hidden or unsafe: %v", err)
			}
			posts := fake.postCount()
			if failAt == 1 && posts != 0 || failAt > 1 && posts != 1 {
				t.Fatal("the failed pre-POST barrier permitted issuance")
			}
			store.failAt = 0
			err = (komga.App{}).Setup(t.Context(), box.h)
			if failAt == 1 && !errors.Is(err, helpers.ErrManual) || failAt > 1 && err != nil || fake.postCount() != posts {
				t.Fatalf("write recovery changed credentials or repeated issuance: %v", err)
			}
		})
	}
}
