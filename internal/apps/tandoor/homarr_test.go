package tandoor_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/apps/tandoor"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
)

func tandoorFreshToken(fixture *tandoorFixture, secrets *tandoorSecrets) {
	fixture.issued = false
	delete(secrets.values, "TANDOOR_HOMARR_API_KEY")
	delete(secrets.values, "TANDOOR_HOMARR_TOKEN_OWNERSHIP")
}

func TestTandoorTokenUsesJournalAndReusesExactOwnerSpaceAndExpiry(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, output, _ := tandoorHarness(t, fixture)
	tandoorFreshToken(fixture, secrets)
	for range 2 {
		if err := (tandoor.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		if err := (tandoor.App{}).Check(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.posts != 1 || tandoorLogins(fixture) != 1 || secrets.writes != 6 || secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] != "" {
		t.Fatalf("unexpected issuance: posts=%d logins=%d writes=%d", fixture.posts, tandoorLogins(fixture), secrets.writes)
	}
	if fixture.inventorySessions != 1 || fixture.inventoryBearers == 0 {
		t.Fatal("issuance skipped supported owner-session pre-inventory")
	}
	var intent struct {
		Intent struct {
			Inventory struct {
				Count      int
				MaxID      int64
				ObservedAt time.Time
			} `json:"inventory"`
		} `json:"intent"`
	}
	if json.Unmarshal([]byte(secrets.saved[0]), &intent) != nil || intent.Intent.Inventory.Count != 1 || intent.Intent.Inventory.MaxID != 7 || intent.Intent.Inventory.ObservedAt.IsZero() {
		t.Fatal("issuance did not retain minimal novelty evidence")
	}
	if !strings.Contains(secrets.saved[0], `"issued":null`) || !strings.Contains(secrets.saved[1], `"secret":"tda_fixture_read_key_1234"`) {
		t.Fatal("intent and one-time capture were not saved before acceptance")
	}
	for _, saved := range secrets.saved {
		if strings.Contains(saved, "unrelated-private-token") || strings.Contains(saved, `"id":92`) {
			t.Fatal("persisted an all-token inventory")
		}
	}
	if strings.Contains(output.String(), "tda_fixture_read_key_1234") || strings.Contains(output.String(), "unrelated-private-token") {
		t.Fatal("credential escaped into output")
	}
	identity, err := tandoor.HomarrIdentity(h)
	if err != nil || identity.AccountID != 17 || identity.ActiveSpaceID != 31 || identity.FirstSpaceID != 31 {
		t.Fatalf("incorrect identity: %+v %v", identity, err)
	}
}

func TestTandoorTokenPersistenceFailuresNeverReissue(t *testing.T) {
	for failAt := 1; failAt <= 6; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, output, results := tandoorHarness(t, fixture)
			tandoorFreshToken(fixture, secrets)
			secrets.failAt = failAt
			err := (tandoor.App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrStepFailed) || errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-store-canary") || strings.Contains(output.String(), "tda_fixture_read_key_1234") {
				t.Fatalf("unsafe persistence failure: %v", err)
			}
			if len(*results) != 3 || (*results)[2].Status != status.StepFail {
				t.Fatal("storage failure was not recorded as Failed")
			}
			secrets.failAt = 0
			fresh := helpers.New(h.Config)
			resumeErr := (tandoor.App{}).Setup(t.Context(), fresh)
			if failAt == 2 {
				if !errors.Is(resumeErr, helpers.ErrManual) {
					t.Fatalf("uncaptured one-time credential was accepted: %v", resumeErr)
				}
			} else if resumeErr != nil {
				t.Fatal(resumeErr)
			}
			if fixture.posts != 1 {
				t.Fatalf("credential reissued: %d", fixture.posts)
			}
		})
	}
}

func TestTandoorUncertainResponsesAreCapturedAndHeldWithoutReissue(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"missing token", `{"id":91,"scope":"read"}`, 201},
		{"malformed", `{"token":"tda_private_canary",`, 201},
		{"server uncertainty", `{"token":"tda_private_canary","id":91}`, 500},
		{"redirect", `{}`, 307},
		{"over-scoped", `{"token":"tda_private_canary","id":91,"scope":"read write"}`, 201},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, output, _ := tandoorHarness(t, fixture)
			tandoorFreshToken(fixture, secrets)
			fixture.issueBody, fixture.issueStatus = test.body, test.status
			if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("uncertainty accepted: %v", err)
			}
			before := maps.Clone(secrets.values)
			if err := (tandoor.App{}).Setup(t.Context(), helpers.New(h.Config)); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("uncertainty resumed: %v", err)
			}
			if fixture.posts != 1 || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" || secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] == "" || !maps.Equal(before, secrets.values) {
				t.Fatal("uncertainty replaced credentials or journal")
			}
			if strings.Contains(output.String(), "tda_private_canary") {
				t.Fatal("response token leaked")
			}
		})
	}
}

func TestTandoorIntentOnlyAndMalformedPendingNeverIssue(t *testing.T) {
	for _, pending := range []string{`{"version":1,"intent":{"accountId":17},"issued":null}`, `not-json`} {
		fixture := newTandoorFixture()
		h, secrets, _, _ := tandoorHarness(t, fixture)
		tandoorFreshToken(fixture, secrets)
		secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] = pending
		if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || fixture.posts != 0 || secrets.writes != 0 {
			t.Fatalf("unsafe pending resume: %v", err)
		}
	}
}

func TestTandoorIssuedCredentialIsCapturedBeforeSemanticVerification(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, _, _ := tandoorHarness(t, fixture)
	tandoorFreshToken(fixture, secrets)
	fixture.afterIssue = func() { fixture.space = strings.ReplaceAll(fixture.space, `"id":31`, `"id":44`) }
	if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending"}
	pending, exists, err := journal.Pending(h)
	if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != "tda_fixture_read_key_1234" || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" {
		t.Fatalf("one-time capture was lost: %v", err)
	}
	fixture.space = newTandoorFixture().space
	if err := (tandoor.App{}).Setup(t.Context(), helpers.New(h.Config)); err != nil || fixture.posts != 1 {
		t.Fatalf("captured credential did not resume: %v", err)
	}
}

func TestTandoorSavedTokenMetadataAndIdentityRemainReadOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*tandoorFixture, *tandoorSecrets)
	}{
		{"lost key", func(_ *tandoorFixture, s *tandoorSecrets) { delete(s.values, "TANDOOR_HOMARR_API_KEY") }},
		{"lost ownership", func(_ *tandoorFixture, s *tandoorSecrets) { delete(s.values, "TANDOOR_HOMARR_TOKEN_OWNERSHIP") }},
		{"legacy ownership without inventory", func(_ *tandoorFixture, s *tandoorSecrets) {
			var ownership map[string]json.RawMessage
			if err := json.Unmarshal([]byte(s.values["TANDOOR_HOMARR_TOKEN_OWNERSHIP"]), &ownership); err != nil {
				t.Fatal(err)
			}
			delete(ownership, "inventory")
			body, err := json.Marshal(ownership)
			if err != nil {
				t.Fatal(err)
			}
			s.values["TANDOOR_HOMARR_TOKEN_OWNERSHIP"] = string(body)
		}},
		{"wrong key", func(_ *tandoorFixture, s *tandoorSecrets) {
			s.values["TANDOOR_HOMARR_API_KEY"] = "tda_another_key_1234"
		}},
		{"wrong scope", func(f *tandoorFixture, _ *tandoorSecrets) {
			f.inventory = `[{"id":91,"scope":"write","token":"tda_fixture_read_key_1234"}]`
		}},
		{"missing inventory", func(f *tandoorFixture, _ *tandoorSecrets) { f.inventory = `[]` }},
		{"wrong first Space", func(f *tandoorFixture, _ *tandoorSecrets) { f.spaces = `[{"id":44},{"id":31}]` }},
		{"missing first Space", func(f *tandoorFixture, _ *tandoorSecrets) { f.spaces = `[]` }},
		{"rejected key", func(f *tandoorFixture, _ *tandoorSecrets) { f.statuses["/api/access-token/"] = http.StatusForbidden }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, _, _ := tandoorHarness(t, fixture)
			test.change(fixture, secrets)
			before := maps.Clone(secrets.values)
			if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("invalid token accepted: %v", err)
			}
			if fixture.posts != 0 || secrets.writes != 0 || !maps.Equal(before, secrets.values) {
				t.Fatal("saved credential replaced")
			}
		})
	}
}

func TestTandoorFirstAndActiveSpacesCanDifferAndArraysAreSupported(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, _, _ := tandoorHarness(t, fixture)
	fixture.spaces = `[{"id":44},{"id":31}]`
	var ownership map[string]any
	if err := json.Unmarshal([]byte(secrets.values["TANDOOR_HOMARR_TOKEN_OWNERSHIP"]), &ownership); err != nil {
		t.Fatal(err)
	}
	ownership["firstSpaceId"] = 44
	body, _ := json.Marshal(ownership)
	secrets.values["TANDOOR_HOMARR_TOKEN_OWNERSHIP"] = string(body)
	if err := tandoor.VerifyHomarr(t.Context(), h); err != nil {
		t.Fatal(err)
	}
}

func TestTandoorExactScopeExpiryAndMaskedCredentialAreRequired(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"write scope", func(token map[string]any) { token["scope"] = "read write" }},
		{"changed expiry", func(token map[string]any) { token["expires"] = "2035-01-01T00:00:00Z" }},
		{"wrong token suffix", func(token map[string]any) { token["token"] = "tda_************_******_***********5678" }},
		{"wrong token ID", func(token map[string]any) { token["id"] = 92 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, _, _ := tandoorHarness(t, fixture)
			token := map[string]any{"id": 91, "scope": "read", "expires": fixture.expires, "token": "tda_************_******_***********1234"}
			test.change(token)
			body, err := json.Marshal([]map[string]any{token})
			if err != nil {
				t.Fatal(err)
			}
			fixture.inventory = string(body)
			if err := tandoor.VerifyHomarr(t.Context(), h); !errors.Is(err, helpers.ErrManual) || secrets.writes != 0 || fixture.posts != 0 {
				t.Fatalf("token metadata was changed or accepted: %v", err)
			}
		})
	}
}

func TestTandoorTokenIssuanceThrottlePreservesPendingAttempt(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, _, _ := tandoorHarness(t, fixture)
	tandoorFreshToken(fixture, secrets)
	fixture.issueStatus = http.StatusTooManyRequests
	for range 2 {
		err := (tandoor.App{}).Setup(t.Context(), h)
		var manual *helpers.ManualError
		if !errors.As(err, &manual) || !strings.Contains(manual.Hint, "HTTP 429") || !strings.Contains(manual.Hint, "remains pending") {
			t.Fatalf("token throttle lost its hold: %v", err)
		}
	}
	if fixture.posts != 1 || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" || secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] == "" {
		t.Fatal("throttled issuance was retried or accepted")
	}
}

type tandoorRoundTripper func(*http.Request) (*http.Response, error)

func (f tandoorRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTandoorLostIssuanceResponseDoesNotExposeOrReissue(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, output, _ := tandoorHarness(t, fixture)
	tandoorFreshToken(fixture, secrets)
	client := *h.Config.HTTP
	transport := client.Transport
	client.Transport = tandoorRoundTripper(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost && r.URL.Path == "/api/access-token/" {
			response.Body.Close()
			return nil, errors.New("transport-private-canary tda_fixture_read_key_1234")
		}
		return response, err
	})
	h.Config.HTTP = &client
	for range 2 {
		err := (tandoor.App{}).Setup(t.Context(), h)
		if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "transport-private-canary") {
			t.Fatalf("lost response was not safely held: %v", err)
		}
	}
	if fixture.posts != 1 || secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] == "" || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" || strings.Contains(output.String(), "transport-private-canary") {
		t.Fatal("lost response caused replacement, acceptance or private output")
	}
}

func TestTandoorExpiredCredentialNeverRotates(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, _, _ := tandoorHarness(t, fixture)
	expires, err := time.Parse(time.RFC3339, fixture.expires)
	if err != nil {
		t.Fatal(err)
	}
	h.Now = func() time.Time { return expires.Add(time.Second) }
	before := maps.Clone(secrets.values)
	if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || secrets.writes != 0 || fixture.posts != 0 || !maps.Equal(before, secrets.values) {
		t.Fatalf("expired credential was rotated or accepted: %v", err)
	}
}

func TestTandoorPreInventoryMustBeSupportedAndValidatedBeforeBegin(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		code int
	}{
		{"unsupported endpoint", "", http.StatusNotFound},
		{"forbidden owner inventory", "", http.StatusForbidden},
		{"null inventory", `null`, 200},
		{"unsupported pagination", `{"results":[]}`, 200},
		{"malformed", `[{`, 200},
		{"missing scope and expiry", `[{"id":7}]`, 200},
		{"unknown rights", `[{"id":7,"scope":"all","expires":"2030-01-01T00:00:00Z"}]`, 200},
		{"duplicate IDs", `[{"id":7,"scope":"read","expires":"2030-01-01T00:00:00Z"},{"id":7,"scope":"write","expires":"2030-01-01T00:00:00Z"}]`, 200},
		{"duplicate metadata field", `[{"id":91,"id":7,"scope":"read","expires":"2030-01-01T00:00:00Z"}]`, 200},
		{"case-folded duplicate field", `[{"id":91,"ID":7,"scope":"read","expires":"2030-01-01T00:00:00Z"}]`, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, _, results := tandoorHarness(t, fixture)
			tandoorFreshToken(fixture, secrets)
			fixture.inventory = test.body
			fixture.statuses["/api/access-token/"] = test.code
			before := maps.Clone(secrets.values)
			if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || fixture.posts != 0 || secrets.writes != 0 || !maps.Equal(before, secrets.values) {
				t.Fatalf("unsupported pre-inventory authorized issuance: %v", err)
			}
			if len(*results) != 3 || (*results)[2].Status != status.StepManual {
				t.Fatal("invalid inventory did not remain Manual")
			}
		})
	}
}

func TestTandoorPreInventoryOccursBeforeJournalAndExistingIDCannotBeAccepted(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, _, _ := tandoorHarness(t, fixture)
	tandoorFreshToken(fixture, secrets)
	fixture.beforeInventory = func() {
		if !fixture.issued && (secrets.writes != 0 || secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] != "") {
			t.Error("journal preceded pre-inventory")
		}
	}
	fixture.inventory = fmt.Sprintf(`[{"id":91,"scope":"read","expires":%q,"token":"unrelated-private-token"}]`, fixture.expires)
	if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || fixture.posts != 1 || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" {
		t.Fatalf("preexisting token ID was adopted: %v", err)
	}
	before := maps.Clone(secrets.values)
	if err := (tandoor.App{}).Setup(t.Context(), helpers.New(h.Config)); !errors.Is(err, helpers.ErrManual) || fixture.posts != 1 || !maps.Equal(before, secrets.values) {
		t.Fatalf("collision caused replacement or Commit: %v", err)
	}
}

func TestTandoorIncrementalCapturePreservesKeyAndPermanentOutcomeHold(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(string) string
		flag   string
	}{
		{"trailing invalid JSON", func(body string) string { return body + ` invalid` }, "decodeError"},
		{"trailing object", func(body string) string { return body + `{}` }, "decodeError"},
		{"later malformed member", func(body string) string { return strings.TrimSuffix(body, "}") + `,"other":` }, "decodeError"},
		{"incomplete selected metadata", func(string) string { return `{"token":"tda_fixture_read_key_1234","id":91}` }, "incomplete"},
		{"wrong ID type", func(body string) string { return strings.Replace(body, `"id":91`, `"id":true`, 1) }, "decodeError"},
		{"type error then valid projection", func(body string) string { return strings.Replace(body, `"id":91`, `"id":"invalid","id":91`, 1) }, "decodeError"},
		{"duplicate token", func(body string) string { return strings.TrimSuffix(body, "}") + `,"token":"tda_another_secret"}` }, "duplicate"},
		{"duplicate unknown field", func(body string) string { return strings.TrimSuffix(body, "}") + `,"other":1,"other":2}` }, "duplicate"},
		{"status type error", func(body string) string { return strings.TrimSuffix(body, "}") + `,"status":"invalid"}` }, "reportedError"},
		{"response body error", func(body string) string { return strings.TrimSuffix(body, "}") + `,"error":"private-error-canary"}` }, "reportedError"},
		{"bounded response", func(body string) string {
			return strings.TrimSuffix(body, "}") + `,"padding":"` + strings.Repeat("private-padding", 6000) + `"}`
		}, "limited"},
		{"bounded field count", func(body string) string {
			body = strings.TrimSuffix(body, "}")
			for index := range 65 {
				body += fmt.Sprintf(`,"field%d":0`, index)
			}
			return body + "}"
		}, "limited"},
		{"bounded selected value", func(body string) string {
			return strings.Replace(body, `"scope":"read"`, `"scope":"`+strings.Repeat("private-padding", 100)+`"`, 1)
		}, "limited"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, output, results := tandoorHarness(t, fixture)
			tandoorFreshToken(fixture, secrets)
			valid := fmt.Sprintf(`{"token":"tda_fixture_read_key_1234","id":91,"scope":"read","expires":%q}`, fixture.expires)
			fixture.issueBody = test.change(valid)
			err := (tandoor.App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) || len(*results) != 3 || (*results)[2].Status != status.StepManual {
				t.Fatalf("uncertain response did not remain Manual: %v", err)
			}
			journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending"}
			pending, exists, err := journal.Pending(h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != "tda_fixture_read_key_1234" {
				t.Fatalf("recoverable one-time key was discarded: %v", err)
			}
			var data struct {
				Outcome map[string]bool `json:"outcome"`
			}
			if json.Unmarshal(pending.Issued.Data, &data) != nil || data.Outcome["complete"] || !data.Outcome[test.flag] {
				t.Fatalf("uncertainty outcome was not persisted: %s", pending.Issued.Data)
			}
			before := maps.Clone(secrets.values)
			for range 2 {
				if err := (tandoor.App{}).Setup(t.Context(), helpers.New(h.Config)); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("uncertainty was accepted on retry: %v", err)
				}
			}
			if fixture.posts != 1 || secrets.writes != 2 || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" || secrets.values["TANDOOR_HOMARR_TOKEN_OWNERSHIP"] != "" || !maps.Equal(before, secrets.values) {
				t.Fatal("retry committed or reissued uncertain evidence")
			}
			raw := secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"]
			if len(raw) > 4096 || strings.Contains(raw, "private-padding") || strings.Contains(raw, "private-error-canary") || strings.Contains(raw, "tda_another_secret") || strings.Contains(output.String(), "tda_fixture_read_key_1234") {
				t.Fatal("capture persisted a raw response, another token or private output")
			}
		})
	}
}

func TestTandoorMissingInventoryOrOutcomeEvidenceDoesNotAuthorizeCommit(t *testing.T) {
	for _, remove := range []string{"inventory", "outcome"} {
		t.Run(remove, func(t *testing.T) {
			fixture := newTandoorFixture()
			h, secrets, _, _ := tandoorHarness(t, fixture)
			tandoorFreshToken(fixture, secrets)
			secrets.failAt = 3
			if err := (tandoor.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrStepFailed) {
				t.Fatalf("did not retain captured checkpoint: %v", err)
			}
			secrets.failAt = 0
			var pending map[string]json.RawMessage
			if json.Unmarshal([]byte(secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"]), &pending) != nil {
				t.Fatal("bad fixture checkpoint")
			}
			if remove == "inventory" {
				var intent map[string]json.RawMessage
				json.Unmarshal(pending["intent"], &intent)
				delete(intent, "inventory")
				pending["intent"], _ = json.Marshal(intent)
			} else {
				var issued map[string]json.RawMessage
				var data map[string]json.RawMessage
				json.Unmarshal(pending["issued"], &issued)
				json.Unmarshal(issued["data"], &data)
				delete(data, "outcome")
				issued["data"], _ = json.Marshal(data)
				pending["issued"], _ = json.Marshal(issued)
			}
			body, _ := json.Marshal(pending)
			secrets.values["TANDOOR_HOMARR_TOKEN_PENDING"] = string(body)
			before := maps.Clone(secrets.values)
			if err := (tandoor.App{}).Setup(t.Context(), helpers.New(h.Config)); !errors.Is(err, helpers.ErrManual) || fixture.posts != 1 || !maps.Equal(before, secrets.values) {
				t.Fatalf("missing evidence resumed or replaced: %v", err)
			}
		})
	}
}

func TestTandoorUncertainResponseCaptureStorageFailureRemainsFailed(t *testing.T) {
	fixture := newTandoorFixture()
	h, secrets, output, results := tandoorHarness(t, fixture)
	tandoorFreshToken(fixture, secrets)
	fixture.issueBody = fmt.Sprintf(`{"token":"tda_fixture_read_key_1234","id":91,"scope":"read","expires":%q} trailing`, fixture.expires)
	secrets.failAt = 2
	err := (tandoor.App{}).Setup(t.Context(), h)
	if !errors.Is(err, helpers.ErrStepFailed) || errors.Is(err, helpers.ErrManual) || len(*results) != 3 || (*results)[2].Status != status.StepFail || strings.Contains(output.String(), "tda_fixture_read_key_1234") {
		t.Fatalf("capture storage failure was hidden as semantic Manual: %v", err)
	}
	secrets.failAt = 0
	if err := (tandoor.App{}).Setup(t.Context(), helpers.New(h.Config)); !errors.Is(err, helpers.ErrManual) || fixture.posts != 1 || secrets.values["TANDOOR_HOMARR_API_KEY"] != "" {
		t.Fatalf("uncaptured uncertain response was retried or accepted: %v", err)
	}
}
