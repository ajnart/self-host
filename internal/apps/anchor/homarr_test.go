package anchor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/apps/anchor"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

const (
	issuedToken   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	foreignToken  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	adminIdentity = `{"id":"existing-admin","email":"admin@selfhost.local"}`
)

func ownershipFor(token string) string {
	sum := sha256.Sum256([]byte(token))
	return `{"accountId":"existing-admin","email":"admin@selfhost.local","fingerprint":"` + hex.EncodeToString(sum[:]) + `"}`
}

type tokenFake struct {
	token          *string
	regenerateCode int
	calls          []string
	unexpected     []string
	deniedToken    bool
}

func tokenValue(value string) *string { return &value }

func (f *tokenFake) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.Method + " " + r.URL.RequestURI()
	f.calls = append(f.calls, key)
	status, body := http.StatusOK, ""
	auth := r.Header.Get("Authorization")
	jwt := auth == "Bearer synthetic-login-jwt"
	switch key {
	case "GET /api/health":
		body = `{"app":"anchor","status":"ok","version":"0.17.2"}`
	case "POST /api/auth/login":
		body = loginJSON
	case "GET /api/auth/me":
		body = userJSON
	case "GET /api/notes?limit=1":
		body = "[" + noteJSON + "]"
		if !jwt && (f.deniedToken || f.token == nil || auth != "Bearer "+*f.token) {
			status, body = http.StatusUnauthorized, ""
		}
	case "GET /api/auth/api-token":
		body = `{"apiToken":null}`
		if f.token != nil {
			body = `{"apiToken":"` + *f.token + `"}`
		}
	case "POST /api/auth/api-token/regenerate":
		status = f.regenerateCode
		if status == http.StatusOK {
			f.token = tokenValue(issuedToken)
			body = `{"apiToken":"` + issuedToken + `"}`
		} else {
			body = `upstream-private-error`
		}
	default:
		f.unexpected = append(f.unexpected, key)
		status = http.StatusNotFound
	}
	if key != "GET /api/health" && key != "POST /api/auth/login" && key != "GET /api/notes?limit=1" && !jwt {
		f.unexpected = append(f.unexpected, "non-JWT "+key)
		status, body = http.StatusUnauthorized, ""
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (f *tokenFake) count(key string) int {
	count := 0
	for _, call := range f.calls {
		if call == key {
			count++
		}
	}
	return count
}

type tokenBox struct {
	h       *helpers.H
	results []helpers.StepResult
	events  []events.Event
}

func newTokenBox(t *testing.T, fake *tokenFake, sec *secrets, homarr bool) *tokenBox {
	t.Helper()
	box := &tokenBox{}
	clock := systemtest.NewClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	cfg := helpers.Config{
		App: "anchor", Service: "anchor", ContainerPort: 3000,
		URL: "http://anchor.invalid", BaseURL: "http://anchor.invalid",
		Secrets: sec, HTTP: &http.Client{Transport: fake},
		Now: clock.Now, Sleep: clock.Sleep,
		Log:    events.New(events.SinkFunc(func(e events.Event) { box.events = append(box.events, e) })),
		Record: func(result helpers.StepResult) { box.results = append(box.results, result) },
	}
	if homarr {
		cfg.Peer = func(id string) *helpers.H {
			if id == "homarr" {
				return helpers.New(helpers.Config{App: "homarr", Log: events.Discard()})
			}
			return nil
		}
	}
	box.h = helpers.New(cfg)
	t.Cleanup(func() {
		if len(fake.unexpected) != 0 {
			t.Errorf("unexpected requests: %v", fake.unexpected)
		}
	})
	return box
}

func tokenSecrets() *secrets {
	sec := newSecrets()
	delete(sec.values, "ANCHOR_HOMARR_API_KEY")
	sec.values["ANCHOR_ADMIN_IDENTITY"] = adminIdentity
	sec.durable = map[string]string{}
	for key, value := range sec.values {
		sec.durable[key] = value
	}
	sec.writes = nil
	return sec
}

func (b *tokenBox) assertPrivate(t *testing.T, err error) {
	t.Helper()
	text := ""
	for _, result := range b.results {
		text += result.Msg + result.Error + result.Hint
	}
	for _, event := range b.events {
		text += fmt.Sprint(event)
	}
	if err != nil {
		text += err.Error()
	}
	for _, value := range []string{issuedToken, foreignToken, "synthetic-login-jwt", "synthetic-password", "upstream-private-error", "private-note-title"} {
		if strings.Contains(text, value) {
			t.Errorf("private value appeared in diagnostics: %s", value)
		}
	}
}

func TestHomarrTokenIsIssuedOnceForTheVerifiedAccountAndReused(t *testing.T) {
	fake, sec := &tokenFake{regenerateCode: http.StatusOK}, tokenSecrets()
	box := newTokenBox(t, fake, sec, true)
	if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if sec.durable["ANCHOR_HOMARR_API_KEY"] != issuedToken || sec.durable["ANCHOR_HOMARR_TOKEN_OWNERSHIP"] != ownershipFor(issuedToken) || sec.durable["ANCHOR_HOMARR_TOKEN_PENDING"] != "" {
		t.Fatal("the issued token was not committed with its account ownership and a cleared journal")
	}
	if fake.count("POST /api/auth/api-token/regenerate") != 1 {
		t.Fatal("expected exactly one token issuance")
	}
	box = newTokenBox(t, fake, sec, true)
	if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if err := anchor.VerifyHomarr(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.count("POST /api/auth/api-token/regenerate") != 1 || box.step("homarr-token").Status != stepstatus.StepSkip {
		t.Fatal("a verified saved token must be reused without another issuance")
	}
	box.assertPrivate(t, nil)
}

func (b *tokenBox) step(id string) helpers.StepResult {
	for i := len(b.results) - 1; i >= 0; i-- {
		if b.results[i].ID == id {
			return b.results[i]
		}
	}
	return helpers.StepResult{}
}

func TestHomarrTokenIsSkippedWithoutHomarr(t *testing.T) {
	fake, sec := &tokenFake{regenerateCode: http.StatusOK}, tokenSecrets()
	box := newTokenBox(t, fake, sec, false)
	if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if fake.count("GET /api/auth/api-token") != 0 || sec.durable["ANCHOR_HOMARR_API_KEY"] != "" {
		t.Fatal("no token work is allowed without Homarr")
	}
}

func TestExistingOrUnrecordedPermanentTokenIsNeverReplaced(t *testing.T) {
	fake, sec := &tokenFake{token: tokenValue(foreignToken), regenerateCode: http.StatusOK}, tokenSecrets()
	box := newTokenBox(t, fake, sec, true)
	err := (anchor.App{}).Setup(context.Background(), box.h)
	if !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("expected Manual, got %v", err)
	}
	if fake.count("POST /api/auth/api-token/regenerate") != 0 || sec.durable["ANCHOR_HOMARR_API_KEY"] != "" || sec.durable["ANCHOR_HOMARR_TOKEN_PENDING"] != "" {
		t.Fatal("an existing token was replaced or recorded")
	}
	box.assertPrivate(t, err)
}

func TestRecordedTokenThatNoLongerMatchesTheAccountStopsManual(t *testing.T) {
	for name, current := range map[string]*string{"replaced": tokenValue(foreignToken), "revoked": nil} {
		t.Run(name, func(t *testing.T) {
			fake, sec := &tokenFake{token: current, regenerateCode: http.StatusOK}, tokenSecrets()
			sec.values["ANCHOR_HOMARR_API_KEY"] = issuedToken
			sec.values["ANCHOR_HOMARR_TOKEN_OWNERSHIP"] = ownershipFor(issuedToken)
			box := newTokenBox(t, fake, sec, true)
			err := (anchor.App{}).Setup(context.Background(), box.h)
			if !errors.Is(err, helpers.ErrManual) || fake.count("POST /api/auth/api-token/regenerate") != 0 {
				t.Fatalf("expected Manual without regeneration: %v", err)
			}
			if sec.values["ANCHOR_HOMARR_API_KEY"] != issuedToken || len(sec.writes) != 0 {
				t.Fatal("recorded credentials must be preserved")
			}
			box.assertPrivate(t, err)
		})
	}
}

func TestRecordedTokenForAnotherAccountOrMismatchedOwnershipStopsManual(t *testing.T) {
	other := strings.Replace(ownershipFor(issuedToken), "existing-admin", "someone-else", 1)
	for name, ownership := range map[string]string{
		"other account": other, "fingerprint": ownershipFor(foreignToken), "incomplete": `{"accountId":"existing-admin"}`, "missing": "",
	} {
		t.Run(name, func(t *testing.T) {
			fake, sec := &tokenFake{token: tokenValue(issuedToken), regenerateCode: http.StatusOK}, tokenSecrets()
			sec.values["ANCHOR_HOMARR_API_KEY"] = issuedToken
			sec.values["ANCHOR_HOMARR_TOKEN_OWNERSHIP"] = ownership
			box := newTokenBox(t, fake, sec, true)
			err := (anchor.App{}).Setup(context.Background(), box.h)
			if !errors.Is(err, helpers.ErrManual) || fake.count("POST /api/auth/api-token/regenerate") != 0 {
				t.Fatalf("expected Manual without regeneration: %v", err)
			}
			if verify := anchor.VerifyHomarr(context.Background(), box.h); !errors.Is(verify, helpers.ErrManual) {
				t.Fatalf("verification must be Manual: %v", verify)
			}
		})
	}
}

func TestUncertainIssuanceIsPreservedAndNeverRetriedBlindly(t *testing.T) {
	fake, sec := &tokenFake{regenerateCode: http.StatusInternalServerError}, tokenSecrets()
	box := newTokenBox(t, fake, sec, true)
	err := (anchor.App{}).Setup(context.Background(), box.h)
	if err == nil || sec.durable["ANCHOR_HOMARR_TOKEN_PENDING"] == "" || sec.durable["ANCHOR_HOMARR_API_KEY"] != "" {
		t.Fatalf("uncertain issuance must keep its intent only: %v", err)
	}
	box.assertPrivate(t, err)
	box = newTokenBox(t, fake, sec, true)
	err = (anchor.App{}).Setup(context.Background(), box.h)
	if !errors.Is(err, helpers.ErrManual) || fake.count("POST /api/auth/api-token/regenerate") != 1 {
		t.Fatalf("a pending intent must stop as Manual without another regeneration: %v", err)
	}
	if verify := anchor.VerifyHomarr(context.Background(), box.h); !errors.Is(verify, helpers.ErrManual) {
		t.Fatalf("a pending token must not verify: %v", verify)
	}
}

func TestCapturedIssuanceCompletesAfterInterruption(t *testing.T) {
	fake, sec := &tokenFake{token: tokenValue(issuedToken), regenerateCode: http.StatusOK}, tokenSecrets()
	sec.values["ANCHOR_HOMARR_TOKEN_PENDING"] = `{"version":1,"intent":{"accountId":"existing-admin","email":"admin@selfhost.local"},"issued":{"secret":"` + issuedToken + `","data":null}}`
	box := newTokenBox(t, fake, sec, true)
	if err := (anchor.App{}).Setup(context.Background(), box.h); err != nil {
		t.Fatal(err)
	}
	if sec.durable["ANCHOR_HOMARR_API_KEY"] != issuedToken || sec.durable["ANCHOR_HOMARR_TOKEN_PENDING"] != "" || fake.count("POST /api/auth/api-token/regenerate") != 0 {
		t.Fatal("captured evidence must be committed without regenerating")
	}
}

func TestRejectedSavedTokenFailsNoteVerification(t *testing.T) {
	fake, sec := &tokenFake{token: tokenValue(issuedToken), deniedToken: true}, tokenSecrets()
	sec.values["ANCHOR_HOMARR_API_KEY"] = issuedToken
	sec.values["ANCHOR_HOMARR_TOKEN_OWNERSHIP"] = ownershipFor(issuedToken)
	box := newTokenBox(t, fake, sec, true)
	if err := anchor.VerifyHomarr(context.Background(), box.h); err == nil {
		t.Fatal("a token Anchor rejects for notes must not verify")
	}
}
