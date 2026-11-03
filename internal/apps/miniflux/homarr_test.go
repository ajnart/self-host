package miniflux_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/systemtest"
)

type minifluxTokenWrite struct{ key, value string }

type minifluxTokenStore struct {
	mu      sync.Mutex
	values  map[string]string
	disk    map[string]string
	writes  []minifluxTokenWrite
	failAt  int
	failure string
}

func (s *minifluxTokenStore) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *minifluxTokenStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, minifluxTokenWrite{key, value})
	fail := len(s.writes) == s.failAt
	if !fail || s.failure != "before-memory" {
		s.values[key] = value
	}
	if !fail || s.failure == "after-disk" {
		s.disk = maps.Clone(s.values)
	}
	if fail {
		return errors.New("store-canary: " + value)
	}
	return nil
}

type minifluxTestKey struct {
	ID          int64  `json:"id"`
	ReaderID    int64  `json:"user_id"`
	Description string `json:"description"`
	Token       string `json:"token"`
	CreatedAt   string `json:"created_at"`
}

type minifluxTokenFixture struct {
	mu              sync.Mutex
	h               *helpers.H
	store           *minifluxTokenStore
	keys            []minifluxTestKey
	posts           int
	requests        []string
	steps           []helpers.StepResult
	events          []events.Event
	issued          minifluxTestKey
	response        string
	responseStatus  int
	lostResponse    bool
	inventoryRaw    string
	inventoryStatus int
	tokenIdentity   string
	tokenRejected   bool
}

func newMinifluxTokenFixture(t *testing.T) *minifluxTokenFixture {
	t.Helper()
	values := map[string]string{
		"ADMIN_USER": "admin", "ADMIN_PASSWORD": "private-admin-password", "MINIFLUX_READER_ID": "27", "MINIFLUX_DB_PASSWORD": "private-database-password",
	}
	f := &minifluxTokenFixture{
		store:           &minifluxTokenStore{values: values, disk: maps.Clone(values)},
		keys:            []minifluxTestKey{{ID: 7, ReaderID: 27, Description: "Personal key", Token: "unrelated-private-key", CreatedAt: "2026-11-01T12:00:00Z"}},
		issued:          minifluxTestKey{ID: 8, ReaderID: 27, Description: "self-host Homarr statistics", Token: "issued-private-key", CreatedAt: "2026-11-02T12:00:00Z"},
		responseStatus:  http.StatusCreated,
		inventoryStatus: http.StatusOK,
		tokenIdentity:   `{"id":27,"username":"admin","is_admin":true}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path == "/healthcheck" && r.Method == http.MethodGet {
			w.Write([]byte("OK"))
			return
		}
		user, password, basic := r.BasicAuth()
		token := r.Header.Get("X-Auth-Token")
		if token != "" {
			if basic || token != f.issued.Token || f.tokenRejected {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		} else if !basic || user != "admin" || password != "private-admin-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected request parameters: %s", r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/me":
			if token != "" {
				w.Write([]byte(f.tokenIdentity))
			} else {
				w.Write([]byte(`{"id":27,"username":"admin","is_admin":true}`))
			}
		case "GET /v1/feeds":
			w.Write([]byte(`[{"id":41,"user_id":27}]`))
		case "GET /v1/api-keys":
			w.WriteHeader(f.inventoryStatus)
			if f.inventoryRaw != "" {
				w.Write([]byte(f.inventoryRaw))
			} else {
				json.NewEncoder(w).Encode(f.keys)
			}
		case "POST /v1/api-keys":
			f.posts++
			f.store.mu.Lock()
			pending := f.store.disk["MINIFLUX_HOMARR_TOKEN_PENDING"]
			f.store.mu.Unlock()
			if pending == "" || token != "" {
				t.Error("issuance occurred without an acknowledged intent and administrator authentication")
			}
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]string{"description": "self-host Homarr statistics"}) {
				t.Errorf("unsupported issuance body: %+v", body)
			}
			f.keys = append(f.keys, f.issued)
			if f.lostResponse {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
			w.WriteHeader(f.responseStatus)
			if f.response != "" {
				w.Write([]byte(f.response))
			} else {
				json.NewEncoder(w).Encode(f.issued)
			}
		default:
			t.Errorf("unexpected account/feed/history action: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	peer := helpers.New(helpers.Config{App: "homarr", Log: events.Discard()})
	f.h = helpers.New(helpers.Config{
		App: "miniflux", Service: "miniflux", ContainerPort: 8080,
		BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(), Secrets: f.store,
		Log: events.New(events.SinkFunc(func(event events.Event) { f.events = append(f.events, event) })),
		Now: deps.Now, Sleep: deps.Sleep,
		Record: func(result helpers.StepResult) { f.steps = append(f.steps, result) },
		Peer: func(id string) *helpers.H {
			if id == "homarr" {
				return peer
			}
			return nil
		},
	})
	return f
}

func (f *minifluxTokenFixture) assertPrivate(t *testing.T, err error) {
	t.Helper()
	message := ""
	if err != nil {
		message = err.Error()
	}
	raw, encodeErr := json.Marshal([]any{message, f.steps, f.events})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	for _, secret := range []string{"private-admin-password", "private-database-password", "issued-private-key", "unrelated-private-key", "store-canary", "response-canary"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("credential material entered diagnostics: %s", secret)
		}
	}
}

func TestMinifluxHomarrIssuesOneCredentialAndReusesOwnedIdentity(t *testing.T) {
	f := newMinifluxTokenFixture(t)
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 || f.store.Get("MINIFLUX_HOMARR_API_KEY") != f.issued.Token || f.store.Get("MINIFLUX_HOMARR_TOKEN_PENDING") != "" {
		t.Fatal("issuance did not publish exactly the verified credential")
	}
	before := len(f.store.writes)
	for range 2 {
		if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil {
			t.Fatal(err)
		}
		if err := (miniflux.App{}).Check(t.Context(), f.h); err != nil {
			t.Fatal(err)
		}
	}
	if f.posts != 1 || len(f.store.writes) != before || f.store.Get("MINIFLUX_READER_ID") != "27" {
		t.Fatal("valid ownership was rewritten or caused another issuance")
	}
	keys := []string{}
	for _, write := range f.store.writes {
		keys = append(keys, write.key)
		if strings.Contains(write.value, "unrelated-private-key") {
			t.Fatal("an unrelated inventory credential was persisted")
		}
	}
	want := []string{"MINIFLUX_HOMARR_TOKEN_PENDING", "MINIFLUX_HOMARR_TOKEN_PENDING", "MINIFLUX_HOMARR_TOKEN_PENDING", "MINIFLUX_HOMARR_TOKEN_OWNERSHIP", "MINIFLUX_HOMARR_API_KEY", "MINIFLUX_HOMARR_TOKEN_PENDING"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("journal write ordering changed: %v", keys)
	}
	f.assertPrivate(t, nil)
}

func TestMinifluxHomarrPreinventoryAndExistingRecordsNeverAuthorizeReplacement(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*minifluxTokenFixture)
	}{
		{name: "managed description collision", change: func(f *minifluxTokenFixture) { f.keys[0].Description = "SELF-HOST HOMARR STATISTICS" }},
		{name: "null inventory", change: func(f *minifluxTokenFixture) { f.inventoryRaw = "null" }},
		{name: "malformed inventory", change: func(f *minifluxTokenFixture) { f.inventoryRaw = `[{"id":"bad","token":"response-canary"}]` }},
		{name: "wrong inventory owner", change: func(f *minifluxTokenFixture) { f.keys[0].ReaderID = 28 }},
		{name: "duplicate key ID", change: func(f *minifluxTokenFixture) { f.keys = append(f.keys, f.keys[0]) }},
		{name: "unsupported token API", change: func(f *minifluxTokenFixture) { f.inventoryStatus = 404 }},
		{name: "secret without ownership", change: func(f *minifluxTokenFixture) { f.store.values["MINIFLUX_HOMARR_API_KEY"] = "existing-token" }},
		{name: "ownership without secret", change: func(f *minifluxTokenFixture) { f.store.values["MINIFLUX_HOMARR_TOKEN_OWNERSHIP"] = `{}` }},
		{name: "malformed pending", change: func(f *minifluxTokenFixture) { f.store.values["MINIFLUX_HOMARR_TOKEN_PENDING"] = "unknown" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newMinifluxTokenFixture(t)
			scenario.change(f)
			before := maps.Clone(f.store.values)
			for range 2 {
				err := (miniflux.App{}).Setup(t.Context(), f.h)
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("expected Manual without issuance: %v", err)
				}
				f.assertPrivate(t, err)
			}
			if f.posts != 0 || len(f.store.writes) != 0 || !reflect.DeepEqual(f.store.values, before) {
				t.Fatal("preinventory or existing records were replaced")
			}
		})
	}
}

func TestMinifluxHomarrCapturesSecretBeforeRejectingIssuanceSemantics(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*minifluxTokenFixture)
	}{
		{name: "unsuccessful status", change: func(f *minifluxTokenFixture) { f.responseStatus = 500 }},
		{name: "wrong returned reader", change: func(f *minifluxTokenFixture) { f.issued.ReaderID = 28 }},
		{name: "wrong returned description", change: func(f *minifluxTokenFixture) { f.issued.Description = "another key" }},
		{name: "bad ID with recoverable secret", change: func(f *minifluxTokenFixture) {
			f.response = `{"token":"issued-private-key","id":"bad","user_id":27,"description":"self-host Homarr statistics","created_at":"2026-11-02T12:00:00Z"}`
		}},
		{name: "truncated body after secret", change: func(f *minifluxTokenFixture) { f.response = `{"token":"issued-private-key","id":` }},
		{name: "duplicate response token", change: func(f *minifluxTokenFixture) {
			f.response = `{"token":"issued-private-key","token":"response-canary","id":8,"user_id":27,"description":"self-host Homarr statistics","created_at":"2026-11-02T12:00:00Z"}`
		}},
		{name: "body error", change: func(f *minifluxTokenFixture) {
			f.response = `{"token":"issued-private-key","id":8,"user_id":27,"description":"self-host Homarr statistics","created_at":"2026-11-02T12:00:00Z","error_message":"response-canary"}`
		}},
		{name: "missing creation time", change: func(f *minifluxTokenFixture) { f.issued.CreatedAt = "" }},
		{name: "preexisting resource ID", change: func(f *minifluxTokenFixture) { f.issued.ID = 7 }},
		{name: "token authenticates another ID", change: func(f *minifluxTokenFixture) { f.tokenIdentity = `{"id":28,"username":"admin","is_admin":true}` }},
		{name: "token authenticates another username", change: func(f *minifluxTokenFixture) { f.tokenIdentity = `{"id":27,"username":"another","is_admin":true}` }},
		{name: "token lacks administrator authorization", change: func(f *minifluxTokenFixture) { f.tokenIdentity = `{"id":27,"username":"admin","is_admin":false}` }},
		{name: "token lacks explicit authorization", change: func(f *minifluxTokenFixture) { f.tokenIdentity = `{"id":27,"username":"admin"}` }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newMinifluxTokenFixture(t)
			scenario.change(f)
			for range 2 {
				err := (miniflux.App{}).Setup(t.Context(), f.h)
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("invalid issuance was not held: %v", err)
				}
				f.assertPrivate(t, err)
			}
			journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending"}
			pending, exists, err := journal.Pending(f.h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != "issued-private-key" || f.posts != 1 || f.store.Get("MINIFLUX_HOMARR_API_KEY") != "" || f.store.Get("MINIFLUX_HOMARR_TOKEN_OWNERSHIP") != "" {
				t.Fatalf("issued secret was lost, accepted prematurely or issued again: exists=%v err=%v posts=%d", exists, err, f.posts)
			}
		})
	}
}

func TestMinifluxHomarrLostResponseOrMissingSecretNeverReissues(t *testing.T) {
	for _, mode := range []string{"lost response", "missing secret"} {
		t.Run(mode, func(t *testing.T) {
			f := newMinifluxTokenFixture(t)
			f.lostResponse = mode == "lost response"
			if mode == "missing secret" {
				f.response = `{"id":8,"user_id":27,"description":"self-host Homarr statistics","created_at":"2026-11-02T12:00:00Z"}`
			}
			for range 2 {
				if err := (miniflux.App{}).Setup(t.Context(), f.h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("uncertain issuance should be Manual: %v", err)
				}
			}
			if f.posts != 1 || f.store.Get("MINIFLUX_HOMARR_TOKEN_PENDING") == "" || f.store.Get("MINIFLUX_HOMARR_API_KEY") != "" {
				t.Fatal("uncertainty caused reissuance or discarded the pending record")
			}
		})
	}
}

func TestMinifluxHomarrEachPersistenceFailureStopsAndNeverDuplicatesIssuance(t *testing.T) {
	for _, mode := range []string{"before-memory", "after-memory", "after-disk"} {
		for failAt := 1; failAt <= 6; failAt++ {
			t.Run(fmt.Sprintf("%s/write-%d", mode, failAt), func(t *testing.T) {
				f := newMinifluxTokenFixture(t)
				f.store.failAt, f.store.failure = failAt, mode
				err := (miniflux.App{}).Setup(t.Context(), f.h)
				if err == nil || len(f.store.writes) != failAt || failAt == 1 && f.posts != 0 || failAt > 1 && f.posts != 1 {
					t.Fatalf("persistence failure did not stop its invocation: err=%v writes=%d posts=%d", err, len(f.store.writes), f.posts)
				}
				f.assertPrivate(t, err)
				f.store.mu.Lock()
				f.store.values = maps.Clone(f.store.disk)
				f.store.failAt = 0
				f.store.mu.Unlock()
				before := f.posts
				err = (miniflux.App{}).Setup(t.Context(), f.h)
				if f.posts > 1 || before > 0 && f.posts != before {
					t.Fatal("recovery issued a second credential")
				}
				if failAt == 2 && mode != "after-disk" || failAt == 1 && mode == "after-disk" {
					if !errors.Is(err, helpers.ErrManual) || f.store.Get("MINIFLUX_HOMARR_TOKEN_PENDING") == "" {
						t.Fatalf("intent-only uncertainty was not preserved: %v", err)
					}
				} else if err != nil {
					t.Fatalf("captured or fully committed tuple could not resume: %v", err)
				}
				f.assertPrivate(t, err)
			})
		}
	}
}

func TestMinifluxHomarrRejectedOrMissingSavedCredentialIsNotRotated(t *testing.T) {
	for _, mode := range []string{"rejected", "missing", "fingerprint mismatch", "wrong key ID", "inventory changed creation", "pending conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newMinifluxTokenFixture(t)
			if mode == "pending conflict" {
				f.store.failAt, f.store.failure = 4, "before-memory"
			}
			err := (miniflux.App{}).Setup(t.Context(), f.h)
			if mode != "pending conflict" && err != nil {
				t.Fatal(err)
			}
			f.store.failAt = 0
			switch mode {
			case "rejected":
				f.tokenRejected = true
			case "missing":
				f.store.values["MINIFLUX_HOMARR_API_KEY"] = ""
			case "fingerprint mismatch", "pending conflict":
				f.store.values["MINIFLUX_HOMARR_API_KEY"] = "other-private-token"
			case "wrong key ID":
				raw := f.store.values["MINIFLUX_HOMARR_TOKEN_OWNERSHIP"]
				f.store.values["MINIFLUX_HOMARR_TOKEN_OWNERSHIP"] = strings.Replace(raw, `"id":8`, `"id":9`, 1)
			case "inventory changed creation":
				f.mu.Lock()
				f.keys[len(f.keys)-1].CreatedAt = "2026-11-02T13:00:00Z"
				f.mu.Unlock()
			}
			before := maps.Clone(f.store.values)
			for range 2 {
				err := (miniflux.App{}).Setup(t.Context(), f.h)
				if !errors.Is(err, helpers.ErrManual) || f.posts != 1 || !reflect.DeepEqual(before, f.store.values) {
					t.Fatalf("saved or pending identity was replaced: %v", err)
				}
				f.assertPrivate(t, err)
			}
		})
	}
}

func TestMinifluxHomarrCheckIsReadOnlyWhileCapturedIssuanceIsPending(t *testing.T) {
	f := newMinifluxTokenFixture(t)
	f.store.failAt, f.store.failure = 4, "before-memory"
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err == nil {
		t.Fatal("expected failed ownership persistence")
	}
	before := len(f.store.writes)
	if err := miniflux.VerifyHomarr(t.Context(), f.h); !errors.Is(err, helpers.ErrManual) || len(f.store.writes) != before || f.posts != 1 {
		t.Fatalf("read-only verification finalized or reissued pending credentials: %v", err)
	}
	f.store.failAt = 0
	if err := (miniflux.App{}).Setup(t.Context(), f.h); err != nil || f.posts != 1 {
		t.Fatalf("Do could not finish exactly the captured credential: %v", err)
	}
}
