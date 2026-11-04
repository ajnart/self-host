package karakeep_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/karakeep"
	"github.com/ajnart/self-host/internal/helpers"
)

func karakeepJournal() helpers.CredentialIssuance {
	return helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
}

func karakeepIssuanceHarness(t *testing.T, fake *fakeKarakeep) *harness {
	t.Helper()
	box := newHarness(t, fake)
	box.secrets.values["KARAKEEP_ACCOUNT_ID"] = "selected-owner"
	box.secrets.values["KARAKEEP_ADMIN_EMAIL"] = "admin@selfhost.local"
	delete(box.secrets.values, "KARAKEEP_HOMARR_API_KEY")
	delete(box.secrets.values, "KARAKEEP_HOMARR_KEY_OWNERSHIP")
	fake.keys = []map[string]any{{"id": "user-resource", "name": "User's bookmark key", "keyId": "user-key-id", "scopes": []string{"bookmarks:readwrite"}}}
	return box
}

func TestKarakeepIssuesOneScopedSessionOwnedKeyAndReusesItsTuple(t *testing.T) {
	box := karakeepIssuanceHarness(t, &fakeKarakeep{})
	if err := (karakeep.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if box.fake.issued != 1 || box.secrets.Get("KARAKEEP_HOMARR_API_KEY") != karakeepTestKey || box.secrets.Get("KARAKEEP_HOMARR_KEY_PENDING") != "" {
		t.Fatal("one captured credential was not committed")
	}
	var ownership struct {
		ID, KeyID, AccountID, Name, Fingerprint string
		Scopes                                  []string
	}
	if json.Unmarshal([]byte(box.secrets.Get("KARAKEEP_HOMARR_KEY_OWNERSHIP")), &ownership) != nil || ownership.ID != "key-resource-1" || ownership.KeyID != karakeepTestKeyID || ownership.AccountID != "selected-owner" || ownership.ID == ownership.KeyID || len(ownership.Scopes) != 1 || ownership.Scopes[0] != "users:read" || len(ownership.Fingerprint) != 64 {
		t.Fatal("resource ID, keyId, intended account or least scope was lost")
	}
	before := maps.Clone(box.secrets.values)
	for range 2 {
		if err := (karakeep.App{}).Setup(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
		if err := (karakeep.App{}).Check(t.Context(), box.h); err != nil {
			t.Fatal(err)
		}
	}
	if box.fake.issued != 1 || !maps.Equal(before, box.secrets.values) || len(box.fake.keys) != 2 || box.fake.keys[0]["id"] != "user-resource" {
		t.Fatal("rerun replaced a valid tuple, issued a duplicate or changed an unrelated key")
	}
	if box.h.Log.Redact(karakeepTestKey) == karakeepTestKey || strings.Contains(fmt.Sprint(box.steps, box.events), karakeepTestKey) {
		t.Fatal("one-time key reached public diagnostics")
	}
}

func TestKarakeepPreinventoryAndNameCollisionsStopBeforeIssuance(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		body      string
		collision bool
	}{
		{name: "name collision", collision: true},
		{name: "missing inventory", body: `{}`},
		{name: "null inventory", body: `{"result":{"data":{"json":{"keys":null}}}}`},
		{name: "tRPC body error", body: `{"error":{"message":"synthetic-private-response"},"result":{"data":{"json":{"keys":[]}}}}`},
		{name: "missing scope metadata", body: `{"result":{"data":{"json":{"keys":[{"id":"user-resource","keyId":"user-key-id","name":"User key"}]}}}}`},
		{name: "duplicate resource IDs", body: `{"result":{"data":{"json":{"keys":[{"id":"same","keyId":"one","name":"User key","scopes":["users:read"]},{"id":"same","keyId":"two","name":"Other key","scopes":["users:read"]}]}}}}`},
		{name: "duplicate key IDs", body: `{"result":{"data":{"json":{"keys":[{"id":"one","keyId":"same","name":"User key","scopes":["users:read"]},{"id":"two","keyId":"same","name":"Other key","scopes":["users:read"]}]}}}}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			box := karakeepIssuanceHarness(t, &fakeKarakeep{inventoryBody: scenario.body})
			if scenario.collision {
				box.fake.keys = append(box.fake.keys, karakeepTestInventory())
			}
			before := maps.Clone(box.secrets.values)
			err := (karakeep.App{}).Setup(t.Context(), box.h)
			if !errors.Is(err, helpers.ErrManual) || box.fake.issued != 0 || !maps.Equal(before, box.secrets.values) {
				t.Fatalf("uncertain inventory caused an issuance or changed records: %v", err)
			}
			if strings.Contains(fmt.Sprint(err, box.events, box.steps), "synthetic-private-response") {
				t.Fatal("raw inventory error leaked")
			}
		})
	}
}

func TestKarakeepCapturePrecedesStatusScopeAndOwnerAcceptance(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		status         int
		body           string
		keyOwner       string
		afterInventory string
	}{
		{name: "unexpected HTTP status", status: http.StatusInternalServerError},
		{name: "broader scope", body: `{"result":{"data":{"json":{"id":"key-resource-1","name":"self-host Homarr statistics","key":"` + karakeepTestKey + `","scopes":["fullaccess"]}}}}`},
		{name: "malformed scope", body: `{"result":{"data":{"json":{"id":"key-resource-1","name":"self-host Homarr statistics","key":"` + karakeepTestKey + `","scopes":"users:read"}}}}`},
		{name: "tRPC body error with key", body: `{"error":{"message":"synthetic-private-response"},"result":{"data":{"json":{"id":"key-resource-1","name":"self-host Homarr statistics","key":"` + karakeepTestKey + `","scopes":["users:read"]}}}}`},
		{name: "preexisting returned resource", body: `{"result":{"data":{"json":{"id":"user-resource","name":"self-host Homarr statistics","key":"` + karakeepTestKey + `","scopes":["users:read"]}}}}`},
		{name: "different bearer account", keyOwner: "unselected-owner"},
		{name: "unavailable post-issuance inventory", afterInventory: `{"result":{"data":{"json":{"keys":null}}}}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			box := karakeepIssuanceHarness(t, &fakeKarakeep{createCode: scenario.status, createBody: scenario.body, keyOwner: scenario.keyOwner, afterInventory: scenario.afterInventory})
			for range 2 {
				err := (karakeep.App{}).Setup(t.Context(), box.h)
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("unaccepted issuance did not remain Manual: %v", err)
				}
				pending, exists, err := karakeepJournal().Pending(box.h)
				if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != karakeepTestKey || strings.Contains(string(pending.Issued.Data), karakeepTestKey) {
					t.Fatalf("one-time secret was not captured before rejection: %v", err)
				}
			}
			if box.fake.issued != 1 || box.secrets.Get("KARAKEEP_HOMARR_API_KEY") != "" || box.secrets.Get("KARAKEEP_HOMARR_KEY_OWNERSHIP") != "" {
				t.Fatal("rejected evidence was accepted or replaced")
			}
		})
	}
}

func TestKarakeepPendingIntentMustRetainTheIntendedAccountAndPreinventory(t *testing.T) {
	for _, intent := range []string{
		`{"accountId":"different-owner","name":"self-host Homarr statistics","scopes":["users:read"],"beforeIds":[],"beforeKeyIds":[]}`,
		`{"accountId":"selected-owner","name":"self-host Homarr statistics","scopes":["fullaccess"],"beforeIds":[],"beforeKeyIds":[]}`,
		`{"accountId":"selected-owner","name":"self-host Homarr statistics","scopes":["users:read"],"beforeIds":[""],"beforeKeyIds":[""]}`,
	} {
		t.Run(intent, func(t *testing.T) {
			box := karakeepIssuanceHarness(t, &fakeKarakeep{})
			if err := karakeepJournal().Begin(box.h, json.RawMessage(intent)); err != nil {
				t.Fatal(err)
			}
			before := maps.Clone(box.secrets.values)
			if err := (karakeep.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("ambiguous intent was accepted: %v", err)
			}
			if box.fake.issued != 0 || !maps.Equal(before, box.secrets.values) {
				t.Fatal("ambiguous pending intent was replaced")
			}
		})
	}
}

func TestKarakeepUncertainRemoteOutcomeNeverReissues(t *testing.T) {
	box := karakeepIssuanceHarness(t, &fakeKarakeep{dropCreate: true})
	for range 2 {
		if err := (karakeep.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("uncertain issuance = %v", err)
		}
	}
	if box.fake.issued != 1 || box.secrets.Get("KARAKEEP_HOMARR_KEY_PENDING") == "" || box.secrets.Get("KARAKEEP_HOMARR_API_KEY") != "" {
		t.Fatal("uncertain issuance lost its hold or issued again")
	}
}

func TestKarakeepCaptureRecoversSelectedSecretFromDamagedResponses(t *testing.T) {
	for _, scenario := range []struct {
		name string
		body string
	}{
		{name: "truncated after key", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `",`},
		{name: "malformed trailing field", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `","scopes":[!`},
		{name: "malformed trailing document", body: `{"result":{"data":{"json":{"id":"key-resource-1","name":"self-host Homarr statistics","scopes":["users:read"],"key":"` + karakeepTestKey + `"}}}} trailing-private-error`},
		{name: "duplicate selected key", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `","key":"unrelated-private-token","id":"key-resource-1","name":"self-host Homarr statistics","scopes":["users:read"]}}}}`},
		{name: "duplicate wrapper", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `","id":"key-resource-1","name":"self-host Homarr statistics","scopes":["users:read"]}}},"result":{"token":"unrelated-private-token"}}`},
		{name: "duplicate resource ID", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `","id":"key-resource-1","id":"other-resource","name":"self-host Homarr statistics","scopes":["users:read"]}}}}`},
		{name: "duplicate scope", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `","id":"key-resource-1","name":"self-host Homarr statistics","scopes":["users:read"],"scopes":["fullaccess"]}}}}`},
		{name: "bounded oversized response", body: `{"result":{"data":{"json":{"key":"` + karakeepTestKey + `","id":"key-resource-1","name":"self-host Homarr statistics","scopes":["users:read"],"unrelated":"` + strings.Repeat("x", 64<<10) + `"}}}}`},
		{name: "unknown values and provider error", body: `{"token":"unrelated-private-token","error":{"message":"trailing-private-error"},"result":{"data":{"json":{"key":"` + karakeepTestKey + `","id":"key-resource-1","name":"self-host Homarr statistics","scopes":["users:read"],"unrelated":{"key":"unrelated-private-token"}}}}}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			box := karakeepIssuanceHarness(t, &fakeKarakeep{createBody: scenario.body})
			for range 2 {
				err := (karakeep.App{}).Setup(t.Context(), box.h)
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("damaged evidence did not stay Manual: %v", err)
				}
				pending, exists, err := karakeepJournal().Pending(box.h)
				if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != karakeepTestKey {
					t.Fatalf("extractable one-time secret was not captured: %v", err)
				}
				for _, value := range box.secrets.values {
					if strings.Contains(value, "unrelated-private-token") || strings.Contains(value, "trailing-private-error") || strings.Contains(value, `"response"`) {
						t.Fatal("unselected response data was persisted")
					}
				}
			}
			if box.fake.issued != 1 || box.secrets.Get("KARAKEEP_HOMARR_API_KEY") != "" {
				t.Fatal("damaged evidence was committed or caused reissuance")
			}
		})
	}
}

func TestKarakeepCaptureStoresOnlyTypedProjectionAndResumesSameIssuance(t *testing.T) {
	body := `{"token":"unrelated-private-token","result":{"data":{"json":{"id":"key-resource-1","name":"self-host Homarr statistics","key":"` + karakeepTestKey + `","scopes":["users:read"],"other":{"token":"unrelated-private-token"}},"meta":{"token":"unrelated-private-token"}}},"debug":"unrelated-private-token"}`
	box := karakeepIssuanceHarness(t, &fakeKarakeep{createBody: body})
	box.secrets.fail = "KARAKEEP_HOMARR_KEY_OWNERSHIP"
	if err := (karakeep.App{}).Setup(t.Context(), box.h); err == nil {
		t.Fatal("storage failure was ignored")
	}
	pending, exists, err := karakeepJournal().Pending(box.h)
	if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != karakeepTestKey {
		t.Fatalf("selected secret did not survive storage failure: %v", err)
	}
	want := `{"version":1,"status":200,"created":{"id":"key-resource-1","name":"self-host Homarr statistics","keyId":"` + karakeepTestKeyID + `","scopes":["users:read"]},"complete":true,"rejected":false,"uncertain":false}`
	if string(pending.Issued.Data) != want {
		t.Fatal("capture was not the exact minimal typed projection")
	}
	for _, value := range box.secrets.values {
		if strings.Contains(value, "unrelated-private-token") {
			t.Fatal("unselected response data was persisted")
		}
	}
	box.secrets.fail = ""
	if err := (karakeep.App{}).Setup(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if box.fake.issued != 1 || box.secrets.Get("KARAKEEP_HOMARR_API_KEY") != karakeepTestKey || box.secrets.Get("KARAKEEP_HOMARR_KEY_PENDING") != "" {
		t.Fatal("projection recovery did not complete the same captured issuance")
	}
}

func TestKarakeepJournalStorageFailuresDoNotDuplicateIssuance(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		key         string
		at          int
		issued      int
		recoverable bool
	}{
		{name: "begin", key: "KARAKEEP_HOMARR_KEY_PENDING", at: 1, issued: 0, recoverable: true},
		{name: "capture", key: "KARAKEEP_HOMARR_KEY_PENDING", at: 2, issued: 1},
		{name: "commit ownership", key: "KARAKEEP_HOMARR_KEY_OWNERSHIP", at: 1, issued: 1, recoverable: true},
		{name: "commit active secret", key: "KARAKEEP_HOMARR_API_KEY", at: 1, issued: 1, recoverable: true},
		{name: "completion marker", key: "KARAKEEP_HOMARR_KEY_PENDING", at: 4, issued: 1, recoverable: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			box := karakeepIssuanceHarness(t, &fakeKarakeep{})
			box.secrets.fail, box.secrets.failAt = scenario.key, scenario.at
			if err := (karakeep.App{}).Setup(t.Context(), box.h); err == nil {
				t.Fatal("storage failure was ignored")
			}
			if box.fake.issued != scenario.issued {
				t.Fatalf("issued %d times before storage failure", box.fake.issued)
			}
			box.secrets.fail = ""
			err := (karakeep.App{}).Setup(t.Context(), box.h)
			if scenario.recoverable {
				if err != nil {
					t.Fatal(err)
				}
				if box.secrets.Get("KARAKEEP_HOMARR_API_KEY") != karakeepTestKey || box.secrets.Get("KARAKEEP_HOMARR_KEY_PENDING") != "" {
					t.Fatal("captured issuance did not recover")
				}
			} else if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("lost capture did not remain Manual: %v", err)
			}
			if box.fake.issued != 1 {
				t.Fatalf("storage retry issued %d keys", box.fake.issued)
			}
		})
	}
}

func TestKarakeepChangedCurrentTupleRemainsUntouched(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*harness)
	}{
		{name: "lost secret", change: func(b *harness) { delete(b.secrets.values, "KARAKEEP_HOMARR_API_KEY") }},
		{name: "lost ownership", change: func(b *harness) { delete(b.secrets.values, "KARAKEEP_HOMARR_KEY_OWNERSHIP") }},
		{name: "rejected key", change: func(b *harness) { b.fake.keyCode = http.StatusUnauthorized }},
		{name: "scope widened", change: func(b *harness) { b.fake.keys[0]["scopes"] = []string{"users:read", "bookmarks:readwrite"} }},
		{name: "resource ID changed", change: func(b *harness) { b.fake.keys[0]["id"] = "different-resource" }},
		{name: "keyId changed", change: func(b *harness) { b.fake.keys[0]["keyId"] = "different-key-id" }},
		{name: "key resource changed name", change: func(b *harness) { b.fake.keys[0]["name"] = "Renamed user key" }},
		{name: "bearer account changed", change: func(b *harness) { b.fake.keyOwner = "different-account" }},
		{name: "malformed journal", change: func(b *harness) { b.secrets.values["KARAKEEP_HOMARR_KEY_PENDING"] = `{"version":1}` }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			box := newHarness(t, &fakeKarakeep{})
			box.secrets.values["KARAKEEP_ACCOUNT_ID"] = "selected-owner"
			box.secrets.values["KARAKEEP_ADMIN_EMAIL"] = "admin@selfhost.local"
			scenario.change(box)
			before := maps.Clone(box.secrets.values)
			if err := (karakeep.App{}).Setup(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("changed tuple was accepted: %v", err)
			}
			if box.fake.issued != 0 || !maps.Equal(before, box.secrets.values) {
				t.Fatal("changed tuple was replaced or rewritten")
			}
		})
	}
}
