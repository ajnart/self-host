package plantit_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/plantit"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type secrets map[string]string

func (s secrets) Get(key string) string       { return s[key] }
func (s secrets) Set(key, value string) error { s[key] = value; return nil }

type fakePlantit struct {
	mu        sync.Mutex
	username  string
	password  string
	id        int64
	userID    int64
	paths     []string
	badBody   bool
	userError bool
}

func (f *fakePlantit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	switch r.URL.Path {
	case "/api/info/ping":
		w.Write([]byte("pong"))
	case "/api/authentication/login":
		var body struct{ Username, Password string }
		json.NewDecoder(r.Body).Decode(&body)
		if f.badBody {
			w.Write([]byte("<html>"))
			return
		}
		if f.username == "" || body.Username != f.username || body.Password != f.password {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("not json"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": f.id, "username": f.username, "jwt": map[string]any{"value": "fixture-jwt"}})
	case "/api/user":
		if r.Header.Get("Authorization") != "Bearer fixture-jwt" || f.userError {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		userID := f.userID
		if userID == 0 {
			userID = f.id
		}
		json.NewEncoder(w).Encode(map[string]any{"id": userID, "username": f.username})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakePlantit) mutations() int {
	count := 0
	for _, path := range f.paths {
		if strings.HasPrefix(path, "POST") && !strings.HasSuffix(path, "/api/authentication/login") || strings.Contains(path, "signup") || strings.Contains(path, "reset") {
			count++
		}
	}
	return count
}

func newHelpers(t *testing.T, fake *fakePlantit, store secrets, homarr bool) (*helpers.H, *[]helpers.StepResult) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	steps := &[]helpers.StepResult{}
	store["ADMIN_USER"], store["ADMIN_PASSWORD"] = "gardener", "fixture-password"
	return helpers.New(helpers.Config{
		App: "plant-it", Service: "plant-it", HTTP: server.Client(), Log: events.Discard(), Secrets: store,
		URL: "http://127.0.0.1:3000", Inputs: map[string]string{"api-port": parsed.Port()},
		Record: func(step helpers.StepResult) { *steps = append(*steps, step) },
		Peer: func(id string) *helpers.H {
			if id == "homarr" && homarr {
				return &helpers.H{}
			}
			return nil
		},
	}), steps
}

func TestComposeSeparatesUIAndAPIAndKeepsStorage(t *testing.T) {
	compose := plantit.App{}.Compose(app.RenderContext{Port: 3100, Inputs: map[string]string{"api-port": "8180"}})
	for _, want := range []string{
		"${BIND_IP}:3100:3000", "${BIND_IP}:8180:8080", "./upload-dir:/upload-dir", "./db:/var/lib/mysql",
		"msdeluise/plant-it-server:0.10.0", "plant-it-database:", "CACHE_TYPE: none", "NTFY_ENABLED: \"false\"", "GOTIFY_ENABLED: \"false\"",
		"${PLANT_IT_JWT_SECRET}", "${PLANT_IT_DB_PASSWORD}", "${PLANT_IT_DB_ROOT_PASSWORD}",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	for _, banned := range []string{"latest", "SMTP", "3306:3306", "docker.sock"} {
		if strings.Contains(compose, banned) {
			t.Errorf("compose contains %q", banned)
		}
	}
	if got := (plantit.App{}).Inputs()[0]; got.Key != "api-port" || got.Default != "8080" || len(got.HostPortProtocols) != 1 || got.HostPortProtocols[0] != "tcp" {
		t.Fatalf("api port input = %+v", got)
	}
}

func TestSetupRecordsTheExistingOwnerOnce(t *testing.T) {
	fake := &fakePlantit{username: "gardener", password: "fixture-password", id: 7}
	store := secrets{}
	h, _ := newHelpers(t, fake, store, true)
	if err := (plantit.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if store["PLANT_IT_OWNER_ID"] != "7" || store["PLANT_IT_HOMARR_USERNAME"] != "gardener" || store["PLANT_IT_HOMARR_PASSWORD"] != "fixture-password" {
		t.Fatalf("owner snapshot = %v", store)
	}
	store["ADMIN_PASSWORD"] = "rotated-password"
	if err := (plantit.App{}).Setup(t.Context(), h); err == nil {
		t.Fatal("a rotated Admin login must not sign in")
	}
	if store["PLANT_IT_HOMARR_PASSWORD"] != "fixture-password" {
		t.Fatal("rerun rotated the recorded owner password")
	}
	if fake.mutations() != 0 {
		t.Fatalf("unexpected writes: %v", fake.paths)
	}
}

func TestSetupWithoutAnOwnerIsManualAndNeverSignsUp(t *testing.T) {
	fake := &fakePlantit{}
	store := secrets{}
	h, steps := newHelpers(t, fake, store, true)
	err := (plantit.App{}).Setup(t.Context(), h)
	if err == nil {
		t.Fatal("Setup must stop at the owner boundary")
	}
	if fake.mutations() != 0 {
		t.Fatalf("Setup wrote: %v", fake.paths)
	}
	if store["PLANT_IT_OWNER_ID"] != "" || len(*steps) == 0 {
		t.Fatalf("unexpected state: %v %v", store, *steps)
	}
	if hint := (*steps)[len(*steps)-1].Hint; !strings.Contains(hint, "self-host credentials") || !strings.Contains(hint, "127.0.0.1:") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestSetupWithoutHomarrSkipsTheSnapshot(t *testing.T) {
	fake := &fakePlantit{username: "gardener", password: "fixture-password", id: 3}
	store := secrets{}
	h, _ := newHelpers(t, fake, store, false)
	if err := (plantit.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if store["PLANT_IT_OWNER_ID"] != "" {
		t.Fatal("no Homarr, no owner snapshot")
	}
}

func TestSetupRefusesToAdoptAnotherOwnerOrRotate(t *testing.T) {
	for name, saved := range map[string]secrets{
		"different ID":   {"PLANT_IT_OWNER_ID": "9", "PLANT_IT_HOMARR_USERNAME": "gardener", "PLANT_IT_HOMARR_PASSWORD": "fixture-password"},
		"lost password":  {"PLANT_IT_OWNER_ID": "7", "PLANT_IT_HOMARR_USERNAME": "gardener"},
		"rejected login": {"PLANT_IT_OWNER_ID": "7", "PLANT_IT_HOMARR_USERNAME": "gardener", "PLANT_IT_HOMARR_PASSWORD": "old-password"},
		"lost ID":        {"PLANT_IT_HOMARR_USERNAME": "gardener", "PLANT_IT_HOMARR_PASSWORD": "fixture-password"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakePlantit{username: "gardener", password: "fixture-password", id: 7}
			h, _ := newHelpers(t, fake, saved, true)
			before := map[string]string{}
			for key, value := range saved {
				before[key] = value
			}
			if err := (plantit.App{}).Setup(t.Context(), h); err == nil {
				t.Fatal("Setup must stop for explicit recovery")
			}
			for key, value := range before {
				if saved[key] != value {
					t.Fatalf("%s changed", key)
				}
			}
			if len(saved) != len(before) {
				t.Fatalf("Setup recorded owner state: %v", saved)
			}
		})
	}
}

func TestOwnerVerificationRejectsMalformedAndMismatchedAnswers(t *testing.T) {
	for name, change := range map[string]func(*fakePlantit){
		"wrong password":    func(f *fakePlantit) { f.password = "other" },
		"non-JSON login":    func(f *fakePlantit) { f.badBody = true },
		"user read denied":  func(f *fakePlantit) { f.userError = true },
		"different user ID": func(f *fakePlantit) { f.userID = 8 },
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakePlantit{username: "gardener", password: "fixture-password", id: 7}
			change(fake)
			store := secrets{"PLANT_IT_OWNER_ID": "7", "PLANT_IT_HOMARR_USERNAME": "gardener", "PLANT_IT_HOMARR_PASSWORD": "fixture-password"}
			h, _ := newHelpers(t, fake, store, true)
			if err := plantit.VerifyHomarrOwner(t.Context(), h); err == nil {
				t.Fatal("verification accepted a bad owner")
			}
			if (plantit.App{}).Check(t.Context(), h) == nil {
				t.Fatal("Check accepted a bad owner")
			}
		})
	}
}

func TestOwnerIDMustBePositive(t *testing.T) {
	fake := &fakePlantit{username: "gardener", password: "fixture-password", id: 7}
	h, _ := newHelpers(t, fake, secrets{"PLANT_IT_OWNER_ID": "0", "PLANT_IT_HOMARR_USERNAME": "gardener", "PLANT_IT_HOMARR_PASSWORD": "fixture-password"}, true)
	if _, err := plantit.HomarrOwnerID(t.Context(), h); err == nil {
		t.Fatal("zero owner ID accepted")
	}
}

func TestAdminLoginLimitsAreRejectedBeforeAnyRequest(t *testing.T) {
	fake := &fakePlantit{}
	store := secrets{}
	h, _ := newHelpers(t, fake, store, true)
	store["ADMIN_USER"] = "ab"
	if err := (plantit.App{}).Setup(t.Context(), h); err == nil {
		t.Fatal("short Admin username accepted")
	}
	if fake.mutations() != 0 {
		t.Fatalf("writes: %v", fake.paths)
	}
}
