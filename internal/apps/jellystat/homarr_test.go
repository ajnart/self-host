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
	"github.com/ajnart/self-host/internal/status"
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
	mu                sync.Mutex
	base              *jellystatFixture
	keys              []keyRecord
	posts             int
	status            int
	response          []byte
	changeIssued      func([]keyRecord) []keyRecord
	rejectIssued      bool
	timeoutIssued     bool
	configReadFailure bool
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
		if r.Method == http.MethodGet && f.configReadFailure {
			json.NewEncoder(w).Encode([]keyRecord{})
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
		f.mu.Lock()
		timedOut := f.timeoutIssued && r.Method == http.MethodPost && r.URL.Path == "/api/keys"
		f.mu.Unlock()
		response, err := transport.RoundTrip(r)
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

func TestJellystatHomarrEmptyInventoryCannotAuthorizeIssuance(t *testing.T) {
	for _, readFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("configuration-read-failure-%v", readFailure), func(t *testing.T) {
			h, fixture, store := newJellystatKeyHarness(t)
			fixture.configReadFailure = readFailure
			if !readFailure {
				fixture.keys = []keyRecord{}
			}
			before := store.snapshot()
			for range 2 {
				if err := (App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("ambiguous empty inventory did not remain Manual: %v", err)
				}
			}
			if fixture.postCount() != 0 || len(store.writes) != 0 || !maps.Equal(before, store.snapshot()) {
				t.Fatal("empty inventory or upstream read-failure fallback authorized Begin or POST")
			}
		})
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
				var results []helpers.StepResult
				h.Record = func(result helpers.StepResult) { results = append(results, result) }
				err := (App{}).Setup(t.Context(), h)
				if !errors.Is(err, helpers.ErrStepFailed) || errors.Is(err, helpers.ErrManual) {
					t.Fatalf("storage failure was not a failed step: %v", err)
				}
				last := results[len(results)-1]
				wantError := "credential issuance evidence could not be saved; repair private storage and retry Setup without issuing another credential"
				switch failAt {
				case 4:
					wantError = "credential ownership could not be saved; repair private storage and retry Setup without issuing another credential"
				case 5:
					wantError = "the captured credential could not be saved; repair private storage and retry Setup without issuing another credential"
				case 6:
					wantError = "credential issuance completion could not be saved; repair private storage and retry Setup without issuing another credential"
				}
				if last.ID != "homarr-key" || last.Status != status.StepFail || last.Error != wantError {
					t.Fatal("journal storage error was not recorded as a failed credential step")
				}
				for _, raw := range []string{"private-storage-error", "synthetic-key", "private-other-one", "private-other-two"} {
					if strings.Contains(err.Error(), raw) || strings.Contains(last.Error, raw) {
						t.Fatal("storage failure exposed private state or its raw cause")
					}
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

func TestJellystatHomarrMalformedTrailingIssuanceCapturesOnlySelectedKey(t *testing.T) {
	prefix := `[{"name":"unrelated-one","key":"private-other-one"},{"key":"private-other-two","name":"unrelated-two"},`
	selected := `{"key":"synthetic-key","name":"self-host Homarr statistics"`
	for _, scenario := range []struct {
		name       string
		body       string
		secret     string
		incomplete bool
		duplicate  bool
		failed     bool
	}{
		{"array tail", prefix + selected + `},`, "synthetic-key", true, false, true},
		{"record tail", prefix + selected + `,"extra":`, "synthetic-key", true, false, true},
		{"trailing document", prefix + selected + `}] {`, "synthetic-key", true, false, true},
		{"duplicate selected record", prefix + selected + `},` + selected + `}]`, "", false, true, false},
		{"duplicate key field", prefix + selected + `,"key":"private-new-other"}]`, "", true, true, true},
		{"duplicate name field", prefix + selected + `,"name":"another"}]`, "", true, true, true},
		{"duplicate unrelated field after identity", prefix + selected + `,"extra":1,"extra":2}]`, "synthetic-key", false, true, false},
		{"duplicate unrelated field before identity", prefix + `{"extra":1,"extra":2,"name":"self-host Homarr statistics","key":"synthetic-key"}]`, "synthetic-key", false, true, false},
		{"duplicate unrelated field between identity fields", prefix + `{"name":"self-host Homarr statistics","extra":1,"extra":2,"key":"synthetic-key"}]`, "synthetic-key", false, true, false},
		{"duplicate unrelated field with malformed tail", prefix + selected + `,"extra":1,"extra":`, "synthetic-key", true, true, true},
		{"duplicate unrelated then duplicate key", prefix + selected + `,"extra":1,"extra":2,"key":"synthetic-key"}]`, "", true, true, true},
		{"duplicate unrelated then duplicate name", prefix + `{"extra":1,"extra":2,"name":"self-host Homarr statistics","key":"synthetic-key","name":"self-host Homarr statistics"}]`, "", true, true, true},
		{"byte bound", prefix + selected + `,"extra":"` + strings.Repeat("x", keyInventoryByteLimit) + `"}]`, "synthetic-key", true, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h, fixture, store := newJellystatKeyHarness(t)
			fixture.response = []byte(scenario.body)
			checkpoint := ""
			for attempt := range 2 {
				store.reopen()
				if err := (App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("malformed or duplicate response did not remain held: %v", err)
				}
				current := store.Get("JELLYSTAT_HOMARR_PENDING")
				if attempt == 0 {
					checkpoint = current
				} else if current != checkpoint {
					t.Fatal("durable retry changed captured uncertainty evidence")
				}
			}
			pending, exists, err := homarrJournal.Pending(h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != scenario.secret {
				t.Fatal("projection lost a recoverable selected key or retained ambiguous evidence")
			}
			var flags struct{ Incomplete, Duplicate, Error bool }
			if json.Unmarshal(pending.Issued.Data, &flags) != nil || flags.Incomplete != scenario.incomplete || flags.Error != scenario.failed || flags.Duplicate != scenario.duplicate {
				t.Fatal("nonsecret incomplete/duplicate/error evidence was not retained")
			}
			if fixture.postCount() != 1 || store.Get("JELLYSTAT_HOMARR_API_KEY") != "" || store.Get("JELLYSTAT_HOMARR_OWNERSHIP") != "" || len(store.writes) != 2 {
				t.Fatal("uncertain selected evidence was committed, rewritten or reissued on retry")
			}
			if len(store.Get("JELLYSTAT_HOMARR_PENDING")) > 2048 {
				t.Fatal("projection persisted more than minimal selected evidence")
			}
			if strings.Contains(checkpoint, `"extra"`) {
				t.Fatal("unrelated duplicate fields were persisted instead of projected away")
			}
			jellystatAssertNoOtherKeysPersisted(t, store)
			for _, raw := range []string{"private-other-one", "private-other-two", "synthetic-key"} {
				if strings.Contains(h.Log.Redact(raw), raw) {
					t.Fatal("malformed array key was not registered in memory for redaction")
				}
			}
		})
	}
}

func TestJellystatHomarrKeyProjectionBoundsRecordsAndFields(t *testing.T) {
	h, _, _ := newJellystatKeyHarness(t)
	var fields strings.Builder
	fields.WriteString(`[{"key":"synthetic-key","name":"self-host Homarr statistics"`)
	for i := 0; i < keyInventoryFieldLimit; i++ {
		fmt.Fprintf(&fields, `,"field%d":0`, i)
	}
	fields.WriteString(`}]`)
	projection := projectKeyInventory(h, []byte(fields.String()))
	if !projection.Incomplete || !projection.Error || projection.Duplicate || len(projection.Records) != 1 || projection.Records[0].Key != "synthetic-key" {
		t.Fatal("bounded field projection lost the minimal recoverable key")
	}
	projection = projectKeyInventory(h, []byte(`[{"key":"synthetic-key","name":"self-host Homarr statistics",`+strings.Repeat(`"extra":0,`, keyInventoryFieldLimit)+`"last":0}]`))
	if !projection.Incomplete || !projection.Error || !projection.Duplicate || len(projection.Records) != 1 || projection.Records[0].Key != "synthetic-key" {
		t.Fatal("duplicate unrelated fields bypassed the total field bound or lost the selected key")
	}
	var records strings.Builder
	records.WriteByte('[')
	for i := 0; i <= keyInventoryRecordLimit; i++ {
		if i > 0 {
			records.WriteByte(',')
		}
		fmt.Fprintf(&records, `{"name":"key%d","key":"private-%d"}`, i, i)
	}
	records.WriteByte(']')
	projection = projectKeyInventory(h, []byte(records.String()))
	if !projection.Incomplete || !projection.Error || projection.Duplicate || len(projection.Records) != keyInventoryRecordLimit {
		t.Fatal("inventory projection did not enforce its record bound")
	}
	lastKey := fmt.Sprintf("private-%d", keyInventoryRecordLimit)
	if strings.Contains(h.Log.Redact(lastKey), lastKey) {
		t.Fatal("a key beyond the projection bound was not registered for memory-only redaction")
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
