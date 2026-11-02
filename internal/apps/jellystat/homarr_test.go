package jellystat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

type jellystatKeyStore struct {
	mu      sync.Mutex
	values  map[string]string
	disk    map[string]string
	writes  []string
	failAt  int
	failure string
}

func (s *jellystatKeyStore) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *jellystatKeyStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, key)
	fail := len(s.writes) == s.failAt
	if !fail || s.failure != "before-memory" {
		s.values[key] = value
	}
	if !fail || s.failure == "after-disk" {
		s.disk = maps.Clone(s.values)
	}
	if fail {
		return errors.New("private-storage-error: " + value)
	}
	return nil
}

func (s *jellystatKeyStore) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.values)
}

func (s *jellystatKeyStore) reopen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = maps.Clone(s.disk)
	s.failAt = 0
}

type jellystatKeyFixture struct {
	mu            sync.Mutex
	base          *jellystatFixture
	keys          []keyRecord
	posts         int
	status        int
	response      []byte
	changeIssued  func([]keyRecord) []keyRecord
	rejectIssued  bool
	timeoutIssued bool
}

func newJellystatKeyHarness(t *testing.T) (*helpers.H, *jellystatKeyFixture, *jellystatKeyStore) {
	t.Helper()
	base := newJellystatFixture()
	h, _ := jellystatHarness(t, base)
	initial := maps.Clone(map[string]string(h.Secrets.(jellystatSecrets)))
	store := &jellystatKeyStore{values: initial, disk: maps.Clone(initial)}
	f := &jellystatKeyFixture{base: base, status: http.StatusOK, keys: []keyRecord{
		{Name: "unrelated-one", Key: "private-other-one"}, {Name: "unrelated-two", Key: "private-other-two"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/keys" {
			if r.URL.Path == "/api/getconfig" && r.Header.Get("X-API-Token") != "" {
				f.mu.Lock()
				rejected := f.rejectIssued
				f.mu.Unlock()
				if rejected {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}
			base.serve(t, w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-jwt" && r.Header.Get("X-API-Token") != "synthetic-key" {
			t.Error("key operation did not use the intended private header credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			f.posts++
			if store.Get("JELLYSTAT_HOMARR_PENDING") == "" {
				t.Error("issuance preceded the durable intent")
			}
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["name"] != homarrKeyName {
				t.Error("issuance invented a scope or used the wrong key name")
			}
			f.keys = append(f.keys, keyRecord{Name: homarrKeyName, Key: "synthetic-key"})
			if f.changeIssued != nil {
				f.keys = f.changeIssued(f.keys)
			}
			w.WriteHeader(f.status)
			if f.response != nil {
				w.Write(f.response)
				return
			}
		} else if r.Method != http.MethodGet {
			t.Error("key verification attempted deletion or another mutation")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		json.NewEncoder(w).Encode(f.keys)
	}))
	t.Cleanup(srv.Close)
	h.BaseURL, h.URL, h.Config.HTTP, h.Secrets = srv.URL, srv.URL, srv.Client(), store
	h.Peer = func(id string) *helpers.H {
		if id == "homarr" {
			return &helpers.H{}
		}
		return nil
	}
	client := *h.Config.HTTP
	transport := client.Transport
	client.Transport = jellystatTransport(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		f.mu.Lock()
		timedOut := f.timeoutIssued && r.Method == http.MethodPost && r.URL.Path == "/api/keys"
		f.mu.Unlock()
		if timedOut && response != nil {
			response.Body.Close()
			return nil, errors.New("private simulated timeout after issuance")
		}
		return response, err
	})
	h.Config.HTTP = &client
	return h, f, store
}

func (f *jellystatKeyFixture) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

func jellystatAssertNoOtherKeysPersisted(t *testing.T, store *jellystatKeyStore) {
	t.Helper()
	for _, value := range store.snapshot() {
		for _, other := range []string{"private-other-one", "private-other-two", "unrelated-one", "unrelated-two", "private-new-other"} {
			if strings.Contains(value, other) {
				t.Fatal("another user's key or key record was persisted")
			}
		}
	}
}

func TestJellystatHomarrIssuanceUsesOnlyExactNewSelectedKey(t *testing.T) {
	h, fixture, store := newJellystatKeyHarness(t)
	if err := (App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := (App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		if err := (App{}).Check(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.postCount() != 1 || store.Get("JELLYSTAT_HOMARR_API_KEY") != "synthetic-key" || store.Get("JELLYSTAT_HOMARR_PENDING") != "" {
		t.Fatal("issued key was replaced, duplicated or not committed")
	}
	wantWrites := []string{"JELLYSTAT_HOMARR_PENDING", "JELLYSTAT_HOMARR_PENDING", "JELLYSTAT_HOMARR_PENDING", "JELLYSTAT_HOMARR_OWNERSHIP", "JELLYSTAT_HOMARR_API_KEY", "JELLYSTAT_HOMARR_PENDING"}
	store.mu.Lock()
	gotWrites := append([]string{}, store.writes...)
	store.mu.Unlock()
	if fmt.Sprint(gotWrites) != fmt.Sprint(wantWrites) {
		t.Fatalf("issuance did not use the shared four-operation journal: %v", gotWrites)
	}
	jellystatAssertNoOtherKeysPersisted(t, store)
	for _, raw := range []string{"private-other-one", "private-other-two", "synthetic-key"} {
		if strings.Contains(h.Log.Redact(raw), raw) {
			t.Fatal("a raw key was not registered for in-memory redaction")
		}
	}
}

func TestJellystatHomarrExplicitEmptyInventoryIsNotMissingInventory(t *testing.T) {
	h, fixture, store := newJellystatKeyHarness(t)
	fixture.keys = []keyRecord{}
	if err := (App{}).Setup(t.Context(), h); err != nil || fixture.postCount() != 1 || store.Get("JELLYSTAT_HOMARR_API_KEY") != "synthetic-key" {
		t.Fatalf("an authenticated explicit empty inventory did not issue exactly one key: %v", err)
	}
}

func TestJellystatHomarrAccountStateNeverGrantsBootstrapAuthority(t *testing.T) {
	for _, state := range []int{0, 1} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			h, fixture, store := newJellystatKeyHarness(t)
			fixture.base.state = map[string]any{"state": state, "version": "1.1.12"}
			before := store.snapshot()
			if err := (App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || fixture.postCount() != 0 || !maps.Equal(before, store.snapshot()) {
				t.Fatalf("configuration state authorized account/key mutation: %v", err)
			}
		})
	}
}

func TestJellystatHomarrUncertainIssuanceNeverReissues(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    func(*jellystatKeyFixture)
		captured  bool
		wantPosts int
	}{
		{"rejected response captures selected key", func(f *jellystatKeyFixture) { f.status = http.StatusForbidden }, true, 1},
		{"successful response rejected key", func(f *jellystatKeyFixture) { f.rejectIssued = true }, true, 1},
		{"timeout after remote issuance", func(f *jellystatKeyFixture) { f.timeoutIssued = true }, false, 1},
		{"malformed response", func(f *jellystatKeyFixture) {
			f.response = []byte(`[{"name":"unrelated-one","key":"private-other-one"},`)
		}, false, 1},
		{"null response", func(f *jellystatKeyFixture) { f.response = []byte(`null`) }, false, 1},
		{"another new key is ambiguous", func(f *jellystatKeyFixture) {
			f.changeIssued = func(keys []keyRecord) []keyRecord {
				return append(keys, keyRecord{Name: "another", Key: "private-new-other"})
			}
		}, false, 1},
		{"existing key changed", func(f *jellystatKeyFixture) {
			f.changeIssued = func(keys []keyRecord) []keyRecord { keys[0].Key = "private-new-other"; return keys }
		}, false, 1},
		{"existing name collision", func(f *jellystatKeyFixture) {
			f.keys = append(f.keys, keyRecord{Name: homarrKeyName, Key: "unowned-key"})
		}, false, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h, fixture, store := newJellystatKeyHarness(t)
			scenario.change(fixture)
			for range 2 {
				if err := (App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("expected a Manual hold: %v", err)
				}
			}
			if fixture.postCount() != scenario.wantPosts || store.Get("JELLYSTAT_HOMARR_API_KEY") != "" || store.Get("JELLYSTAT_HOMARR_OWNERSHIP") != "" {
				t.Fatal("uncertain issuance was repeated or incorrectly committed")
			}
			if scenario.wantPosts == 1 {
				pending, exists, err := homarrJournal.Pending(h)
				if err != nil || !exists || pending.Issued == nil || (pending.Issued.Secret != "") != scenario.captured {
					t.Fatal("capture did not precede semantic/status rejection or retained an ambiguous key")
				}
			}
			jellystatAssertNoOtherKeysPersisted(t, store)
			if strings.Contains(h.Log.Redact("private-other-one"), "private-other-one") {
				t.Fatal("a rejected all-key response was not redacted")
			}
		})
	}
}

func TestJellystatHomarrJournalSurvivesEveryPrivateWriteBoundary(t *testing.T) {
	for _, failure := range []string{"before-memory", "after-memory", "after-disk"} {
		for failAt := 1; failAt <= 6; failAt++ {
			t.Run(fmt.Sprintf("%s/write-%d", failure, failAt), func(t *testing.T) {
				h, fixture, store := newJellystatKeyHarness(t)
				store.failure, store.failAt = failure, failAt
				err := (App{}).Setup(t.Context(), h)
				if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-storage-error") {
					t.Fatalf("storage failure was not a secret-safe Manual hold: %v", err)
				}
				if failAt == 1 && fixture.postCount() != 0 || failAt > 1 && fixture.postCount() != 1 {
					t.Fatal("remote issuance did not follow durable Begin")
				}
				store.reopen()
				if err := (App{}).Setup(context.Background(), h); err != nil && !errors.Is(err, helpers.ErrManual) {
					t.Fatal(err)
				}
				if fixture.postCount() > 1 {
					t.Fatal("restart after a failed private write issued another key")
				}
				jellystatAssertNoOtherKeysPersisted(t, store)
			})
		}
	}
}

func TestJellystatHomarrPinsIdentityAndNeverBootstrapsFromStateOne(t *testing.T) {
	for _, scenario := range []string{"account", "backend", "kind", "admin", "key", "ownership", "malformed journal", "state one"} {
		t.Run(scenario, func(t *testing.T) {
			h, fixture, store := newJellystatKeyHarness(t)
			if err := (App{}).Setup(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			fixture.base.mu.Lock()
			switch scenario {
			case "account":
				store.values["ADMIN_USER"] = "another"
				fixture.base.username = "another"
				fixture.base.config["APP_USER"] = "another"
			case "backend":
				store.values["JELLYSTAT_SERVER_ADDRESS"] = "http://other.invalid"
				fixture.base.config["JF_HOST"] = "http://other.invalid"
			case "kind":
				h.Inputs["media_server"] = "emby"
				fixture.base.config["IS_JELLYFIN"] = false
			case "admin":
				store.values["JELLYSTAT_SERVER_ADMIN_ID"] = "another"
				fixture.base.admins = []any{map[string]any{"Id": "another", "Policy": map[string]bool{"IsAdministrator": true}}}
			case "key":
				store.values["JELLYSTAT_HOMARR_API_KEY"] = "another"
			case "ownership":
				store.values["JELLYSTAT_HOMARR_OWNERSHIP"] = ""
			case "malformed journal":
				store.values["JELLYSTAT_HOMARR_PENDING"] = `{}`
			case "state one":
				fixture.base.state = map[string]any{"state": 1, "version": "1.1.12"}
			}
			fixture.base.mu.Unlock()
			store.mu.Unlock()
			if err := (App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || fixture.postCount() != 1 {
				t.Fatalf("changed identity authorized mutation or lost Manual recovery: %v", err)
			}
		})
	}
}
