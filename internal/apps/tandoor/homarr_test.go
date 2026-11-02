package tandoor_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/apps/tandoor"
	"github.com/ajnart/self-host/internal/helpers"
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
			h, secrets, output, _ := tandoorHarness(t, fixture)
			tandoorFreshToken(fixture, secrets)
			secrets.failAt = failAt
			err := (tandoor.App{}).Setup(t.Context(), h)
			if err == nil || strings.Contains(err.Error(), "private-store-canary") || strings.Contains(output.String(), "tda_fixture_read_key_1234") {
				t.Fatalf("unsafe persistence failure: %v", err)
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
