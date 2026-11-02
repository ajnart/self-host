package mealie_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/mealie"
	"github.com/ajnart/self-host/internal/helpers"
)

type mealieRoundTrip func(*http.Request) (*http.Response, error)

func (f mealieRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mealieFreshIssuance(t *testing.T) (*mealieFake, *mealieHarness) {
	t.Helper()
	f := newMealieFake()
	f.user["tokens"] = []any{}
	b := newMealieHarness(t, f)
	b.secrets.allowWrites = true
	delete(b.secrets.values, "MEALIE_HOMARR_API_KEY")
	delete(b.secrets.values, "MEALIE_HOMARR_KEY_OWNERSHIP")
	f.onIssue = func() {
		if b.secrets.values["MEALIE_HOMARR_KEY_PENDING"] == "" {
			t.Error("issuance before acknowledged intent")
		}
	}
	return f, b
}

func TestMealieIssuanceUsesSharedJournalAndReusesFullAccessKey(t *testing.T) {
	f, b := mealieFreshIssuance(t)
	for range 2 {
		if err := (mealie.App{}).Setup(t.Context(), b.h); err != nil {
			t.Fatal(err)
		}
	}
	if err := (mealie.App{}).Check(t.Context(), b.h); err != nil {
		t.Fatal(err)
	}
	if f.issued != 1 || b.secrets.writes != 6 || b.secrets.values["MEALIE_HOMARR_API_KEY"] != "existing-unmodified-key" || b.secrets.values["MEALIE_HOMARR_KEY_PENDING"] != "" {
		t.Fatalf("issuance=%d writes=%d", f.issued, b.secrets.writes)
	}
	var owned struct {
		Intent struct {
			Authority string          `json:"authority"`
			Identity  mealie.Identity `json:"identity"`
		} `json:"intent"`
	}
	if json.Unmarshal([]byte(b.secrets.values["MEALIE_HOMARR_KEY_OWNERSHIP"]), &owned) != nil || owned.Intent.Authority != "full-account-access" || owned.Intent.Identity.HouseholdID != mealieHouseholdID {
		t.Fatal("ownership lost full-access/actual-household semantics")
	}
}

func TestMealieIssuanceCapturesSecretBeforeRejectingBadEvidence(t *testing.T) {
	for _, scenario := range []struct {
		name, body string
		status     int
	}{
		{"bad ID", `{"token":"existing-unmodified-key","id":"invalid","name":"self-host Homarr statistics","createdAt":"2026-11-02T12:00:00Z"}`, 201},
		{"bad timestamp", `{"token":"existing-unmodified-key","id":7,"name":"self-host Homarr statistics","createdAt":null}`, 201},
		{"wrong name", `{"token":"existing-unmodified-key","id":7,"name":"another token","createdAt":"2026-11-02T12:00:00Z"}`, 201},
		{"partial JSON", `{"token":"existing-unmodified-key","id":`, 201},
		{"duplicate secret", `{"token":"existing-unmodified-key","token":"other-secret","id":7}`, 201},
		{"failed status", `{"token":"existing-unmodified-key","id":7,"name":"self-host Homarr statistics","createdAt":"2026-11-02T12:00:00Z"}`, 500},
		{"missing secret", `{"id":7,"name":"self-host Homarr statistics","createdAt":"2026-11-02T12:00:00Z"}`, 201},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, b := mealieFreshIssuance(t)
			f.issueBody, f.issueStatus = scenario.body, scenario.status
			for range 2 {
				if err := (mealie.App{}).Setup(t.Context(), b.h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("want held evidence, got %v", err)
				}
			}
			journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
			pending, exists, err := journal.Pending(b.h)
			wantSecret := "existing-unmodified-key"
			if scenario.name == "missing secret" {
				wantSecret = ""
			}
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != wantSecret || f.issued != 1 || b.secrets.values["MEALIE_HOMARR_API_KEY"] != "" {
				t.Fatalf("one-time secret was lost or accepted; issued=%d err=%v", f.issued, err)
			}
			if strings.Contains(b.output.String(), "existing-unmodified-key") || strings.Contains(b.output.String(), "other-secret") {
				t.Fatal("captured secret leaked")
			}
		})
	}
}

func TestMealieRejectedKeyAndPendingSelectionRemainHeld(t *testing.T) {
	f, b := mealieFreshIssuance(t)
	if err := (mealie.App{}).Setup(t.Context(), b.h); err != nil {
		t.Fatal(err)
	}
	f.rejectKey = true
	before := maps.Clone(b.secrets.values)
	if err := (mealie.App{}).Setup(t.Context(), b.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("rejected saved key=%v", err)
	}
	if f.issued != 1 || !maps.Equal(before, b.secrets.values) {
		t.Fatal("rejected key caused replacement or lost records")
	}
	f2, b2 := mealieFreshIssuance(t)
	b2.secrets.failAt = 3
	if err := (mealie.App{}).Setup(t.Context(), b2.h); err == nil {
		t.Fatal("checkpoint failure passed")
	}
	pending := b2.secrets.values["MEALIE_HOMARR_KEY_PENDING"]
	b2.secrets.failAt = 0
	b2.secrets.values["MEALIE_HOUSEHOLD_ID"] = mealieOtherID
	f2.user["householdId"], f2.household["id"] = mealieOtherID, mealieOtherID
	if err := (mealie.App{}).Setup(t.Context(), b2.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("changed pending selection=%v", err)
	}
	if f2.issued != 1 || b2.secrets.values["MEALIE_HOMARR_KEY_PENDING"] != pending {
		t.Fatal("changed selection rewrote pending evidence or issued again")
	}
}

func TestMealieLostIssuanceResponsePreservesIntentWithoutReissue(t *testing.T) {
	f, b := mealieFreshIssuance(t)
	transport := b.h.Config.HTTP.Transport
	b.h.Config.HTTP.Transport = mealieRoundTrip(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && r.URL.Path == "/api/users/api-tokens" {
			response.Body.Close()
			return nil, errors.New("transport-private-canary")
		}
		return response, err
	})
	for range 2 {
		if err := (mealie.App{}).Setup(t.Context(), b.h); !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "transport-private-canary") {
			t.Fatalf("unconfirmed issuance did not hold safely: %v", err)
		}
	}
	journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
	pending, exists, err := journal.Pending(b.h)
	if err != nil || !exists || pending.Issued != nil || f.issued != 1 || b.secrets.values["MEALIE_HOMARR_API_KEY"] != "" {
		t.Fatal("lost response permitted reissuance or discarded its intent")
	}
}

func TestMealieIssuanceWriteFailureNeverReissues(t *testing.T) {
	for failAt := 1; failAt <= 6; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			f, b := mealieFreshIssuance(t)
			b.secrets.failAt = failAt
			if err := (mealie.App{}).Setup(t.Context(), b.h); err == nil || strings.Contains(err.Error(), "existing-unmodified-key") || strings.Contains(err.Error(), "synthetic private") {
				t.Fatalf("failed persistence did not stop safely: %v", err)
			}
			issued := f.issued
			if failAt == 1 {
				if issued != 0 {
					t.Fatal("failed intent authorized issuance")
				}
				return
			}
			b.secrets.failAt = 0
			err := (mealie.App{}).Setup(t.Context(), b.h)
			if f.issued != issued {
				t.Fatal("persistence recovery issued another token")
			}
			if failAt == 2 {
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatal("intent-only failure was not held")
				}
			} else if err != nil {
				t.Fatalf("exact captured completion failed: %v", err)
			}
		})
	}
}

func TestMealieExistingCollisionRejectedAndChangedSelectionNeverIssue(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*mealieFake, *mealieHarness)
	}{
		{"name collision", func(f *mealieFake, b *mealieHarness) { f.user["tokens"] = newMealieFake().user["tokens"] }},
		{"saved key missing ownership", func(_ *mealieFake, b *mealieHarness) {
			b.secrets.values["MEALIE_HOMARR_API_KEY"] = "existing-unmodified-key"
		}},
		{"wrong user", func(f *mealieFake, _ *mealieHarness) { f.user["id"] = mealieOtherID }},
		{"wrong group", func(f *mealieFake, _ *mealieHarness) { f.user["groupId"] = mealieOtherID }},
		{"wrong household", func(f *mealieFake, _ *mealieHarness) { f.user["householdId"] = mealieOtherID }},
		{"missing inventory", func(f *mealieFake, _ *mealieHarness) { delete(f.user, "tokens") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, b := mealieFreshIssuance(t)
			scenario.change(f, b)
			before := maps.Clone(b.secrets.values)
			if err := (mealie.App{}).Setup(t.Context(), b.h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unsafe prerequisites passed: %v", err)
			}
			if f.issued != 0 || !maps.Equal(before, b.secrets.values) {
				t.Fatal("collision/identity hold mutated private state or issued a token")
			}
		})
	}
	f, b := mealieFreshIssuance(t)
	if err := (mealie.App{}).Setup(t.Context(), b.h); err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(b.secrets.values)
	b.secrets.values["MEALIE_HOUSEHOLD_ID"] = mealieOtherID
	f.user["householdId"], f.household["id"] = mealieOtherID, mealieOtherID
	if err := (mealie.App{}).Setup(t.Context(), b.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("changed explicit selection adopted an old key")
	}
	if f.issued != 1 || b.secrets.values["MEALIE_HOMARR_KEY_OWNERSHIP"] != before["MEALIE_HOMARR_KEY_OWNERSHIP"] {
		t.Fatal("changed selection replaced ownership or issued a key")
	}
}
