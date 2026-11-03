package arr_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/lidarr"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/qbittorrent"
	"github.com/ajnart/self-host/internal/apps/sabnzbd"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

type sabnzbdLinkFixture struct {
	mu                                           sync.Mutex
	key, incomplete, root, directory, script     string
	missing, missingDefault, rejected, malformed bool
	sorting                                      bool
	calls                                        []string
}

func newSabnzbdLinkFixture() *sabnzbdLinkFixture {
	return &sabnzbdLinkFixture{key: "fixture-full-key", incomplete: "/data/usenet/incomplete", root: "/data/usenet/complete", script: "Default"}
}

func (f *sabnzbdLinkFixture) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/login/" {
			r.ParseForm()
			if r.PostForm.Get("username") != "" && r.PostForm.Get("password") != "" {
				http.SetCookie(w, &http.Cookie{Name: "login_cookie", Value: "controlled-cookie"})
			}
			w.WriteHeader(303)
			return
		}
		if r.URL.Path != "/api" {
			w.WriteHeader(404)
			return
		}
		r.ParseForm()
		if r.Method == "GET" && r.Form.Get("mode") == "version" {
			json.NewEncoder(w).Encode(map[string]any{"version": "5.1.3"})
			return
		}
		if r.Method != "POST" {
			t.Error("SAB credentials must use POST forms")
			w.WriteHeader(405)
			return
		}
		mode, section, keyword := r.PostForm.Get("mode"), r.PostForm.Get("section"), r.PostForm.Get("keyword")
		f.calls = append(f.calls, mode+":"+section+":"+keyword)
		if mode == "auth" {
			auth := "badkey"
			if r.PostForm.Get("key") == f.key && !f.rejected {
				auth = "apikey"
			}
			json.NewEncoder(w).Encode(map[string]any{"auth": auth})
			return
		}
		if r.PostForm.Get("apikey") != f.key || f.rejected {
			json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "private-response-must-not-escape"})
			return
		}
		switch mode {
		case "queue":
			json.NewEncoder(w).Encode(map[string]any{"queue": map[string]any{"version": "5.1.3", "paused": false, "kbpersec": "0", "slots": []any{}}})
		case "history":
			json.NewEncoder(w).Encode(map[string]any{"history": map[string]any{"slots": []any{}}})
		case "fullstatus":
			t.Error("the Link must not resolve folders through potentially mutating fullstatus")
			w.WriteHeader(400)
		case "get_config":
			switch section {
			case "categories":
				if keyword == "*" && f.missingDefault {
					json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{}})
					return
				}
				var entries []any
				for _, category := range []string{"*", "tv", "movies", "music", "chosen"} {
					if keyword != "" && category != keyword {
						continue
					}
					if f.missing && category != "*" {
						continue
					}
					directory, script := f.directory, f.script
					if category == "*" {
						directory, script = "", "None"
					}
					entries = append(entries, map[string]any{"name": category, "dir": directory, "script": script, "pp": "3"})
				}
				if f.malformed && keyword == "" {
					json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"categories": nil}})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"categories": entries}})
			case "sorters":
				sorters := []any{}
				if f.sorting {
					sorters = append(sorters, map[string]any{"is_active": 1, "sort_cats": []any{}})
				}
				json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"sorters": sorters}})
			case "misc":
				key := r.PostForm.Get("keyword")
				value := ""
				switch key {
				case "complete_dir":
					value = f.root
				case "download_dir":
					value = f.incomplete
				case "username":
					value = "admin"
				}
				json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"misc": map[string]any{key: value}}})
			case "servers":
				json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{}})
			default:
				t.Errorf("unexpected configuration section %s", section)
				w.WriteHeader(400)
			}
		default:
			t.Errorf("target mutation or provider action %s", mode)
			w.WriteHeader(400)
		}
	})
}

type sabnzbdLinkExecutor struct {
	helpers.Executor
	distinct, missingPrerequisites    bool
	downloadService, consumerDownload string
}

func (e *sabnzbdLinkExecutor) Exec(_ context.Context, service string, _ map[string]string, args ...string) (system.Result, error) {
	targetService := e.downloadService
	if targetService == "" {
		targetService = "sabnzbd"
	}
	if len(args) == 9 && args[6] == "self-host-sabnzbd-read-prerequisites" {
		if service != targetService || strings.Join(args[:3], " ") != "s6-setuidgid abc sh" || e.missingPrerequisites {
			return system.Result{ExitCode: 1}, nil
		}
		return system.Result{Stdout: []byte("existing-download-folders\n")}, nil
	}
	if len(args) != 8 || strings.Join(args[:3], " ") != "s6-setuidgid abc sh" || args[6] != "self-host-download-path" {
		return system.Result{}, errors.New("unexpected command")
	}
	directory := args[7]
	if e.consumerDownload != "" && service != targetService && !strings.Contains(directory, "/media/") && directory != e.consumerDownload {
		return system.Result{ExitCode: 1}, nil
	}
	identity := "1:42"
	if strings.Contains(directory, "/media/") {
		identity = "1:99"
	}
	if e.distinct && service != targetService {
		identity = "1:77"
	}
	backing := "/shared/downloads"
	if identity == "1:99" {
		backing = "/shared/library"
	}
	return system.Result{Stdout: []byte(directory + "\n" + identity + "\n1 0 0:5 " + backing + " " + directory + " rw - ext4 /dev/fixture rw\n")}, nil
}

func sabnzbdConsumerLink(t *testing.T, consumer app.App) app.Link {
	t.Helper()
	for _, link := range consumer.Links() {
		if link.Target == "sabnzbd" {
			return link
		}
	}
	t.Fatal("consumer lacks SABnzbd Link")
	return app.Link{}
}

func TestSABnzbdLinksAllThreeConsumersPreserveOwnedIDsAndCategories(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		t.Run(consumer.Info().ID, func(t *testing.T) {
			id := consumer.Info().ID
			protocol := "usenet"
			if id == "lidarr" {
				protocol = "UsenetDownloadProtocol"
			}
			backend := arrtest.NewArr(id)
			backend.SeedRootFolder("/data/media/library")
			key := "fixture-full-key"
			backend.AcceptDownloadProvider = func(client map[string]any) bool {
				return client["implementation"] == "Sabnzbd" && client["protocol"] == protocol && arrtest.Field(client, "host") == "sabnzbd" && arrtest.Field(client, "port") == float64(8080) && arrtest.Field(client, "apiKey") == key
			}
			server := httptest.NewServer(backend.Handler())
			t.Cleanup(server.Close)
			fixture := newSabnzbdLinkFixture()
			target := httptest.NewServer(fixture.handler(t))
			t.Cleanup(target.Close)
			secrets := porttest.Secrets{strings.ToUpper(id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": key}
			recorder := &porttest.Recorder{}
			from := porttest.New(t, porttest.Options{App: id, Service: id, Secrets: secrets}, server, recorder)
			exec := &sabnzbdLinkExecutor{}
			from.Config.Exec = exec
			to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
			to.ContainerPort, to.Port, to.Config.Exec = 8080, 18080, exec
			link := sabnzbdConsumerLink(t, consumer)
			if err := link.Apply(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			owned := backend.DownloadClients()[0]
			if owned["protocol"] != protocol {
				t.Fatal("SABnzbd resource did not retain its consumer's native protocol")
			}
			firstID := owned["id"]
			field := map[string]string{"sonarr": "tvCategory", "radarr": "movieCategory", "lidarr": "musicCategory"}[id]
			for _, raw := range owned["fields"].([]any) {
				entry := raw.(map[string]any)
				if entry["name"] == field {
					entry["value"] = "chosen"
				}
			}
			owned["priority"] = float64(9)
			if err := link.Apply(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if len(backend.DownloadClients()) != 1 || backend.DownloadClientTests() != 3 || owned["id"] != firstID || arrtest.Field(owned, field) != "chosen" || owned["priority"] != float64(9) {
				t.Fatal("rerun changed owned ID/category/options or skipped native verification")
			}
			fixture.mu.Lock()
			fixture.key = "renewed-accepted-key"
			fixture.mu.Unlock()
			key = "renewed-accepted-key"
			secrets["SABNZBD_API_KEY"] = key
			if err := link.Apply(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if current := backend.DownloadClients()[0]; current["id"] != firstID || arrtest.Field(current, "apiKey") != key || arrtest.Field(current, field) != "chosen" {
				t.Fatal("accepted target credential update replaced its owned ID or category")
			}
			testsBeforeFailure := backend.DownloadClientTests()
			fixture.mu.Lock()
			fixture.rejected = true
			fixture.mu.Unlock()
			recorder.Steps = nil
			if err := link.Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("rejected full key was accepted", err)
			}
			if backend.DownloadClientTests() != testsBeforeFailure || recorder.Status("link-sabnzbd") != stepstatus.StepManual || !strings.Contains(recorder.Hint(), "self-host setup sabnzbd") {
				t.Fatal("authentication prerequisite ran native test or lost recovery")
			}
			if err := link.Remove(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if len(backend.DownloadClients()) != 0 {
				t.Fatal("recorded resource was not removed without target readiness")
			}
			fixture.mu.Lock()
			fixture.rejected = false
			fixture.mu.Unlock()
			if err := link.Apply(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if len(backend.DownloadClients()) != 1 || backend.DownloadClients()[0]["id"] == firstID {
				t.Fatal("reinstall reclaimed an old ID")
			}
			backend.SeedDownloadClient(map[string]any{"name": "Personal SABnzbd", "implementation": "Sabnzbd", "fields": []any{map[string]any{"name": "host", "value": "sabnzbd"}}})
			if err := link.Remove(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if clients := backend.DownloadClients(); len(clients) != 1 || clients[0]["name"] != "Personal SABnzbd" {
				t.Fatal("removal changed a same-target person-owned client")
			}
			if err := link.Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("reinstall adopted a same-target person-owned client", err)
			}
		})
	}
}

func TestSABnzbdPathResolutionAndManualPrerequisites(t *testing.T) {
	for _, scenario := range []string{"relative", "absolute", "resolved", "missing", "job-folders", "filesystem-root", "script", "sorting", "malformed", "rejected"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSabnzbdLinkFixture()
			want := "/data/usenet/complete/tv"
			manual := false
			failure := false
			switch scenario {
			case "relative":
				f.directory = "tv"
			case "absolute":
				f.directory = "/downloads/selected"
				want = f.directory
			case "resolved":
				f.root = "Downloads/complete"
				manual = true
			case "missing":
				f.missing = true
				manual = true
			case "job-folders":
				f.directory = "tv*"
				manual = true
			case "filesystem-root":
				f.directory = "/"
				manual = true
			case "script":
				f.script = "move-files.py"
				manual = true
			case "sorting":
				f.sorting = true
				manual = true
			case "malformed":
				f.malformed = true
				failure = true
			case "rejected":
				f.rejected = true
				manual = true
			}
			server := httptest.NewServer(f.handler(t))
			t.Cleanup(server.Close)
			h := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: porttest.Secrets{"SABNZBD_API_KEY": f.key}}, server, &porttest.Recorder{})
			h.Config.Exec = &sabnzbdLinkExecutor{}
			got, err := sabnzbd.CompletedDownloadPath(context.Background(), h, "tv")
			if manual {
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatal(err)
				}
				return
			}
			if failure {
				if err == nil || errors.Is(err, helpers.ErrManual) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || got != want {
				t.Fatalf("path=%q err=%v", got, err)
			}
		})
	}
}

func TestSABnzbdExistingLegacyMappingForEachConsumer(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, mappingHost := range []string{"selected-sabnzbd", "SELECTED-SABNZBD", "selected-sabnzbd.", "sabnzbd"} {
			t.Run(consumer.Info().ID+"/"+mappingHost, func(t *testing.T) {
				id := consumer.Info().ID
				backend := arrtest.NewArr(id)
				backend.SeedRootFolder("/media/library")
				backend.AcceptDownloadProvider = func(client map[string]any) bool {
					return client["implementation"] == "Sabnzbd" && arrtest.Field(client, "apiKey") == "fixture-full-key"
				}
				handler := backend.Handler()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/remotepathmapping") {
						if r.Method != "GET" || r.Header.Get("X-Api-Key") != backend.Key {
							t.Error("mapping read must be authenticated and read-only")
							w.WriteHeader(400)
							return
						}
						json.NewEncoder(w).Encode([]map[string]any{{"id": 8, "host": mappingHost, "remotePath": "/data/complete", "localPath": "/downloads/sab"}})
						return
					}
					handler.ServeHTTP(w, r)
				}))
				t.Cleanup(server.Close)
				fixture := newSabnzbdLinkFixture()
				fixture.root, fixture.directory = "/data/complete", "kept-category-folder"
				target := httptest.NewServer(fixture.handler(t))
				t.Cleanup(target.Close)
				secrets := porttest.Secrets{strings.ToUpper(id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": fixture.key}
				from := porttest.New(t, porttest.Options{App: id, Service: id, Secrets: secrets}, server, &porttest.Recorder{})
				executor := &sabnzbdLinkExecutor{downloadService: "selected-sabnzbd", consumerDownload: "/downloads/sab/kept-category-folder"}
				from.Config.Exec = executor
				to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "selected-sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
				to.ContainerPort, to.Config.Exec = 8080, executor
				err := sabnzbdConsumerLink(t, consumer).Apply(context.Background(), from, to)
				if mappingHost == "selected-sabnzbd." || mappingHost == "sabnzbd" {
					if !errors.Is(err, helpers.ErrManual) || backend.DownloadClientTests() != 0 || len(backend.DownloadClients()) != 0 || secrets[from.Key("link_sabnzbd_resource")] != "" {
						t.Fatal("nonmatching provider host reached a native test or ownership write", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(backend.DownloadClients()) != 1 || backend.DownloadClientTests() != 2 || fixture.root != "/data/complete" || fixture.directory != "kept-category-folder" {
					t.Fatal("compatible existing legacy mapping failed or target paths changed")
				}
			})
		}
	}
}

func TestSABnzbdConsumerManualBoundariesPrecedeNativeTests(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, scenario := range []string{"category", "library", "legacy", "upload-key", "missing-folders", "relative-incomplete", "relative-complete", "missing-default-category"} {
			t.Run(consumer.Info().ID+"/"+scenario, func(t *testing.T) {
				id := consumer.Info().ID
				backend := arrtest.NewArr(id)
				if scenario != "library" {
					backend.SeedRootFolder("/data/media/library")
				}
				server := httptest.NewServer(backend.Handler())
				t.Cleanup(server.Close)
				fixture := newSabnzbdLinkFixture()
				fixture.missing = scenario == "category"
				fixture.missingDefault = scenario == "missing-default-category"
				if scenario == "relative-incomplete" {
					fixture.incomplete = "Downloads/incomplete"
				}
				if scenario == "relative-complete" {
					fixture.root = "Downloads/complete"
				}
				target := httptest.NewServer(fixture.handler(t))
				t.Cleanup(target.Close)
				key := fixture.key
				if scenario == "upload-key" {
					key = "upload-only-key"
				}
				secrets := porttest.Secrets{strings.ToUpper(id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": key}
				recorder := &porttest.Recorder{}
				from := porttest.New(t, porttest.Options{App: id, Service: id, Secrets: secrets}, server, recorder)
				executor := &sabnzbdLinkExecutor{distinct: scenario == "legacy", missingPrerequisites: scenario == "missing-folders"}
				from.Config.Exec = executor
				to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
				to.ContainerPort, to.Config.Exec = 8080, executor
				if err := sabnzbdConsumerLink(t, consumer).Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("unsafe prerequisite was not Manual", err)
				}
				if backend.DownloadClientTests() != 0 || len(backend.DownloadClients()) != 0 || secrets[from.Key("link_sabnzbd_resource")] != "" || recorder.Status("link-sabnzbd") != stepstatus.StepManual || !strings.Contains(recorder.Hint(), "Setup") && !strings.Contains(recorder.Hint(), "setup") {
					t.Fatal("Manual boundary ran a native test/mutation or failed to persist recovery")
				}
				if executor.missingPrerequisites || strings.HasPrefix(scenario, "relative-") || fixture.missingDefault {
					for _, call := range fixture.calls {
						if call == "queue::" || call == "get_config:categories:" || strings.HasPrefix(call, "fullstatus:") {
							t.Fatalf("unsafe prerequisite reached a lazy-writing API: %s", call)
						}
					}
				}
			})
		}
	}
}

type sabnzbdLinkRunner struct{ system.Runner }

func (r *sabnzbdLinkRunner) Run(ctx context.Context, cmd system.Cmd) (system.Result, error) {
	for _, arg := range cmd.Args {
		if arg == "self-host-sabnzbd-read-prerequisites" {
			return system.Result{Stdout: []byte("existing-download-folders\n")}, nil
		}
	}
	for index, arg := range cmd.Args {
		if arg == "self-host-download-path" && index+1 < len(cmd.Args) {
			directory := cmd.Args[index+1]
			identity := "1:42"
			if strings.Contains(directory, "/media/") {
				identity = "1:99"
			}
			backing := "/shared/downloads"
			if identity == "1:99" {
				backing = "/shared/library"
			}
			return system.Result{Stdout: []byte(directory + "\n" + identity + "\n1 0 0:5 " + backing + " " + directory + " rw - ext4 /dev/fixture rw\n")}, nil
		}
	}
	return r.Runner.Run(ctx, cmd)
}

func TestSABnzbdProductionSetupSchedulerBothOrdersAndLateReadiness(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, order := range []string{"consumer-first", "target-first", "unready-target"} {
			t.Run(consumer.Info().ID+"/"+order, func(t *testing.T) {
				e := newEnv(t, consumer, sabnzbd.App{}, qbittorrent.App{})
				id := consumer.Info().ID
				backend := e.Sonarr
				if id == "radarr" {
					backend = e.Radarr
				}
				if id == "lidarr" {
					backend = arrtest.NewArr(id)
					backend.SeedRootFolder("/data/media/music")
					e.Serve(id, backend.Handler())
				}
				f := newSabnzbdLinkFixture()
				f.rejected = order == "unready-target"
				e.Serve("sabnzbd", f.handler(t))
				e.Deps.Runner = &sabnzbdLinkRunner{Runner: e.Deps.Runner}
				for appID, content := range map[string]string{"sabnzbd": "[misc]\napi_key = fixture-full-key\n", "lidarr": "<Config><ApiKey>key-lidarr</ApiKey></Config>"} {
					if appID == "lidarr" && id != "lidarr" {
						continue
					}
					dir := filepath.Join(e.AppDir(appID), "config")
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					name := "sabnzbd.ini"
					if appID == "lidarr" {
						name = "config.xml"
					}
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				backend.AcceptDownloadProvider = func(client map[string]any) bool {
					if client["implementation"] == "Sabnzbd" {
						return arrtest.Field(client, "apiKey") == f.key && arrtest.Field(client, "host") == "sabnzbd" && arrtest.Field(client, "port") == float64(8080)
					}
					user, password := e.QBittorrent.Credentials()
					return arrtest.Field(client, "host") == "qbittorrent" && arrtest.Field(client, "username") == user && arrtest.Field(client, "password") == password
				}
				if order == "consumer-first" {
					result := e.Run("install", id, "qbittorrent", "--yes", "--json")
					if result.Code != 0 || result.Step(id, "link-sabnzbd") != nil {
						t.Fatalf("absent target scheduled: %s", result.Out)
					}
					result = e.Run("install", "sabnzbd", "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
				} else {
					result := e.Run("install", "sabnzbd", "--yes", "--json")
					if order == "target-first" && result.Code != 0 {
						t.Fatal(result.Out)
					}
					result = e.Run("install", id, "qbittorrent", "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
					if order == "unready-target" {
						if step := result.Step(id, "link-sabnzbd"); step == nil || !strings.Contains(step["msg"].(string), "waiting for sabnzbd") {
							t.Fatalf("missing unready wait: %s", result.Out)
						}
						f.mu.Lock()
						f.rejected = false
						f.mu.Unlock()
						result = e.Run("setup", "sabnzbd", "--json")
						if result.Code != 0 {
							t.Fatal(result.Out)
						}
					}
				}
				if len(backend.DownloadClients()) != 2 || backend.DownloadClientTests() == 0 {
					t.Fatal("production Setup scheduler did not create both download clients")
				}
				var survivor any
				for _, client := range backend.DownloadClients() {
					if client["implementation"] == "QBittorrent" {
						survivor = client["id"]
					}
				}
				result := e.Run("remove", "sabnzbd", "--yes", "--json")
				if result.Code != 0 {
					t.Fatal(result.Out)
				}
				clients := backend.DownloadClients()
				if len(clients) != 1 || clients[0]["implementation"] != "QBittorrent" || clients[0]["id"] != survivor {
					t.Fatal("SAB removal changed the qBittorrent survivor")
				}
				if _, err := os.Stat(filepath.Join(e.AppDir("sabnzbd"), "config", "sabnzbd.ini")); err != nil {
					t.Fatal("target data was removed")
				}
			})
		}
	}
}
