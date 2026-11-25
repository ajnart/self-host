package printstash_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/printstash"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type secrets map[string]string

func (s secrets) Get(key string) string { return s[key] }
func (s secrets) Set(_, _ string) error { return errors.New("unexpected secret write") }

type fixture struct {
	setup       string
	setupStatus int
	login       string
	user        string
	health      string
	loginStatus int
	redirect    string
	requests    []string
}

func goodFixture() *fixture {
	return &fixture{
		setup:  `{"configured":true,"recovery_required":false,"storage_choice_required":false}`,
		login:  `{"access_token":"fixture-access","refresh_token":"fixture-refresh","token_type":"bearer"}`,
		user:   `{"id":7,"username":"owner","is_active":true,"is_superuser":true}`,
		health: `{"status":"ok","components":{"database":{"ok":true},"storage":{"ok":true}}}`,
	}
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	f.requests = append(f.requests, route)
	w.Header().Set("Content-Type", "application/json")
	switch route {
	case "GET /api/v1/setup/status":
		if f.setupStatus != 0 {
			w.WriteHeader(f.setupStatus)
		}
		w.Write([]byte(f.setup))
	case "POST /api/v1/auth/login":
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["username"] != "owner" || body["password"] != "fixture-password" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.redirect != "" {
			w.Header().Set("Location", f.redirect)
		}
		if f.loginStatus != 0 {
			w.WriteHeader(f.loginStatus)
		}
		w.Write([]byte(f.login))
	case "GET /api/v1/auth/me":
		if r.Header.Get("Authorization") != "Bearer fixture-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(f.user))
	case "GET /api/v1/health/details":
		if r.Header.Get("Authorization") != "Bearer fixture-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(f.health))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func harness(t *testing.T, f *fixture) (*helpers.H, *bytes.Buffer) {
	t.Helper()
	return harnessWithSecrets(t, f, secrets{"ADMIN_USER": "owner", "ADMIN_PASSWORD": "fixture-password"})
}

func harnessWithSecrets(t *testing.T, f *fixture, creds secrets) (*helpers.H, *bytes.Buffer) {
	t.Helper()
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	out := &bytes.Buffer{}
	return helpers.New(helpers.Config{App: "printstash", Service: "printstash", ContainerPort: 3000,
		BaseURL: s.URL, URL: s.URL, HTTP: s.Client(), Log: events.New(events.JSON(out)),
		Secrets: creds}), out
}

func TestDeployment(t *testing.T) {
	a := printstash.App{}
	i := a.Info()
	if i.ID != "printstash" || i.Service != "printstash" || i.Port != 3000 || i.ContainerPortFor(13000) != 3000 || i.Image != "ghcr.io/xiao-villamor/printstash:latest" || !i.HomeAddressOnly || len(a.Links()) != 0 {
		t.Fatalf("incorrect catalog metadata: %+v", i)
	}
	c := a.Compose(app.RenderContext{Port: 13000})
	for _, want := range []string{"image: " + i.Image, "${BIND_IP}:13000:3000", "./data:/data", "PUID: \"${PUID}\"", "PGID: \"${PGID}\"", "stop_grace_period: 60s", "VAULT_SETUP_MODE: trusted_network", "restart: unless-stopped"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"privileged:", "docker.sock", "container_name:", "ADMIN_PASSWORD", "homarr"} {
		if strings.Contains(c, bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
}

func TestSetupExistingAndIdempotent(t *testing.T) {
	f := goodFixture()
	h, out := harness(t, f)
	for i := 0; i < 2; i++ {
		if err := (printstash.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if err := (printstash.App{}).Check(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	logins := 0
	for _, route := range f.requests {
		if route == "POST /api/v1/auth/login" {
			logins++
		}
		if strings.HasPrefix(route, "POST ") && route != "POST /api/v1/auth/login" {
			t.Errorf("unexpected mutation: %s", route)
		}
	}
	if logins != 1 {
		t.Errorf("got %d logins, want one", logins)
	}
	for _, secret := range []string{"fixture-password", "fixture-access", "fixture-refresh"} {
		if strings.Contains(out.String(), secret) {
			t.Error("secret leaked to output")
		}
	}
}

func TestManualAndInvalidStates(t *testing.T) {
	cases := []struct {
		name      string
		change    func(*fixture)
		wantLogin bool
	}{
		{"fresh", func(f *fixture) { f.setup = `{"configured":false}` }, false},
		{"recovery", func(f *fixture) {
			f.setup = `{"configured":true,"recovery_required":true,"storage_choice_required":false}`
		}, false},
		{"storage", func(f *fixture) {
			f.setup = `{"configured":true,"recovery_required":false,"storage_choice_required":true}`
		}, false},
		{"setup-redirect", func(f *fixture) { f.setupStatus = http.StatusTemporaryRedirect }, false},
		{"missing-fields", func(f *fixture) { f.setup = `{"configured":true}` }, false},
		{"invalid-login", func(f *fixture) { f.loginStatus = 401 }, true},
		{"bad-token", func(f *fixture) { f.login = `{"token_type":"bearer"}` }, true},
		{"not-admin", func(f *fixture) { f.user = `{"id":7,"username":"owner","is_active":true,"is_superuser":false}` }, true},
		{"inactive", func(f *fixture) { f.user = `{"id":7,"username":"owner","is_active":false,"is_superuser":true}` }, true},
		{"wrong-user", func(f *fixture) { f.user = `{"id":7,"username":"other","is_active":true,"is_superuser":true}` }, true},
		{"degraded", func(f *fixture) {
			f.health = `{"status":"degraded","components":{"database":{"ok":true},"storage":{"ok":false}}}`
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := goodFixture()
			tc.change(f)
			h, _ := harness(t, f)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := (printstash.App{}).Setup(ctx, h)
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("expected Manual, got %v", err)
			}
			logins := 0
			for _, route := range f.requests {
				switch {
				case route == "POST /api/v1/auth/login":
					logins++
				case strings.HasPrefix(route, "POST "):
					t.Errorf("unexpected mutation: %s", route)
				}
			}
			wantLogins := 0
			if tc.wantLogin {
				wantLogins = 1
			}
			if logins != wantLogins {
				t.Errorf("got %d login requests, want %d", logins, wantLogins)
			}
		})
	}
}

func TestLoginRedirectDoesNotForwardCredentials(t *testing.T) {
	var hits atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer sink.Close()
	f := goodFixture()
	f.loginStatus = http.StatusTemporaryRedirect
	f.redirect = sink.URL
	h, _ := harness(t, f)
	if err := (printstash.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("got %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("redirect received %d requests", n)
	}
}

func TestMissingAdminLoginDoesNotAuthenticate(t *testing.T) {
	f := goodFixture()
	h, _ := harnessWithSecrets(t, f, secrets{"ADMIN_USER": "owner"})
	if err := (printstash.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("expected Manual, got %v", err)
	}
	for _, route := range f.requests {
		if strings.HasPrefix(route, "POST ") {
			t.Errorf("unexpected mutation: %s", route)
		}
	}
}

func TestCheckLeavesStartupErrorsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		setup, login int
	}{
		{name: "setup status", setup: http.StatusServiceUnavailable},
		{name: "login", login: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := goodFixture()
			f.setupStatus, f.loginStatus = tc.setup, tc.login
			h, out := harness(t, f)
			if err := (printstash.App{}).Check(t.Context(), h); err == nil || errors.Is(err, helpers.ErrManual) {
				t.Fatalf("expected a retryable check error, got %v", err)
			}
			f.setupStatus, f.loginStatus = 0, 0
			if err := (printstash.App{}).Check(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "fixture-password") {
				t.Fatal("secret leaked to output")
			}
		})
	}
}

func TestDataDirectoryUsesProjectIdentity(t *testing.T) {
	a, ok := any(printstash.App{}).(app.DirectoryDeclarer)
	if !ok {
		t.Fatal("App does not declare its data directory")
	}
	settings := map[string]string{"PUID": "1234", "PGID": "2345"}
	want := []app.Directory{{Path: "data", Mode: 0o700, UID: "1234", GID: "2345"}}
	if got := a.Directories(app.RenderContext{Settings: settings}); !slices.Equal(got, want) {
		t.Fatalf("data directories: got %+v, want %+v", got, want)
	}
}

func TestPreflightRequiresPositiveProjectIdentity(t *testing.T) {
	a, ok := any(printstash.App{}).(app.PreflightChecker)
	if !ok {
		t.Fatal("App does not validate its runtime identity")
	}
	for _, tc := range []struct {
		uid, gid string
		ok       bool
	}{
		{"1234", "2345", true},
		{"0", "2345", false},
		{"1234", "0", false},
		{"1234", "", false},
		{"", "2345", false},
		{"+1", "2345", false},
		{"1234", "4294967295", false},
	} {
		t.Run(tc.uid+"/"+tc.gid, func(t *testing.T) {
			h := helpers.New(helpers.Config{Settings: map[string]string{"PUID": tc.uid, "PGID": tc.gid}})
			err := a.Preflight(t.Context(), h)
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unexpected preflight result: %v", err)
			}
		})
	}
}
