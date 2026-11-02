package sportarr_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/sportarr"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"go.yaml.in/yaml/v3"
)

type sportarrFake struct {
	mu             sync.Mutex
	key            string
	method         string
	required       string
	user           string
	password       string
	statusBody     string
	settingsBody   string
	authBody       string
	loginBody      string
	loginStatus    int
	sessionUser    string
	settingsKey    string
	acceptInvalid  bool
	acceptAnon     bool
	missingCookie  bool
	insecureCookie bool
	logoutStatus   int
	sessions       int
	logins         int
	logouts        int
	paths          []string
	preserved      map[string]any
}

func sportarrServer(t *testing.T) (*sportarrFake, *httptest.Server, *helpers.H, *porttest.Recorder, porttest.Secrets) {
	t.Helper()
	f := &sportarrFake{
		key: "sportarr-synthetic-key", method: "forms", required: "enabled",
		user: "admin", password: "sportarr-synthetic-password",
		preserved: map[string]any{
			"library":   []string{"/data/media/chosen-sports"},
			"profiles":  []string{"Personal 4K", "Archive"},
			"schedules": []string{"person-owned-recording"},
			"settings":  map[string]any{"rssSyncInterval": 95, "useHardlinks": false, "custom": "unchanged"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(srv.Close)
	rec := &porttest.Recorder{}
	secrets := porttest.Secrets{"ADMIN_USER": f.user, "ADMIN_PASSWORD": f.password}
	h := porttest.New(t, porttest.Options{App: "sportarr", Service: "sportarr", Dir: t.TempDir(), Secrets: secrets}, srv, rec)
	return f, srv, h, rec, secrets
}

func (f *sportarrFake) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	cookie, _ := r.Cookie("SportarrAuth")
	session := cookie != nil && cookie.Value == "sportarr-synthetic-session" && f.sessions > 0
	switch r.Method + " " + r.URL.Path {
	case "GET /ping":
		w.Write([]byte(`{"status":"ok"}`))
	case "GET /api/system/status":
		key := r.Header.Get("X-Api-Key")
		if !session && key != f.key && !(key == "" && f.acceptAnon) && !(key != "" && f.acceptInvalid) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.statusBody != "" {
			w.Write([]byte(f.statusBody))
			return
		}
		w.Write([]byte(`{"appName":"Sportarr","version":"4.1.11.1121","authentication":"apikey"}`))
	case "POST /api/login":
		f.logins++
		var body struct {
			Username   string `json:"username"`
			Password   string `json:"password"`
			RememberMe bool   `json:"rememberMe"`
		}
		if r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil || body.RememberMe {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.loginStatus != 0 {
			w.WriteHeader(f.loginStatus)
			return
		}
		if body.Username != f.user || body.Password != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !f.missingCookie {
			f.sessions++
			http.SetCookie(w, &http.Cookie{Name: "SportarrAuth", Value: "sportarr-synthetic-session", Path: "/", HttpOnly: !f.insecureCookie})
		}
		if f.loginBody != "" {
			w.Write([]byte(f.loginBody))
			return
		}
		w.Write([]byte(`{"success":true,"message":"Login successful"}`))
	case "GET /api/auth/check":
		if !session {
			w.Write([]byte(`{"authenticated":false}`))
			return
		}
		if f.authBody != "" {
			w.Write([]byte(f.authBody))
			return
		}
		user := f.user
		if f.sessionUser != "" {
			user = f.sessionUser
		}
		json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "username": user})
	case "GET /api/settings":
		if !session {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.settingsBody != "" {
			w.Write([]byte(f.settingsBody))
			return
		}
		key := f.key
		if f.settingsKey != "" {
			key = f.settingsKey
		}
		security, _ := json.Marshal(map[string]any{
			"authenticationMethod": f.method, "authenticationRequired": f.required,
			"username": f.user, "apiKey": key, "certificateValidation": "enabled",
		})
		json.NewEncoder(w).Encode(map[string]any{"securitySettings": string(security), "untouched": f.preserved})
	case "POST /api/logout":
		f.logouts++
		if !session {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.sessions--
		if f.logoutStatus != 0 {
			w.WriteHeader(f.logoutStatus)
			return
		}
		w.Write([]byte(`{"message":"Logged out successfully"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestSportarrReusesVerifiedExistingLoginAndDiscoversItsCurrentKey(t *testing.T) {
	f, _, h, rec, secrets := sportarrServer(t)
	before, _ := json.Marshal(f.preserved)
	if err := (sportarr.App{}).Setup(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if secrets["SPORTARR_API_KEY"] != f.key || rec.Status("credentials") != stepstatus.StepOK {
		t.Fatal("Setup did not save and verify the existing key")
	}
	for range 2 {
		rerun := &porttest.Recorder{}
		h.Record = rerun.Record
		if err := (sportarr.App{}).Setup(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		if rerun.Status("credentials") != stepstatus.StepSkip {
			t.Fatal("rerun did not reuse accepted credentials")
		}
		if err := (sportarr.App{}).Check(context.Background(), h); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := json.Marshal(f.preserved)
	if string(before) != string(after) || f.sessions != 0 || f.logins != f.logouts {
		t.Fatal("Setup changed personal resources or retained a login session")
	}
	for _, path := range f.paths {
		if !strings.HasPrefix(path, "GET ") && path != "POST /api/login" && path != "POST /api/logout" {
			t.Fatalf("unexpected mutation %s", path)
		}
		if strings.Contains(path, "/api/v3/") || strings.Contains(path, "initialize") || strings.Contains(path, "stats") {
			t.Fatalf("unexpected discovery or compatibility path %s", path)
		}
	}
}

func TestSportarrRejectedSavedKeyIsNotReplaced(t *testing.T) {
	f, _, h, rec, secrets := sportarrServer(t)
	secrets["SPORTARR_API_KEY"] = "rejected-synthetic-key"
	err := (sportarr.App{}).Setup(context.Background(), h)
	if !errors.Is(err, helpers.ErrManual) || rec.Status("credentials") != stepstatus.StepManual || secrets["SPORTARR_API_KEY"] != "rejected-synthetic-key" || f.logins != 0 {
		t.Fatalf("rejected saved key was not a non-mutating Manual boundary: %v", err)
	}
	if !strings.Contains(rec.Hint(), "--set-file sportarr.api_key=") || !strings.Contains(rec.Hint(), "self-host setup sportarr") {
		t.Fatal("missing actionable private-key recovery")
	}
}

func TestSportarrRequiresRealNativeIdentityAndAnEnforcedKeyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sportarrFake)
	}{
		{"Sonarr shim identity", func(f *sportarrFake) {
			f.statusBody = `{"appName":"Sonarr","instanceName":"Sportarr","version":"4.1.11.1121","sportarrVersion":"4.1.11","authentication":"apikey"}`
		}},
		{"missing identity", func(f *sportarrFake) { f.statusBody = `{}` }},
		{"malformed version", func(f *sportarrFake) {
			f.statusBody = `{"appName":"Sportarr","version":"4.1.x","authentication":"apikey"}`
		}},
		{"unsupported version", func(f *sportarrFake) {
			f.statusBody = `{"appName":"Sportarr","version":"5.0.0","authentication":"apikey"}`
		}},
		{"missing authentication", func(f *sportarrFake) { f.statusBody = `{"appName":"Sportarr","version":"4.1.11.1121"}` }},
		{"HTML status", func(f *sportarrFake) { f.statusBody = `<html>sportarr-synthetic-key</html>` }},
		{"accepts anonymous", func(f *sportarrFake) { f.acceptAnon = true }},
		{"accepts invalid key", func(f *sportarrFake) { f.acceptInvalid = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, h, _, secrets := sportarrServer(t)
			var output bytes.Buffer
			h.Log.AddSink(events.JSON(&output))
			secrets["SPORTARR_API_KEY"] = f.key
			tc.change(f)
			if err := (sportarr.App{}).Check(context.Background(), h); err == nil || strings.Contains(err.Error(), f.key) {
				t.Fatalf("invalid identity/auth contract accepted or leaked: %v", err)
			}
			if err := (sportarr.App{}).Setup(context.Background(), h); err == nil || secrets["SPORTARR_API_KEY"] != f.key || f.logins != 0 {
				t.Fatalf("Setup tried account/key replacement: %v", err)
			}
			if strings.Contains(output.String(), f.key) {
				t.Fatal("identity error exposed a synthetic secret")
			}
		})
	}
}

func TestSportarrManualBootstrapAndUserChoicesStayUntouched(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sportarrFake)
	}{
		{"new instance", func(f *sportarrFake) { f.method, f.user, f.password, f.loginStatus = "none", "", "", 401 }},
		{"other account", func(f *sportarrFake) { f.user = "person-owned" }},
		{"different password", func(f *sportarrFake) { f.password = "person-owned-password" }},
		{"Basic auth", func(f *sportarrFake) { f.method = "basic" }},
		{"external auth", func(f *sportarrFake) { f.method = "external" }},
		{"local auth bypass", func(f *sportarrFake) { f.required = "disabledforlocaladdresses" }},
		{"wrong session identity", func(f *sportarrFake) { f.sessionUser = "another-user" }},
		{"bypassed session auth", func(f *sportarrFake) { f.authBody = `{"authenticated":true,"authDisabled":true,"username":"admin"}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, h, rec, secrets := sportarrServer(t)
			tc.change(f)
			before := map[string]string{"key": f.key, "user": f.user, "password": f.password, "method": f.method, "required": f.required}
			err := (sportarr.App{}).Setup(context.Background(), h)
			if !errors.Is(err, helpers.ErrManual) || rec.Status("credentials") != stepstatus.StepManual {
				t.Fatalf("missing retryable Manual boundary: %v", err)
			}
			after := map[string]string{"key": f.key, "user": f.user, "password": f.password, "method": f.method, "required": f.required}
			if !reflect.DeepEqual(before, after) || secrets["SPORTARR_API_KEY"] != "" || f.sessions != 0 {
				t.Fatal("Setup adopted or modified unsupported authentication")
			}
		})
	}
}

func TestSportarrRejectsMalformedCredentialsWithoutLeakingResponsesOrKeepingSessions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sportarrFake)
	}{
		{"malformed settings", func(f *sportarrFake) { f.settingsBody = `{"securitySettings":"sportarr-synthetic-key"}` }},
		{"object instead of encoded section", func(f *sportarrFake) { f.settingsBody = `{"securitySettings":{"authenticationMethod":"forms"}}` }},
		{"missing key", func(f *sportarrFake) { f.settingsBody = `{"securitySettings":"{}"}` }},
		{"candidate key rejected", func(f *sportarrFake) { f.settingsKey = "rejected-candidate-key" }},
		{"null login result", func(f *sportarrFake) { f.loginBody = `{"success":null}` }},
		{"wrong login result", func(f *sportarrFake) { f.loginBody = `{"success":false}` }},
		{"missing session cookie", func(f *sportarrFake) { f.missingCookie = true }},
		{"non HttpOnly cookie", func(f *sportarrFake) { f.insecureCookie = true }},
		{"missing session result", func(f *sportarrFake) { f.authBody = `{}` }},
		{"session error payload", func(f *sportarrFake) { f.authBody = `{"error":"sportarr-synthetic-key"}` }},
		{"logout failed", func(f *sportarrFake) { f.logoutStatus = 500 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, h, _, secrets := sportarrServer(t)
			var output bytes.Buffer
			h.Log.AddSink(events.JSON(&output))
			tc.change(f)
			err := (sportarr.App{}).Setup(context.Background(), h)
			if err == nil || secrets["SPORTARR_API_KEY"] != "" || f.sessions != 0 {
				t.Fatalf("malformed credential result was accepted/stored or session leaked: %v", err)
			}
			for _, secret := range []string{f.key, f.password, "sportarr-synthetic-session", "rejected-candidate-key"} {
				if strings.Contains(err.Error()+output.String(), secret) {
					t.Fatal("error exposed a synthetic secret")
				}
			}
		})
	}
}

func TestSportarrChecksDoNotBorrowAmbientCookies(t *testing.T) {
	f, srv, h, _, secrets := sportarrServer(t)
	secrets["SPORTARR_API_KEY"] = "rejected-synthetic-key"
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "SportarrAuth", Value: "sportarr-synthetic-session"}})
	h.Config.HTTP.Jar = jar
	f.sessions = 1
	if err := (sportarr.App{}).Check(context.Background(), h); !errors.Is(err, helpers.ErrManual) || f.logins != 0 {
		t.Fatalf("ambient session hid a rejected API key: %v", err)
	}
}

func TestSportarrDeploymentUsesOfficialImageAndPreservesStorageChoices(t *testing.T) {
	a := sportarr.App{}
	info := a.Info()
	if info.ID != "sportarr" || info.Service != "sportarr" || info.Name != "Sportarr" || info.Image != "sportarr/sportarr:latest" || info.ContainerPort != 1867 || info.Port != 1867 || !info.UsesMedia {
		t.Fatalf("unexpected App metadata: %+v", info)
	}
	if len(a.Links()) != 0 {
		t.Fatal("App unexpectedly wires a downloader or provider")
	}
	inputs := a.Inputs()
	if len(inputs) != 1 || inputs[0].Key != "api_key" || !inputs[0].Private || !inputs[0].RecoveryOnly || inputs[0].Required {
		t.Fatal("API key recovery must use an optional private Project input")
	}
	for _, shared := range []bool{false, true} {
		settings := map[string]string{}
		if shared {
			settings["MEDIA_LAYOUT"] = "shared"
		}
		compose := a.Compose(app.RenderContext{Port: 21867, Settings: settings})
		var body struct {
			Services map[string]struct {
				Image       string            `yaml:"image"`
				User        string            `yaml:"user"`
				Restart     string            `yaml:"restart"`
				Ports       []string          `yaml:"ports"`
				Volumes     []string          `yaml:"volumes"`
				Environment map[string]string `yaml:"environment"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(compose), &body); err != nil {
			t.Fatal(err)
		}
		svc := body.Services["sportarr"]
		if len(body.Services) != 1 || svc.Image != info.Image || svc.User != "${PUID}:${PGID}" || svc.Restart != "unless-stopped" || !reflect.DeepEqual(svc.Ports, []string{"${BIND_IP}:21867:1867"}) {
			t.Fatal("deployment does not use the official non-root single-service contract")
		}
		mounts := []string{"./config:/config", "${MEDIA_ROOT}:/media", "${DOWNLOADS_ROOT}:/downloads"}
		if shared {
			mounts = []string{"./config:/config", "${DATA_ROOT}:/data"}
		}
		if !reflect.DeepEqual(svc.Volumes, mounts) || svc.Environment["UMASK"] != "${UMASK}" || svc.Environment["TZ"] != "${TZ}" {
			t.Fatal("storage, timezone or ownership defaults changed")
		}
		for _, forbidden := range []string{"privileged:", "devices:", "docker.sock", "container_name:", "SPORTARR_API_KEY", "Sportarr__ApiKey", "SPORTARR_STARTUP_SCRIPT"} {
			if strings.Contains(compose, forbidden) {
				t.Fatalf("unexpected deployment setting %s", forbidden)
			}
		}
	}
}
