package bazarr_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/bazarr"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

func TestBazarrDeclaresIndependentLibraryLinks(t *testing.T) {
	links := (bazarr.App{}).Links()
	if len(links) != 2 || links[0].ID != "link-sonarr" || links[0].Target != "sonarr" || links[1].ID != "link-radarr" || links[1].Target != "radarr" {
		t.Fatal("Bazarr must own independent Sonarr and Radarr Links")
	}
	for _, link := range links {
		if link.Apply == nil || link.Remove == nil {
			t.Fatal("each library Link must support scoped apply and removal")
		}
	}
}

type libraryFixture struct {
	settings       map[string]any
	posts          []map[string]string
	calls          []string
	secrets        fakeSecrets
	from           *helpers.H
	targets        map[string]*helpers.H
	records        []helpers.StepResult
	handler        http.Handler
	bazarrResponse func(http.ResponseWriter, *http.Request) bool
	targetResponse func(string, http.ResponseWriter, *http.Request) bool
	postStatus     int
	afterPost      func()
}

func newLibraryFixture(t *testing.T) *libraryFixture {
	t.Helper()
	f := &libraryFixture{
		settings: map[string]any{
			"general":         map[string]any{"use_sonarr": false, "use_radarr": false, "path_mappings": []any{}, "path_mappings_movie": []any{}, "serie_default_profile": 7, "movie_default_profile": 9, "enabled_providers": []any{"person-provider"}},
			"sonarr":          map[string]any{"ip": "127.0.0.1", "port": 8989, "base_url": "/", "ssl": false, "apikey": "", "series_sync_on_live": true, "series_sync": 180},
			"radarr":          map[string]any{"ip": "127.0.0.1", "port": 7878, "base_url": "/", "ssl": false, "apikey": "", "movies_sync_on_live": false, "movies_sync": 360},
			"person-provider": map[string]any{"password": "person-provider-secret"},
		},
		secrets: fakeSecrets{"BAZARR_API_KEY": apiKey, "SONARR_API_KEY": "accepted-sonarr-key", "RADARR_API_KEY": "accepted-radarr-key"},
		targets: map[string]*helpers.H{},
	}
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if f.bazarrResponse != nil && f.bazarrResponse(w, r) {
			return
		}
		if r.Header.Get("X-API-KEY") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/system/settings":
			json.NewEncoder(w).Encode(f.settings)
		case "POST /api/system/settings":
			r.ParseForm()
			posted := map[string]string{}
			for key, values := range r.PostForm {
				posted[key] = values[0]
				parts := strings.Split(key, "-")
				if len(parts) != 3 || parts[0] != "settings" {
					t.Error("unexpected form field")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var value any = values[0]
				if parts[2] == "port" {
					value, _ = strconv.Atoi(values[0])
				}
				if parts[2] == "ssl" {
					value = values[0] == "true"
				}
				f.settings[parts[1]].(map[string]any)[parts[2]] = value
			}
			f.posts = append(f.posts, posted)
			if f.afterPost != nil {
				f.afterPost()
			}
			if f.postStatus != 0 {
				w.WriteHeader(f.postStatus)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/series", "GET /api/movies":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "total": 0})
		case "GET /api/files/sonarr", "GET /api/files/radarr", "GET /api/files":
			base := r.URL.Query().Get("path")
			json.NewEncoder(w).Encode([]map[string]any{{"name": "tv", "path": path.Join(base, "tv") + "/", "children": true}, {"name": "movies", "path": path.Join(base, "movies") + "/", "children": true}})
		default:
			t.Error("unexpected Bazarr API action: " + r.Method + " " + r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	exec := libraryExecutor{}
	f.from = helpers.New(helpers.Config{App: "bazarr", Service: "bazarr", ContainerPort: 6767, BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(), Secrets: f.secrets, Log: events.Discard(), Exec: exec, Settings: map[string]string{"MEDIA_LAYOUT": "shared"}, Record: func(r helpers.StepResult) { f.records = append(f.records, r) }})
	for _, id := range []string{"sonarr", "radarr"} {
		name, port, root, collection := "Sonarr", 8989, "/data/media/tv", "/api/v3/series"
		if id == "radarr" {
			name, port, root, collection = "Radarr", 7878, "/data/media/movies", "/api/v3/movie"
		}
		targetID := id
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.calls = append(f.calls, targetID+" "+r.Method+" "+r.URL.Path)
			if f.targetResponse != nil && f.targetResponse(targetID, w, r) {
				return
			}
			if r.Header.Get("X-Api-Key") != "accepted-"+targetID+"-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.Method + " " + r.URL.Path {
			case "GET /api/v3/system/status":
				json.NewEncoder(w).Encode(map[string]any{"appName": name, "instanceName": "selected-" + targetID, "version": "4.0.0", "urlBase": ""})
			case "GET /api/v3/rootfolder":
				json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "path": root, "accessible": true}})
			case "GET " + collection:
				json.NewEncoder(w).Encode([]any{})
			default:
				t.Error("unexpected target API action")
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(target.Close)
		f.targets[id] = helpers.New(helpers.Config{App: id, Service: id, ContainerPort: port, Port: 59000, BaseURL: target.URL, URL: target.URL, HTTP: target.Client(), Secrets: f.secrets, Log: events.Discard(), Exec: exec, Settings: f.from.Settings})
	}
	return f
}

func TestBazarrConnectionRemovalRestoresOnlyOwnedFieldsAndReinstallDoesNotAdopt(t *testing.T) {
	f := newLibraryFixture(t)
	for _, id := range []string{"sonarr", "radarr"} {
		_ = libraryLink(t, id).Apply(t.Context(), f.from, f.targets[id])
	}
	radarr, _ := json.Marshal(f.settings["radarr"])
	radarrRecord := f.secrets["BAZARR_LINK_RADARR_RESOURCE"]
	delete(f.secrets, "SONARR_API_KEY")
	if err := libraryLink(t, "sonarr").Remove(t.Context(), f.from, f.targets["sonarr"]); err != nil {
		t.Fatal("owned cleanup must not need target API access")
	}
	sonarr := f.settings["sonarr"].(map[string]any)
	if sonarr["ip"] != "127.0.0.1" || sonarr["apikey"] != "" || f.secrets["BAZARR_LINK_SONARR_RESOURCE"] != "" {
		t.Fatal("owned fields or ownership were not restored")
	}
	remaining, _ := json.Marshal(f.settings["radarr"])
	if string(radarr) != string(remaining) || radarrRecord != f.secrets["BAZARR_LINK_RADARR_RESOURCE"] {
		t.Fatal("other backend was changed during removal")
	}
	f.secrets["SONARR_API_KEY"] = "accepted-sonarr-key"
	_ = libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"])
	if len(f.posts) != 4 {
		t.Fatal("reinstall should perform exactly one new scoped settings write")
	}
	delete(f.secrets, "BAZARR_LINK_SONARR_RESOURCE")
	if err := libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"]); !errors.Is(err, helpers.ErrManual) || len(f.posts) != 4 {
		t.Fatal("missing ownership must not adopt the matching connection")
	}
	f.secrets["BAZARR_API_KEY"] = "rejected-key"
	if err := libraryLink(t, "sonarr").Remove(t.Context(), f.from, f.targets["sonarr"]); err != nil || len(f.posts) != 4 {
		t.Fatal("unowned removal must preserve user settings without authentication probes")
	}
}

func TestBazarrConnectionEditsEnabledChoicesAndKeyChangesRemainManual(t *testing.T) {
	for _, change := range []string{"user-edit", "enabled-before-write", "enabled-removal", "target-key", "missing-ownership", "corrupt-ownership"} {
		t.Run(change, func(t *testing.T) {
			f := newLibraryFixture(t)
			link, target := libraryLink(t, "sonarr"), f.targets["sonarr"]
			if change != "enabled-before-write" {
				_ = link.Apply(t.Context(), f.from, target)
			}
			switch change {
			case "user-edit":
				f.settings["sonarr"].(map[string]any)["ip"] = "person-host"
			case "enabled-before-write", "enabled-removal":
				f.settings["general"].(map[string]any)["use_sonarr"] = true
			case "target-key":
				f.secrets["SONARR_API_KEY"] = "changed-accepted-key"
			case "missing-ownership":
				delete(f.secrets, "BAZARR_LINK_SONARR_RESOURCE")
			case "corrupt-ownership":
				f.secrets["BAZARR_LINK_SONARR_RESOURCE"] = "{invalid"
			}
			before, _ := json.Marshal(f.settings)
			posts := len(f.posts)
			err := link.Apply(t.Context(), f.from, target)
			if change == "enabled-removal" {
				err = link.Remove(t.Context(), f.from, target)
			}
			after, _ := json.Marshal(f.settings)
			if !errors.Is(err, helpers.ErrManual) || len(f.posts) != posts || string(before) != string(after) {
				t.Fatal("unsafe connection change was not held without mutation")
			}
			if len(f.records) == 0 || f.records[len(f.records)-1].Status != stepstatus.StepManual || f.records[len(f.records)-1].Hint == "" {
				t.Fatal("specific Manual recovery was not recorded")
			}
		})
	}
}

func TestBazarrUncertainSettingsMutationIsNotRepeated(t *testing.T) {
	for _, phase := range []string{"apply", "remove", "preservation"} {
		t.Run(phase, func(t *testing.T) {
			f := newLibraryFixture(t)
			link, target := libraryLink(t, "sonarr"), f.targets["sonarr"]
			if phase == "remove" {
				_ = link.Apply(t.Context(), f.from, target)
			}
			f.postStatus = http.StatusInternalServerError
			if phase == "preservation" {
				f.postStatus = 0
				f.afterPost = func() { f.settings["general"].(map[string]any)["movie_default_profile"] = 42 }
			}
			run := link.Apply
			if phase == "remove" {
				run = link.Remove
			}
			if err := run(t.Context(), f.from, target); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("uncertain mutation should retain Manual ownership")
			}
			posts, record := len(f.posts), f.secrets["BAZARR_LINK_SONARR_RESOURCE"]
			if record == "" {
				t.Fatal("uncertain ownership hold disappeared")
			}
			f.postStatus, f.afterPost = 0, nil
			_ = link.Apply(t.Context(), f.from, target)
			if posts != len(f.posts) || record != f.secrets["BAZARR_LINK_SONARR_RESOURCE"] {
				t.Fatal("a pending transaction was automatically repeated or adopted")
			}
		})
	}
}

func TestBazarrSetupFinishesExplicitRemovalAfterTargetDisappears(t *testing.T) {
	f := newLibraryFixture(t)
	link := libraryLink(t, "sonarr")
	_ = link.Apply(t.Context(), f.from, f.targets["sonarr"])
	f.settings["general"].(map[string]any)["use_sonarr"] = true
	if err := link.Remove(t.Context(), f.from, f.targets["sonarr"]); !errors.Is(err, helpers.ErrManual) || len(f.posts) != 1 {
		t.Fatal("active removal must preserve settings until the person disables the backend")
	}
	f.settings["general"].(map[string]any)["use_sonarr"] = false
	f.secrets["ADMIN_USER"], f.secrets["ADMIN_PASSWORD"] = adminUser, adminPass
	local := (&fakeBazarr{user: adminUser, pass: adminPass}).handler()
	f.bazarrResponse = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/" || r.URL.Path == "/api/system/status" || r.URL.Path == "/api/badges" {
			local.ServeHTTP(w, r)
			return true
		}
		return false
	}
	if err := (bazarr.App{}).Setup(t.Context(), f.from); err != nil {
		t.Fatal("local Setup should retry the recorded removal, not reinstall the missing target")
	}
	if len(f.posts) != 2 || f.secrets["BAZARR_LINK_SONARR_RESOURCE"] != "" || f.settings["sonarr"].(map[string]any)["apikey"] != "" {
		t.Fatal("recorded removal was not recoverable after its target disappeared")
	}
}

func TestBazarrMalformedSingletonOwnershipCannotAuthorizeRemoval(t *testing.T) {
	for _, corruption := range []string{"missing-before-field", "null-applied-field", "duplicate-phase"} {
		t.Run(corruption, func(t *testing.T) {
			f := newLibraryFixture(t)
			link := libraryLink(t, "sonarr")
			_ = link.Apply(t.Context(), f.from, f.targets["sonarr"])
			key := "BAZARR_LINK_SONARR_RESOURCE"
			var record map[string]any
			json.Unmarshal([]byte(f.secrets[key]), &record)
			switch corruption {
			case "missing-before-field":
				delete(record["before"].(map[string]any), "ssl")
			case "null-applied-field":
				record["applied"].(map[string]any)["ssl"] = nil
			}
			raw, _ := json.Marshal(record)
			if corruption == "duplicate-phase" {
				raw = []byte(strings.Replace(string(raw), `"phase":"configured"`, `"phase":"applying","phase":"configured"`, 1))
			}
			f.secrets[key] = string(raw)
			if err := link.Remove(t.Context(), f.from, f.targets["sonarr"]); !errors.Is(err, helpers.ErrManual) || len(f.posts) != 1 || f.secrets[key] != string(raw) {
				t.Fatal("malformed ownership authorized a destructive settings restoration")
			}
		})
	}
}

type libraryExecutor struct{ identity string }

func (e libraryExecutor) Exec(context.Context, string, map[string]string, ...string) (system.Result, error) {
	if e.identity != "" {
		return system.Result{Stdout: []byte(e.identity + "\n")}, nil
	}
	return system.Result{Stdout: []byte("2049:12345\n")}, nil
}

func TestBazarrLibraryPrerequisitesFailClosedBeforeAnySettingsWrite(t *testing.T) {
	for _, scenario := range []string{"unowned", "imported", "enabled", "missing-key", "rejected-bazarr-key", "rejected-target-key", "wrong-target", "custom-base", "null-roots", "inaccessible-root", "null-library", "wrong-library-root", "mappings", "missing-field", "duplicate-settings", "duplicate-target-status", "duplicate-library-count", "different-directory", "empty-stat"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLibraryFixture(t)
			block := f.settings["sonarr"].(map[string]any)
			switch scenario {
			case "unowned":
				block["ip"], block["apikey"] = "sonarr", "accepted-sonarr-key"
			case "imported":
				f.bazarrResponse = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/series" {
						return false
					}
					w.Write([]byte(`{"data":[{"sonarrSeriesId":12}],"total":1}`))
					return true
				}
			case "enabled":
				f.settings["general"].(map[string]any)["use_sonarr"] = true
			case "missing-key":
				delete(f.secrets, "SONARR_API_KEY")
			case "rejected-bazarr-key":
				f.secrets["BAZARR_API_KEY"] = "bad-bazarr-key"
			case "rejected-target-key":
				f.secrets["SONARR_API_KEY"] = "bad-target-key"
			case "wrong-target", "custom-base":
				f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/v3/system/status" {
						return false
					}
					name, base := "Radarr", ""
					if scenario == "custom-base" {
						name, base = "Sonarr", "/existing-base"
					}
					json.NewEncoder(w).Encode(map[string]any{"appName": name, "version": "4.0.0", "urlBase": base})
					return true
				}
			case "null-roots":
				f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/v3/rootfolder" {
						return false
					}
					w.Write([]byte(`null`))
					return true
				}
			case "inaccessible-root":
				f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/v3/rootfolder" {
						return false
					}
					w.Write([]byte(`[{"id":1,"path":"/data/media/tv","accessible":false}]`))
					return true
				}
			case "null-library", "wrong-library-root":
				f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/v3/series" {
						return false
					}
					data := `null`
					if scenario == "wrong-library-root" {
						data = `[{"id":1,"path":"/other/library/show","rootFolderPath":"/other/library"}]`
					}
					w.Write([]byte(data))
					return true
				}
			case "mappings":
				f.settings["general"].(map[string]any)["path_mappings"] = []any{[]any{"/old/tv", "/chosen/tv"}}
			case "missing-field":
				delete(block, "ssl")
			case "duplicate-settings":
				f.bazarrResponse = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/system/settings" {
						return false
					}
					w.Write([]byte(`{"sonarr":{},"sonarr":{},"general":{}}`))
					return true
				}
			case "duplicate-target-status":
				f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/v3/system/status" {
						return false
					}
					w.Write([]byte(`{"appName":"Radarr","appName":"Sonarr","version":"4.0.0","urlBase":""}`))
					return true
				}
			case "duplicate-library-count":
				f.bazarrResponse = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/api/series" {
						return false
					}
					w.Write([]byte(`{"data":[],"total":4,"total":0}`))
					return true
				}
			case "different-directory":
				f.targets["sonarr"].Config.Exec = libraryExecutor{identity: "2049:99999"}
			case "empty-stat":
				f.targets["sonarr"].Config.Exec = libraryExecutor{identity: "not-a-directory-identity"}
			}
			before, _ := json.Marshal(f.settings)
			err := libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"])
			after, _ := json.Marshal(f.settings)
			if !errors.Is(err, helpers.ErrManual) || len(f.posts) != 0 || f.secrets["BAZARR_LINK_SONARR_RESOURCE"] != "" || string(before) != string(after) {
				t.Fatal("an unverified prerequisite authorized mutation or ownership")
			}
		})
	}
}

func TestBazarrPreservesEffectiveLegacyAndCustomRoots(t *testing.T) {
	for _, root := range []string{"/media/tv", "/person/chosen/tv"} {
		t.Run(root, func(t *testing.T) {
			f := newLibraryFixture(t)
			delete(f.from.Settings, "MEDIA_LAYOUT")
			f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/v3/rootfolder" {
					return false
				}
				json.NewEncoder(w).Encode([]map[string]any{{"id": 9, "path": root, "accessible": true}})
				return true
			}
			if err := libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"]); !errors.Is(err, helpers.ErrManual) || len(f.posts) != 1 {
				t.Fatal("the actual existing root should be checked without resetting legacy or custom choices")
			}
			for _, call := range f.calls {
				if strings.Contains(call, "POST /api/v3") || strings.Contains(call, "PATCH") {
					t.Fatal("library verification changed the target")
				}
			}
		})
	}
}

func TestBazarrNeverTreatsAnEmptyConfiguredFilesystemBrowserAsAuthorization(t *testing.T) {
	f := newLibraryFixture(t)
	f.bazarrResponse = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/files/sonarr" {
			return false
		}
		w.Write([]byte(`[]`))
		return true
	}
	err := libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"])
	if !errors.Is(err, helpers.ErrManual) || !strings.Contains(f.records[len(f.records)-1].Hint, "empty browser response") {
		t.Fatal("empty native browser response was treated as positive connection evidence")
	}
}

type failingLibrarySecrets struct {
	values fakeSecrets
	phase  string
	clear  bool
}

func (s *failingLibrarySecrets) Get(key string) string { return s.values[key] }
func (s *failingLibrarySecrets) Set(key, value string) error {
	if strings.HasSuffix(key, "_RESOURCE") && ((s.clear && value == "") || (s.phase != "" && strings.Contains(value, `"phase":"`+s.phase+`"`))) {
		return errors.New("private storage unavailable")
	}
	s.values[key] = value
	return nil
}

func TestBazarrPrivateTransactionPersistenceFailuresNeverAuthorizeBlindRetries(t *testing.T) {
	for _, failure := range []string{"applying", "configured", "removing", "clear"} {
		t.Run(failure, func(t *testing.T) {
			f := newLibraryFixture(t)
			link, target := libraryLink(t, "sonarr"), f.targets["sonarr"]
			if failure == "removing" || failure == "clear" {
				_ = link.Apply(t.Context(), f.from, target)
			}
			store := &failingLibrarySecrets{values: f.secrets, phase: failure, clear: failure == "clear"}
			f.from.Secrets = store
			run := link.Apply
			if failure == "removing" || failure == "clear" {
				run = link.Remove
			}
			if err := run(t.Context(), f.from, target); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("private persistence failure must retain actionable Manual recovery")
			}
			posts := len(f.posts)
			if failure == "applying" && posts != 0 {
				t.Fatal("a write ran without saved pending ownership")
			}
			if failure == "configured" && posts != 1 {
				t.Fatal("unexpected mutation count before finalization failure")
			}
			store.phase, store.clear = "", false
			if failure == "clear" {
				if err := run(t.Context(), f.from, target); err != nil || len(f.posts) != posts || f.secrets["BAZARR_LINK_SONARR_RESOURCE"] != "" {
					t.Fatal("verified restored fields should finalize recorded removal without another settings write")
				}
			} else if failure == "configured" {
				if err := run(t.Context(), f.from, target); !errors.Is(err, helpers.ErrManual) || len(f.posts) != posts {
					t.Fatal("failed ownership finalization was blindly retried")
				}
			}
		})
	}
}

func TestBazarrConnectionDiagnosticsDoNotExposeCredentials(t *testing.T) {
	f := newLibraryFixture(t)
	var logged []events.Event
	f.from.Log.AddSink(events.SinkFunc(func(event events.Event) { logged = append(logged, event) }))
	_ = libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"])
	raw, _ := json.Marshal(struct {
		Events  []events.Event
		Records []helpers.StepResult
	}{logged, f.records})
	for _, secret := range []string{apiKey, "accepted-sonarr-key", "person-provider-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("connection diagnostics exposed credentials")
		}
	}
	for _, call := range f.calls {
		if strings.Contains(call, "search") || strings.Contains(call, "task") || strings.Contains(call, "provider") || strings.Contains(call, "PATCH") {
			t.Fatal("connection verification triggered a provider or media action")
		}
	}
}

func TestBazarrConnectionReadsNeverFollowCredentialBearingRedirects(t *testing.T) {
	for _, source := range []string{"target", "bazarr"} {
		t.Run(source, func(t *testing.T) {
			f := newLibraryFixture(t)
			hits := 0
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(http.StatusOK) }))
			t.Cleanup(foreign.Close)
			redirect := func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Location", foreign.URL)
				w.WriteHeader(http.StatusFound)
				return true
			}
			if source == "target" {
				f.targetResponse = func(_ string, w http.ResponseWriter, r *http.Request) bool { return redirect(w, r) }
			} else {
				f.bazarrResponse = redirect
			}
			if err := libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"]); err == nil || hits != 0 || len(f.posts) != 0 {
				t.Fatal("redirected endpoint received a private credential or authorized settings mutation")
			}
		})
	}
}
func (libraryExecutor) LogText(context.Context, int, ...string) (string, error) { return "", nil }
func (libraryExecutor) Restart(context.Context, ...string) error {
	return errors.New("unexpected restart")
}
func (libraryExecutor) Up(context.Context, ...string) error { return errors.New("unexpected start") }

func libraryLink(t *testing.T, id string) app.Link {
	t.Helper()
	for _, link := range (bazarr.App{}).Links() {
		if link.Target == id {
			return link
		}
	}
	t.Fatal("missing library Link")
	return app.Link{}
}

func TestBazarrSavesOnlyDisabledConnectionFieldsAndRetainsManualAcceptance(t *testing.T) {
	for _, id := range []string{"sonarr", "radarr"} {
		t.Run(id, func(t *testing.T) {
			f := newLibraryFixture(t)
			other := "radarr"
			if id == "radarr" {
				other = "sonarr"
			}
			before, _ := json.Marshal(f.settings[other])
			err := libraryLink(t, id).Apply(t.Context(), f.from, f.targets[id])
			if !errors.Is(err, helpers.ErrManual) || len(f.posts) != 1 {
				t.Fatal("disabled connection should be saved once without claiming native acceptance")
			}
			if len(f.posts[0]) != 5 || f.posts[0]["settings-"+id+"-ip"] != id || f.posts[0]["settings-"+id+"-apikey"] != "accepted-"+id+"-key" {
				t.Fatal("wrong settings schema or credentials")
			}
			if f.posts[0]["settings-"+id+"-port"] == "59000" {
				t.Fatal("host port used as internal port")
			}
			after, _ := json.Marshal(f.settings[other])
			if string(before) != string(after) || f.settings["general"].(map[string]any)["use_"+id] != false {
				t.Fatal("unrelated connection or disabled choice changed")
			}
			saved := f.secrets["BAZARR_LINK_"+strings.ToUpper(id)+"_RESOURCE"]
			if saved == "" {
				t.Fatal("missing singleton ownership")
			}
			_ = libraryLink(t, id).Apply(t.Context(), f.from, f.targets[id])
			if len(f.posts) != 1 || saved != f.secrets["BAZARR_LINK_"+strings.ToUpper(id)+"_RESOURCE"] {
				t.Fatal("rerun duplicated the settings mutation or changed ownership")
			}
			wantGeneral := map[string]any{"use_sonarr": false, "use_radarr": false, "path_mappings": []any{}, "path_mappings_movie": []any{}, "serie_default_profile": 7, "movie_default_profile": 9, "enabled_providers": []any{"person-provider"}}
			if !reflect.DeepEqual(f.settings["general"], wantGeneral) {
				t.Fatal("library, provider or path choices changed")
			}
		})
	}
}
