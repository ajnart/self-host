package oxynote_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/oxynote"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type secrets map[string]string

func (s secrets) Get(key string) string       { return s[key] }
func (s secrets) Set(key, value string) error { s[key] = value; return nil }

// fake is the image's auth service: one admin and a trusted origin.
type fake struct {
	password, origin string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch r.Method + " " + r.URL.Path {
	case "GET /auth-realtime/api/auth-config":
		w.WriteHeader(http.StatusOK)
	case "POST /auth-realtime/api/auth/sign-in/email":
		if origin := r.Header.Get("Origin"); origin != "" && origin != f.origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if body["email"] != "admin@example.com" || body["password"] != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		origin, _ := url.Parse(f.origin)
		http.SetCookie(w, &http.Cookie{Name: "auth.session_token", Value: "fixture-session", Domain: origin.Hostname(), Path: "/", HttpOnly: true})
	case "GET /auth-realtime/api/auth/get-session":
		if c, err := r.Cookie("auth.session_token"); err != nil || c.Value != "fixture-session" || r.Header.Get("Origin") != f.origin {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	case "POST /auth-realtime/api/auth/change-password":
		if c, err := r.Cookie("auth.session_token"); err != nil || c.Value != "fixture-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Origin") != f.origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if body["currentPassword"] != f.password {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.password = body["newPassword"].(string)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newH(srv *httptest.Server, password string, records *[]helpers.StepResult) *helpers.H {
	return helpers.New(helpers.Config{App: "oxynote", BaseURL: srv.URL, URL: srv.URL, Port: 8085,
		Settings: map[string]string{"LAN_IP": "192.168.1.20"}, HTTP: srv.Client(), Log: events.Discard(),
		Secrets: secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": password},
		Record:  func(r helpers.StepResult) { *records = append(*records, r) }})
}

func TestSetupReplacesTheDefaultPasswordOnce(t *testing.T) {
	f := &fake{password: "oxynote-admin-1234", origin: "http://192.168.1.20:8085"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var records []helpers.StepResult
	h := newH(srv, "fixture-only-password-20", &records)

	if err := (oxynote.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup = %v", err)
	}
	if f.password != "fixture-only-password-20" {
		t.Fatalf("password = %q, want the Admin password", f.password)
	}
	records = nil
	if err := (oxynote.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("second Setup = %v", err)
	}
	for _, r := range records {
		if r.Status != stepstatus.StepSkip {
			t.Fatalf("second Setup records = %+v, want all skipped", records)
		}
	}
	if err := (oxynote.App{}).Check(context.Background(), h); err != nil {
		t.Fatalf("Check = %v", err)
	}
}

func TestSetupKeepsTheDefaultPasswordWhenTheAdminPasswordIsTooShort(t *testing.T) {
	f := &fake{password: "oxynote-admin-1234", origin: "http://192.168.1.20:8085"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var records []helpers.StepResult

	err := (oxynote.App{}).Setup(context.Background(), newH(srv, "short-password", &records))
	if err == nil || !strings.Contains(err.Error(), "16 to 128 characters") {
		t.Fatalf("Setup = %v, want the length rule", err)
	}
	if f.password != "oxynote-admin-1234" {
		t.Fatalf("password = %q, want the default kept", f.password)
	}
}

func TestComposePublicURLIsTheAddressPeopleOpen(t *testing.T) {
	cases := map[string]struct {
		settings map[string]string
		want     string
	}{
		"home network":  {map[string]string{"LAN_IP": "192.168.1.20"}, "http://${LAN_IP}:8085"},
		"this computer": {map[string]string{"LAN_IP": "192.168.1.20", "EXPOSURE": "local"}, "http://127.0.0.1:8085"},
		"easy names":    {map[string]string{"LAN_IP": "192.168.1.20", "EASY_NAMES": "on"}, "http://${LAN_IP}:8085"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			compose := (oxynote.App{}).Compose(app.RenderContext{Port: 8085, Settings: c.settings})
			if !strings.Contains(compose, "OXYNOTE_PUBLIC_URL: "+c.want+"\n") {
				t.Fatalf("compose lacks OXYNOTE_PUBLIC_URL %s:\n%s", c.want, compose)
			}
		})
	}
}

func TestHomeAddressAuthenticatesWithBrowserCookieAndOriginRules(t *testing.T) {
	for _, settings := range []map[string]string{
		{"LAN_IP": "192.168.1.20"},
		{"LAN_IP": "192.168.1.20", "EASY_NAMES": "on"},
		{"LAN_IP": "192.168.1.20", "EXPOSURE": "local"},
	} {
		t.Run(settings["EASY_NAMES"]+settings["EXPOSURE"], func(t *testing.T) {
			origin := "http://192.168.1.20:9085"
			if settings["EXPOSURE"] == "local" {
				origin = "http://127.0.0.1:9085"
			}
			compose := (oxynote.App{}).Compose(app.RenderContext{Port: 9085, Settings: settings})
			_, configured, ok := strings.Cut(compose, "OXYNOTE_PUBLIC_URL: ")
			if !ok {
				t.Fatal("missing browser origin")
			}
			configured, _, _ = strings.Cut(configured, "\n")
			configured = strings.ReplaceAll(configured, "${LAN_IP}", settings["LAN_IP"])
			f := &fake{password: "oxynote-admin-1234", origin: configured}
			srv := httptest.NewServer(f)
			t.Cleanup(srv.Close)
			var records []helpers.StepResult
			h := newH(srv, "fixture-only-password-20", &records)
			h.Settings, h.Port = settings, 9085
			if err := (oxynote.App{}).Setup(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			}}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Jar: jar, Transport: transport}
			for _, path := range []string{"/auth-realtime/api/auth/sign-in/email", "/auth-realtime/api/auth/get-session"} {
				method := http.MethodGet
				body := ""
				if strings.Contains(path, "sign-in") {
					method = http.MethodPost
					body = `{"email":"admin@example.com","password":"fixture-only-password-20"}`
				}
				req, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Origin", origin)
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s through advertised Address %s returned %d", path, origin, resp.StatusCode)
				}
			}
		})
	}
}
