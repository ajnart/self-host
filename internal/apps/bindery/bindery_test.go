package bindery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/bindery"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

const (
	password = "controlled-admin-password"
	apiKey   = "controlled-existing-instance-key"
	token    = "controlled-session-cookie"
)

type secrets struct {
	values map[string]string
	writes int
}

func (s *secrets) Get(key string) string { return s.values[key] }
func (s *secrets) Set(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

type fakeBindery struct {
	mu              sync.Mutex
	publicStatus    string
	sessionStatus   string
	loginBody       string
	loginStatus     int
	withCookie      bool
	releaseBody     string
	rootsBody       string
	unsignedStatus  int
	logins          int
	forbidden       int
	apiStatus       int
	rootReads       int
	credentialLeaks int
}

func newFake() *fakeBindery {
	return &fakeBindery{
		publicStatus:   `{"authenticated":false,"setupRequired":false,"mode":"enabled","localAuthEnabled":true}`,
		sessionStatus:  `{"authenticated":true,"setupRequired":false,"mode":"enabled","localAuthEnabled":true,"username":"admin","role":"admin"}`,
		loginBody:      `{"ok":true,"username":"admin"}`,
		loginStatus:    http.StatusOK,
		withCookie:     true,
		releaseBody:    `{"version":"v1.46.1","commit":"ca24961c4c04f58a1cd96c2b8382b87f7eda215f"}`,
		rootsBody:      `[]`,
		unsignedStatus: http.StatusUnauthorized,
		apiStatus:      http.StatusOK,
	}
}

func (f *fakeBindery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.RawQuery != "" || strings.Contains(r.URL.String(), apiKey) {
		f.credentialLeaks++
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
		w.Write([]byte(`{"status":"ok","version":"v1.46.1"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/status":
		if r.Header.Get("X-Api-Key") != "" {
			f.credentialLeaks++
		}
		cookie, err := r.Cookie("bindery_session")
		if err == nil && cookie.Value == token {
			w.Write([]byte(f.sessionStatus))
			return
		}
		w.Write([]byte(f.publicStatus))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
		f.logins++
		if r.Header.Get("X-Api-Key") != "" {
			f.credentialLeaks++
		}
		var input struct{ Username, Password string }
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Username != "admin" || input.Password != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.withCookie && f.loginStatus == http.StatusOK {
			http.SetCookie(w, &http.Cookie{Name: "bindery_session", Value: token, Path: "/", HttpOnly: true})
		}
		w.WriteHeader(f.loginStatus)
		w.Write([]byte(f.loginBody))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/status":
		if r.Header.Get("Cookie") != "" {
			f.credentialLeaks++
		}
		key := r.Header.Get("X-Api-Key")
		if key == "" {
			w.WriteHeader(f.unsignedStatus)
			return
		}
		if key != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(f.apiStatus)
		w.Write([]byte(f.releaseBody))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/rootfolder":
		if r.Header.Get("X-Api-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.rootReads++
		w.Write([]byte(f.rootsBody))
	default:
		f.forbidden++
		w.WriteHeader(http.StatusNotFound)
	}
}

func fixture(t *testing.T, f *fakeBindery) (*helpers.H, *secrets, *[]helpers.StepResult) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	store := &secrets{values: map[string]string{"ADMIN_USER": "admin", "ADMIN_PASSWORD": password, "BINDERY_API_KEY": apiKey}}
	var records []helpers.StepResult
	log := events.Discard()
	log.Secret(password)
	log.Secret(apiKey)
	h := helpers.New(helpers.Config{
		App: "bindery", Service: "bindery", BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(),
		Secrets: store, Log: log, Now: deps.Now, Sleep: deps.Sleep,
		Record: func(record helpers.StepResult) { records = append(records, record) },
	})
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.forbidden != 0 || f.credentialLeaks != 0 || store.writes != 0 {
			t.Error("verification issued an unapproved request, exposed credentials or changed the private store")
		}
	})
	return h, store, &records
}

func TestDeploymentContract(t *testing.T) {
	a := bindery.App{}
	info := a.Info()
	if info.ID != "bindery" || info.Image != "ghcr.io/vavallee/bindery:latest" || info.Port != 8787 || info.ContainerPort != 8787 || info.ContainerPortFor(19000) != 8787 || info.Service != "bindery" || !info.UsesMedia {
		t.Fatal("App metadata does not describe the official Bindery image and fixed container port")
	}
	if len(a.Links()) != 0 {
		t.Fatal("App unexpectedly wires a downloader or indexer")
	}
	if _, generates := any(a).(app.SecretDeclarer); generates {
		t.Fatal("App must not generate a replacement instance key while issuance is deferred")
	}
	inputs := a.Inputs()
	if len(inputs) != 1 || inputs[0].Key != "api_key" || !inputs[0].Private || !inputs[0].RecoveryOnly || inputs[0].Required {
		t.Fatal("existing API key must use the optional private recovery boundary")
	}
	files := a.Files(app.RenderContext{}, func(string) string {
		t.Fatal("configuration directory preparation must not obtain credentials")
		return ""
	})
	if len(files) != 1 || files[0].Path != "config/.keep" || !files[0].Keep || files[0].Mode != 0o600 || files[0].Content != "" {
		t.Fatal("configuration preparation must preserve existing App files")
	}
	for _, layout := range []string{"shared", "legacy", ""} {
		t.Run("layout="+layout, func(t *testing.T) {
			compose := a.Compose(app.RenderContext{Port: 19000, Settings: map[string]string{"MEDIA_LAYOUT": layout, "MEDIA_SEPARATE_USERS": "on"}})
			for _, expected := range []string{
				"user: ${PUID}:${PGID}", "${BIND_IP}:19000:8787", "./config:/config",
				`BINDERY_TELEMETRY_DISABLED: "true"`, `BINDERY_DOWNLOAD_RETENTION_DAYS: "0"`,
				`test: ["CMD", "/bindery", "healthcheck"]`, "restart: unless-stopped",
			} {
				if !strings.Contains(compose, expected) {
					t.Fatalf("Compose omitted %s", expected)
				}
			}
			for _, forbidden := range []string{"readarr", "s6-", "CMD-SHELL", "curl", "/ping", "BINDERY_API_KEY", "UMASK", "BINDERY_PUID", "depends_on", "container_name", "privileged", "docker.sock"} {
				if strings.Contains(compose, forbidden) {
					t.Fatalf("Compose used an unsupported or unsafe assumption: %s", forbidden)
				}
			}
			if layout == "shared" {
				for _, expected := range []string{"${DATA_ROOT}:/data", "BINDERY_LIBRARY_DIR: /data/media/books", "BINDERY_DOWNLOAD_DIR: /data/torrents"} {
					if !strings.Contains(compose, expected) {
						t.Fatal("shared Compose lost its common persistent parent or coordinated paths")
					}
				}
				if strings.Contains(compose, "${MEDIA_ROOT}") || strings.Contains(compose, "${DOWNLOADS_ROOT}") {
					t.Fatal("shared Compose split the importer mount")
				}
				return
			}
			for _, expected := range []string{"${MEDIA_ROOT}:/media", "${DOWNLOADS_ROOT}:/downloads", "BINDERY_LIBRARY_DIR: /media/books", "BINDERY_DOWNLOAD_DIR: /downloads"} {
				if !strings.Contains(compose, expected) {
					t.Fatal("legacy Compose changed persistent media/download paths")
				}
			}
		})
	}
}

func TestSetupCheckAndRerunPreserveExistingConfiguration(t *testing.T) {
	f := newFake()
	f.rootsBody = `[{"id":17,"path":"/data/media/my-books"},{"id":91,"path":"/data/media/paired-library"}]`
	h, store, records := fixture(t, f)
	before := map[string]string{}
	for key, value := range store.values {
		before[key] = value
	}
	a := bindery.App{}
	for range 2 {
		if err := a.Setup(t.Context(), h); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if err := a.Check(t.Context(), h); err != nil {
			t.Fatalf("Check: %v", err)
		}
	}
	for _, record := range *records {
		if record.Status != stepstatus.StepSkip {
			t.Fatalf("read-only verification step %s was not already done", record.ID)
		}
	}
	if !reflect.DeepEqual(before, store.values) {
		t.Fatal("rerun changed saved credentials")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rootReads != 4 || f.rootsBody != `[{"id":17,"path":"/data/media/my-books"},{"id":91,"path":"/data/media/paired-library"}]` {
		t.Fatal("verification did not preserve and reread existing library root identities")
	}
}

func TestManualPrerequisitesDoNotBootstrapOrReplaceCredentials(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeBindery, *secrets)
		hint   string
	}{
		{"first administrator", func(f *fakeBindery, _ *secrets) {
			f.publicStatus = `{"authenticated":false,"setupRequired":true,"mode":"enabled","localAuthEnabled":true}`
		}, "first-administrator"},
		{"disabled authentication", func(f *fakeBindery, _ *secrets) {
			f.publicStatus = `{"authenticated":true,"setupRequired":false,"mode":"disabled","localAuthEnabled":true}`
		}, "authentication"},
		{"SSO only", func(f *fakeBindery, _ *secrets) {
			f.publicStatus = `{"authenticated":false,"setupRequired":false,"mode":"enabled","localAuthEnabled":false}`
		}, "SSO"},
		{"wrong password", func(_ *fakeBindery, s *secrets) { s.values["ADMIN_PASSWORD"] = "different-private-password" }, "existing administrator"},
		{"rate limited", func(f *fakeBindery, _ *secrets) { f.loginStatus = http.StatusTooManyRequests }, "rate-limited"},
		{"missing key", func(_ *fakeBindery, s *secrets) { delete(s.values, "BINDERY_API_KEY") }, "BINDERY_API_KEY"},
		{"rejected key", func(_ *fakeBindery, s *secrets) { s.values["BINDERY_API_KEY"] = "rejected-private-key" }, "BINDERY_API_KEY"},
		{"not administrator", func(f *fakeBindery, _ *secrets) {
			f.sessionStatus = `{"authenticated":true,"setupRequired":false,"mode":"enabled","localAuthEnabled":true,"username":"admin","role":"user"}`
		}, "existing administrator"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFake()
			h, store, records := fixture(t, f)
			test.change(f, store)
			err := (bindery.App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) {
				t.Fatalf("Setup did not retain a Manual prerequisite: %v", err)
			}
			last := (*records)[len(*records)-1]
			if last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, test.hint) || !strings.Contains(last.Hint, "self-host setup bindery") || last.Error != "" {
				t.Fatal("Manual result lost actionable private recovery")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.logins > 1 {
				t.Fatal("Manual recovery retried a failed login inside the same Setup invocation")
			}
		})
	}
}

func TestInvalidResponsesAreNotUsefulAcceptance(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeBindery)
	}{
		{"missing authentication flags", func(f *fakeBindery) { f.publicStatus = `{}` }},
		{"null authentication flag", func(f *fakeBindery) {
			f.publicStatus = `{"authenticated":null,"setupRequired":false,"mode":"enabled","localAuthEnabled":true}`
		}},
		{"unsigned identity", func(f *fakeBindery) {
			f.publicStatus = `{"authenticated":true,"setupRequired":false,"mode":"enabled","localAuthEnabled":true}`
		}},
		{"missing cookie", func(f *fakeBindery) { f.withCookie = false }},
		{"missing login confirmation", func(f *fakeBindery) { f.loginBody = `{}` }},
		{"login body error", func(f *fakeBindery) { f.loginBody = `{"error":"` + apiKey + `"}` }},
		{"redirected login", func(f *fakeBindery) { f.loginStatus = http.StatusFound }},
		{"wrong identity", func(f *fakeBindery) {
			f.sessionStatus = `{"authenticated":true,"setupRequired":false,"mode":"enabled","localAuthEnabled":true,"username":"somebody-else","role":"admin"}`
		}},
		{"unprotected status", func(f *fakeBindery) { f.unsignedStatus = http.StatusOK }},
		{"missing version", func(f *fakeBindery) { f.releaseBody = `{"commit":"ca24961"}` }},
		{"null version", func(f *fakeBindery) { f.releaseBody = `{"version":null,"commit":"ca24961"}` }},
		{"invalid version", func(f *fakeBindery) { f.releaseBody = `{"version":"not-a-release","commit":"ca24961"}` }},
		{"missing commit", func(f *fakeBindery) { f.releaseBody = `{"version":"1.46.1"}` }},
		{"status error", func(f *fakeBindery) {
			f.apiStatus = http.StatusInternalServerError
			f.releaseBody = `{"error":"` + apiKey + `"}`
		}},
		{"null roots", func(f *fakeBindery) { f.rootsBody = `null` }},
		{"missing roots", func(f *fakeBindery) { f.rootsBody = `{}` }},
		{"null root", func(f *fakeBindery) { f.rootsBody = `[null]` }},
		{"invalid ID", func(f *fakeBindery) { f.rootsBody = `[{"id":1.5,"path":"/books"}]` }},
		{"missing ID", func(f *fakeBindery) { f.rootsBody = `[{"path":"/books"}]` }},
		{"duplicate ID", func(f *fakeBindery) { f.rootsBody = `[{"id":7,"path":"/books"},{"id":7,"path":"/other"}]` }},
		{"relative root", func(f *fakeBindery) { f.rootsBody = `[{"id":7,"path":"books"}]` }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFake()
			h, _, records := fixture(t, f)
			test.change(f)
			setupErr := (bindery.App{}).Setup(t.Context(), h)
			if setupErr == nil || (bindery.App{}).Check(t.Context(), h) == nil {
				t.Fatal("malformed, missing, unauthenticated or wrong-identity data was accepted")
			}
			if strings.Contains(fmt.Sprint(setupErr, *records), apiKey) || strings.Contains(fmt.Sprint(setupErr, *records), password) || strings.Contains(fmt.Sprint(setupErr, *records), token) {
				t.Fatal("diagnostics exposed private response or credential data")
			}
		})
	}
}

func TestEmptyLibraryAndExplicitPrivateKeyRecovery(t *testing.T) {
	f := newFake()
	h, store, _ := fixture(t, f)
	store.values["BINDERY_API_KEY"] = "old-private-key"
	if err := (bindery.App{}).Check(context.Background(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("rejected stored key was not a Manual boundary")
	}
	if store.values["BINDERY_API_KEY"] != "old-private-key" {
		t.Fatal("verification replaced a rejected key")
	}
	store.values["BINDERY_API_KEY"] = apiKey
	if err := (bindery.App{}).Check(context.Background(), h); err != nil {
		t.Fatalf("explicit private recovery and empty root list: %v", err)
	}
}

func TestNewerReleaseDoesNotNeedAnAllowlist(t *testing.T) {
	f := newFake()
	f.releaseBody = `{"version":"1.99.0","commit":"ca24961c4c04f58a1cd96c2b8382b87f7eda215f"}`
	h, _, _ := fixture(t, f)
	if err := (bindery.App{}).Check(t.Context(), h); err != nil {
		t.Fatalf("newer well-formed release: %v", err)
	}
}
