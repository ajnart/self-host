package tubearchivist_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ajnart/self-host/internal/apps/tubearchivist"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/systemtest"
)

const synthetic = "synthetic-token-0123456789abcdef"

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type tokenFake struct {
	valid      tubeArchivistFake
	issued     string
	tokenReads int
	tokenCalls int
	tokenAcct  string
	tokenPing  string
	deletes    int
	headers    []string
}

func (f *tokenFake) handler(t *testing.T) http.Handler {
	base := f.valid.handler(t)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.deletes++
		}
		if r.URL.Path == "/api/appsettings/token/" {
			f.tokenCalls++
			if _, err := r.Cookie("sessionid"); err != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte(`{"token":"` + f.issued + `"}`))
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			f.headers = append(f.headers, auth)
			f.tokenReads++
			if auth != "Token "+synthetic {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/api/user/account/":
				w.Write([]byte(f.tokenAcct))
			case "/api/ping/":
				w.Write([]byte(f.tokenPing))
			default:
				t.Errorf("unapproved token request %s", r.URL.Path)
			}
			return
		}
		base.ServeHTTP(w, r)
	})
}

func newTokenHarness(t *testing.T, fake *tokenFake, secrets tubeArchivistSecrets, peer bool) *helpers.H {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	config := helpers.Config{
		App: "tubearchivist", Service: "tubearchivist", BaseURL: srv.URL, URL: srv.URL,
		HTTP: srv.Client(), Log: events.Discard(), Secrets: secrets, Now: deps.Now, Sleep: deps.Sleep,
	}
	if peer {
		config.Peer = func(id string) *helpers.H {
			if id == "homarr" {
				return &helpers.H{}
			}
			return nil
		}
	}
	return helpers.New(config)
}

func newTokenFake() *tokenFake {
	return &tokenFake{
		valid:     *tubeArchivistValidFake(),
		issued:    synthetic,
		tokenAcct: `{"id":42,"name":"admin","is_superuser":true,"is_staff":true}`,
		tokenPing: `{"response":"pong","user":42,"version":"v0.6.0"}`,
	}
}

func adminSecrets() tubeArchivistSecrets {
	return tubeArchivistSecrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "synthetic-password"}
}

func ownedSecrets() tubeArchivistSecrets {
	secrets := adminSecrets()
	secrets["TUBEARCHIVIST_HOMARR_API_KEY"] = synthetic
	secrets["TUBEARCHIVIST_HOMARR_API_KEY_OWNER"] = "42"
	secrets["TUBEARCHIVIST_HOMARR_API_KEY_HASH"] = tokenHash(synthetic)
	return secrets
}

func TestTubeArchivistWithoutHomarrIssuesNoToken(t *testing.T) {
	fake := newTokenFake()
	h := newTokenHarness(t, fake, adminSecrets(), false)
	if err := (tubearchivist.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if fake.tokenCalls != 0 || h.Secret("TUBEARCHIVIST_HOMARR_API_KEY") != "" {
		t.Fatal("token was prepared without Homarr")
	}
}

func TestTubeArchivistIssuesOneOwnedTokenAndReusesIt(t *testing.T) {
	fake := newTokenFake()
	secrets := adminSecrets()
	h := newTokenHarness(t, fake, secrets, true)
	for range 3 {
		if err := (tubearchivist.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if fake.tokenCalls != 1 || fake.deletes != 0 {
		t.Fatalf("token calls = %d deletes = %d", fake.tokenCalls, fake.deletes)
	}
	if secrets["TUBEARCHIVIST_HOMARR_API_KEY"] != synthetic || secrets["TUBEARCHIVIST_HOMARR_API_KEY_OWNER"] != "42" || secrets["TUBEARCHIVIST_HOMARR_API_KEY_HASH"] != tokenHash(synthetic) {
		t.Fatal("token, owner or hash was not saved")
	}
	for _, header := range fake.headers {
		if header != "Token "+synthetic {
			t.Fatalf("token header = %q", header)
		}
	}
	if err := tubearchivist.VerifyHomarr(t.Context(), h); err != nil {
		t.Fatal(err)
	}
}

func TestTubeArchivistExistingOwnedTokenIsReusedWithoutIssuing(t *testing.T) {
	fake := newTokenFake()
	secrets := ownedSecrets()
	before := maps.Clone(secrets)
	h := newTokenHarness(t, fake, secrets, false)
	if err := (tubearchivist.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if fake.tokenCalls != 0 || !reflect.DeepEqual(secrets, before) {
		t.Fatal("owned token was reissued or changed")
	}
}

func TestTubeArchivistLostRejectedOrMismatchedTokenStaysManual(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret func(tubeArchivistSecrets)
		fake   func(*tokenFake)
	}{
		{"lost-after-attempt", func(s tubeArchivistSecrets) {
			delete(s, "TUBEARCHIVIST_HOMARR_API_KEY")
		}, nil},
		{"pending-owner-only", func(s tubeArchivistSecrets) {
			delete(s, "TUBEARCHIVIST_HOMARR_API_KEY")
			delete(s, "TUBEARCHIVIST_HOMARR_API_KEY_HASH")
		}, nil},
		{"rejected", func(s tubeArchivistSecrets) {
			s["TUBEARCHIVIST_HOMARR_API_KEY"] = "rejected-token-0123456789"
			s["TUBEARCHIVIST_HOMARR_API_KEY_HASH"] = tokenHash("rejected-token-0123456789")
		}, nil},
		{"owner-mismatch", func(s tubeArchivistSecrets) { s["TUBEARCHIVIST_HOMARR_API_KEY_OWNER"] = "7" }, nil},
		{"hash-mismatch", func(s tubeArchivistSecrets) { s["TUBEARCHIVIST_HOMARR_API_KEY_HASH"] = "bad" }, nil},
		{"malformed-owner", func(s tubeArchivistSecrets) { s["TUBEARCHIVIST_HOMARR_API_KEY_OWNER"] = "admin" }, nil},
		{"token-resolves-elsewhere", nil, func(f *tokenFake) {
			f.tokenAcct = `{"id":7}`
			f.tokenPing = `{"response":"pong","user":7,"version":"v0.6.0"}`
		}},
		{"ping-mismatch", nil, func(f *tokenFake) { f.tokenPing = `{"response":"pong","user":9,"version":"v0.6.0"}` }},
		{"old-version", nil, func(f *tokenFake) { f.tokenPing = `{"response":"pong","user":42,"version":"v0.5.0"}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newTokenFake()
			secrets := ownedSecrets()
			if tc.secret != nil {
				tc.secret(secrets)
			}
			if tc.fake != nil {
				tc.fake(fake)
			}
			before := maps.Clone(secrets)
			h := newTokenHarness(t, fake, secrets, false)
			err := (tubearchivist.App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("Setup = %v, want Manual", err)
			}
			if fake.tokenCalls != 0 || fake.deletes != 0 || !reflect.DeepEqual(secrets, before) {
				t.Fatal("recovery rotated or changed the token")
			}
			if err := tubearchivist.VerifyHomarr(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("VerifyHomarr = %v, want Manual", err)
			}
		})
	}
}

func TestTubeArchivistUnconfirmedTokenCreationIsNotRepeated(t *testing.T) {
	fake := newTokenFake()
	fake.issued = "short"
	secrets := adminSecrets()
	h := newTokenHarness(t, fake, secrets, true)
	if err := (tubearchivist.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("first Setup = %v", err)
	}
	if err := (tubearchivist.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("second Setup = %v", err)
	}
	if fake.tokenCalls != 1 || secrets["TUBEARCHIVIST_HOMARR_API_KEY"] != "" {
		t.Fatalf("token creation repeated or an uncertain token was adopted: %d", fake.tokenCalls)
	}
}
