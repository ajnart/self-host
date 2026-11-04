package setupspec_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type fakeApp struct {
	created  bool
	password string
	role     string
}

func (f *fakeApp) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /api/admin", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"hasAdmin": f.created})
	})
	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Password string }
		json.NewDecoder(r.Body).Decode(&body)
		f.password, f.created = body.Password, true
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Username, Password string }
		json.NewDecoder(r.Body).Decode(&body)
		if !f.created || body.Username != "admin" || body.Password != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{
			"role": f.role, "perms": []map[string]string{{"name": "read"}, {"name": "write"}},
		}})
	})
	return mux
}

type secrets map[string]string

func (s secrets) Get(key string) string       { return s[key] }
func (s secrets) Set(key, value string) error { s[key] = value; return nil }

func newH(t *testing.T, srv *httptest.Server) (*helpers.H, *[]helpers.StepResult) {
	t.Helper()
	deps := systemtest.NewDeps(t)
	var recorded []helpers.StepResult
	h := helpers.New(helpers.Config{
		App: "fake", Service: "fake", BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(),
		Secrets: secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "adminpass"},
		Log:     events.Discard(), Now: deps.Now, Sleep: deps.Sleep,
		Record: func(r helpers.StepResult) { recorded = append(recorded, r) },
	})
	return h, &recorded
}

func fakeSpec() setupspec.Spec {
	return setupspec.Spec{
		Kind:  setupspec.KindFirstAdmin,
		Name:  "Fake",
		Probe: setupspec.Probe{Path: "/ready"},
		Exists: []setupspec.Call{{
			Path: "/api/admin", Assert: []setupspec.Assert{{Path: "hasAdmin", Present: true}},
		}},
		Create: []setupspec.Call{{
			Path: "/api/setup", Ok: []int{201},
			JSON: map[string]any{"username": "{user}", "password": "{password}"},
		}},
		Login: []setupspec.Call{
			{
				Path: "/api/login", Capture: map[string]string{"token": "token"},
				JSON: map[string]any{"username": "{user}", "password": "{password}"},
			},
			{
				Path: "/api/me", Header: map[string]string{"Authorization": "Bearer {token}"},
				Require: []setupspec.Assert{{Path: "user", Type: "object"}},
				Assert: []setupspec.Assert{
					{Path: "user.role", Equals: "admin"},
					{Path: "user.perms", Some: []setupspec.Assert{{Path: "name", Equals: "write"}}},
				},
			},
		},
	}
}

func TestFirstAdminSetupThenCheckThenRerun(t *testing.T) {
	srv := httptest.NewServer((&fakeApp{role: "admin"}).handler())
	defer srv.Close()
	h, recorded := newH(t, srv)
	ctx := context.Background()
	spec := fakeSpec()

	if err := spec.Setup(ctx, h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := spec.Check(ctx, h); err != nil {
		t.Fatalf("Check: %v", err)
	}
	*recorded = nil
	if err := spec.Setup(ctx, h); err != nil {
		t.Fatalf("second Setup: %v", err)
	}
	for _, r := range *recorded {
		if r.Status != stepstatus.StepSkip {
			t.Errorf("step %s = %s on rerun, want skip", r.ID, r.Status)
		}
	}
}

func TestFirstAdminCheckNamesTheApp(t *testing.T) {
	for name, app := range map[string]*fakeApp{
		"wrong password": {created: true, password: "other", role: "admin"},
		"wrong role":     {created: true, password: "adminpass", role: "viewer"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(app.handler())
			defer srv.Close()
			h, _ := newH(t, srv)
			err := fakeSpec().Check(context.Background(), h)
			if err == nil || !strings.Contains(err.Error(), "cannot sign in to Fake") {
				t.Fatalf("Check = %v, want a refused login naming the App", err)
			}
		})
	}
}

func TestCallStatusSemantics(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	for name, tc := range map[string]struct {
		call    setupspec.Call
		wantErr string
	}{
		"unexpected status fails": {call: setupspec.Call{Path: "/nope"}, wantErr: "returned HTTP 404"},
		"lenient status is no":    {call: setupspec.Call{Path: "/nope", Lenient: true}},
		"denied status is no":     {call: setupspec.Call{Path: "/nope", Denied: []int{404}}},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newH(t, srv)
			spec := setupspec.Spec{Kind: setupspec.KindFirstAdmin, Login: []setupspec.Call{tc.call}}
			err := spec.Check(context.Background(), h)
			if tc.wantErr == "" {
				if err == nil || !strings.Contains(err.Error(), "cannot sign in") {
					t.Fatalf("Check = %v, want a refused login", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Check = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRequireFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer((&fakeApp{created: true, password: "adminpass", role: "admin"}).handler())
	defer srv.Close()
	h, _ := newH(t, srv)
	spec := fakeSpec()
	spec.Login[1].Require = []setupspec.Assert{{Path: "missing", Present: true}}
	err := spec.Check(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("Check = %v, want an unexpected-response error", err)
	}
}

func TestValidate(t *testing.T) {
	bad := map[string]setupspec.Spec{
		"call without path":  {Kind: setupspec.KindFirstAdmin, Login: []setupspec.Call{{}}},
		"two creators":       {Kind: setupspec.KindFirstAdmin, CreateError: "no", CreateWait: "1m", Login: []setupspec.Call{{Path: "/"}}},
		"wait without login": {Kind: setupspec.KindFirstAdmin, CreateWait: "1m"},
		"bad duration":       {Kind: setupspec.KindFirstAdmin, Probe: setupspec.Probe{Timeout: "soon"}},
		"unknown kind":       {Kind: "wizard"},
	}
	for name, spec := range bad {
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: Validate passed", name)
		}
	}
	if err := fakeSpec().Validate(); err != nil {
		t.Errorf("fake spec: %v", err)
	}
}
