package deluge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/deluge"
	"github.com/ajnart/self-host/internal/apps/lidarr"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

type delugeLinkPaths struct {
	helpers.Executor
	distinct              bool
	remote, local, denied string
	observed              []string
}

func (e *delugeLinkPaths) Exec(_ context.Context, service string, _ map[string]string, args ...string) (system.Result, error) {
	if len(args) != 8 || strings.Join(args[:3], " ") != "s6-setuidgid abc sh" || args[6] != "self-host-download-path" {
		return system.Result{}, errors.New("unexpected non-App-user path command")
	}
	directory := args[7]
	e.observed = append(e.observed, service+":"+directory)
	if service+":"+directory == e.denied {
		return system.Result{ExitCode: 1}, nil
	}
	identity, backing := "1:42", "/fixture/downloads"
	if strings.Contains(directory, "/media/") {
		identity, backing = "1:99", "/fixture/library"
	} else {
		base := e.local
		if service == "deluge" {
			base = e.remote
		}
		if directory != base && !strings.HasPrefix(directory, base+"/") {
			return system.Result{ExitCode: 1}, nil
		}
		backing += strings.TrimPrefix(directory, base)
		if e.distinct && service != "deluge" {
			identity = "1:77"
		}
	}
	return system.Result{Stdout: []byte(directory + "\n" + identity + "\n1 0 0:5 " + backing + " " + directory + " rw - ext4 /dev/fixture rw\n")}, nil
}

type delugeLinkContract struct {
	consumer                                          app.App
	backend                                           *arrtest.Arr
	target                                            *delugeArrFixture
	from, to                                          *helpers.H
	recorder                                          *porttest.Recorder
	secrets                                           porttest.Secrets
	exec                                              *delugeLinkPaths
	link                                              app.Link
	api, categoryField, category, importedField       string
	template                                          map[string]any
	mappings                                          []map[string]any
	creates, updates, deletes, nativeTests, labelAdds int
	uncertain, rejectNative                           bool
	nativeBody                                        string
	mask                                              bool
	handler                                           http.Handler
}

func newDelugeLinkContract(t *testing.T, consumer app.App) *delugeLinkContract {
	t.Helper()
	id := consumer.Info().ID
	f := &delugeLinkContract{consumer: consumer, backend: arrtest.NewArr(id), target: newDelugeArrFixture(), api: servarr.V3, categoryField: "tvCategory", category: "tv", mappings: []map[string]any{}}
	switch id {
	case "radarr":
		f.categoryField, f.category = "movieCategory", "movies"
	case "lidarr":
		f.api, f.categoryField, f.category = servarr.V1, "musicCategory", "music"
	}
	f.importedField = strings.TrimSuffix(f.categoryField, "Category") + "ImportedCategory"
	for _, category := range []string{"movies", "music", "chosen"} {
		f.target.labels[category] = map[string]any{"apply_move_completed": false, "move_completed": false, "move_completed_path": ""}
	}
	protocol := "torrent"
	if id == "lidarr" {
		protocol = "TorrentDownloadProtocol"
	}
	f.template = map[string]any{"implementation": "Deluge", "configContract": "DelugeSettings", "protocol": protocol, "priority": 1, "fields": []any{}}
	for name, value := range map[string]any{"host": "localhost", "port": 8112, "useSsl": false, "urlBase": nil, "password": "deluge", f.categoryField: "upstream-default", f.importedField: nil, "downloadDirectory": nil, "completedDirectory": nil, "addPaused": false} {
		f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": name, "value": value})
	}
	f.exec = &delugeLinkPaths{remote: "/data/torrents", local: "/data/torrents"}
	f.backend.SeedRootFolder("/data/media/library")
	f.backend.AcceptDownloadProvider = func(client map[string]any) bool {
		if client["implementation"] != "Deluge" {
			return true
		}
		f.target.web.mu.Lock()
		defer f.target.web.mu.Unlock()
		for _, field := range []string{f.categoryField, f.importedField} {
			label, _ := arrtest.Field(client, field).(string)
			if label != "" && f.target.labels[label] == nil {
				f.labelAdds++
			}
		}
		password := arrtest.Field(client, "password")
		return !f.rejectNative && client["protocol"] == protocol && arrtest.Field(client, "host") == "deluge" && arrtest.Field(client, "port") == float64(8112) && (password == f.target.web.password || password == "********")
	}
	base := f.backend.Handler()
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" || r.URL.Path == "/login" {
			base.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != f.backend.Key {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case f.api + "/downloadclient/schema":
			json.NewEncoder(w).Encode([]any{f.template})
			return
		case f.api + "/remotepathmapping":
			json.NewEncoder(w).Encode(f.mappings)
			return
		case f.api + "/downloadclient/test":
			f.nativeTests++
			if f.nativeBody != "" {
				w.Write([]byte(f.nativeBody))
				return
			}
		case f.api + "/downloadclient":
			if r.Method == "POST" {
				f.creates++
				if f.uncertain {
					base.ServeHTTP(httptest.NewRecorder(), r)
					w.Write([]byte(`{}`))
					return
				}
			}
			if r.Method == "GET" && f.mask {
				clients := f.backend.DownloadClients()
				data, _ := json.Marshal(clients)
				clients = nil
				json.Unmarshal(data, &clients)
				for _, client := range clients {
					if client["implementation"] == "Deluge" {
						delugeSetField(client, "password", "********")
					}
				}
				json.NewEncoder(w).Encode(clients)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, f.api+"/downloadclient/") {
			if r.Method == "PUT" {
				f.updates++
			}
			if r.Method == "DELETE" {
				f.deletes++
			}
		}
		base.ServeHTTP(w, r)
	})
	server := httptest.NewServer(f.handler)
	t.Cleanup(server.Close)
	target := httptest.NewServer(f.target.handler(t))
	t.Cleanup(target.Close)
	f.secrets = porttest.Secrets{strings.ToUpper(id) + "_API_KEY": f.backend.Key, "DELUGE_WEB_PASSWORD": "accepted-web-password", "ADMIN_USER": "not-a-daemon-user", "ADMIN_PASSWORD": "not-the-web-password"}
	f.recorder = &porttest.Recorder{}
	f.from = porttest.New(t, porttest.Options{App: id, Service: id, Secrets: f.secrets}, server, f.recorder)
	f.to = porttest.New(t, porttest.Options{App: "deluge", Service: "deluge", Secrets: f.secrets}, target, &porttest.Recorder{})
	f.to.ContainerPort, f.to.Port = 8112, 18112
	f.from.Config.Exec, f.to.Config.Exec = f.exec, f.exec
	f.link = servarr.DelugeLink(f.api, f.categoryField, f.category)
	return f
}

func delugeSetField(client map[string]any, name string, value any) {
	fields, _ := client["fields"].([]any)
	for _, raw := range fields {
		field, _ := raw.(map[string]any)
		if field["name"] == name {
			field["value"] = value
			return
		}
	}
}

func (f *delugeLinkContract) ownership() string {
	return f.secrets[strings.ToUpper(f.consumer.Info().ID)+"_LINK_DELUGE_RESOURCE"]
}

func TestDelugeLinksAllConsumersPreserveActualChoicesAndOwnedIdentity(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		t.Run(consumer.Info().ID, func(t *testing.T) {
			f := newDelugeLinkContract(t, consumer)
			ctx := context.Background()
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			clients := f.backend.DownloadClients()
			if len(clients) != 1 || f.creates != 1 || f.nativeTests != 2 || f.labelAdds != 0 || arrtest.Field(clients[0], "username") != nil || arrtest.Field(clients[0], "password") != "accepted-web-password" {
				t.Fatal("incorrect provider, credentials, guards or native test", clients, f.creates, f.nativeTests, f.labelAdds)
			}
			id, ownership := clients[0]["id"], f.ownership()
			delugeSetField(clients[0], f.categoryField, "chosen")
			delugeSetField(clients[0], f.importedField, "done")
			delugeSetField(clients[0], "downloadDirectory", "/data/torrents/chosen")
			delugeSetField(clients[0], "completedDirectory", "/data/torrents/completed")
			clients[0]["priority"] = 7
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.ownership() != ownership || f.creates != 1 || f.updates != 0 || clients[0]["priority"] != 7 || f.labelAdds != 0 {
				t.Fatal("rerun changed ownership or actual choices")
			}
			for _, path := range []string{"deluge:/data/torrents/chosen", consumer.Info().ID + ":/data/torrents/completed"} {
				if !slices.Contains(f.exec.observed, path) {
					t.Fatal("stored directory was not verified", path)
				}
			}
			f.mask = true
			if err := f.link.Apply(ctx, f.from, f.to); err != nil || f.updates != 0 {
				t.Fatal("masked same-password rerun", err)
			}
			f.secrets["DELUGE_WEB_PASSWORD"] = "new-accepted-web-password"
			f.target.web.password = "new-accepted-web-password"
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.updates != 1 || f.backend.DownloadClients()[0]["id"] != id || arrtest.Field(f.backend.DownloadClients()[0], f.categoryField) != "chosen" {
				t.Fatal("password recovery replaced the recorded client")
			}
		})
	}
}

func TestDelugeConsumerProtocolsMatchUpstreamWithoutNormalization(t *testing.T) {
	for _, scenario := range []struct {
		consumer        app.App
		protocol, other string
	}{
		{arr.Sonarr(), "torrent", "TorrentDownloadProtocol"},
		{arr.Radarr(), "torrent", "TorrentDownloadProtocol"},
		{lidarr.App{}, "TorrentDownloadProtocol", "torrent"},
	} {
		t.Run(scenario.consumer.Info().ID, func(t *testing.T) {
			f := newDelugeLinkContract(t, scenario.consumer)
			if f.template["protocol"] != scenario.protocol {
				t.Fatal("fixture does not model the pinned upstream consumer protocol")
			}
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal("actual consumer protocol rejected", err)
			}
			client := f.backend.DownloadClients()[0]
			if client["protocol"] != scenario.protocol || f.nativeTests != 2 {
				t.Fatal("protocol was normalized or native verification was skipped")
			}
			owned, tests := f.ownership(), f.nativeTests
			client["protocol"] = scenario.other
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("wrong stored protocol accepted", err)
			}
			if f.nativeTests != tests || f.updates != 0 || f.ownership() != owned {
				t.Fatal("wrong stored protocol reached native tests or changed ownership")
			}
			g := newDelugeLinkContract(t, scenario.consumer)
			g.template["protocol"] = scenario.other
			if err := g.link.Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("wrong schema protocol accepted", err)
			}
			if g.nativeTests != 0 || g.creates != 0 || g.ownership() != "" {
				t.Fatal("wrong schema protocol reached native tests or resource creation")
			}
		})
	}
}

func TestDelugeRejectsUnsupportedActualConsumerContracts(t *testing.T) {
	for _, scenario := range []struct {
		consumer app.App
		actual   string
	}{
		{arr.Sonarr(), "readarr"},
		{arr.Sonarr(), "radarr"},
		{lidarr.App{}, "sonarr"},
	} {
		t.Run(scenario.consumer.Info().ID+"/"+scenario.actual, func(t *testing.T) {
			f := newDelugeLinkContract(t, scenario.consumer)
			from := *f.from
			from.App = scenario.actual
			f.secrets[from.Key("api_key")] = f.backend.Key
			if err := f.link.Apply(context.Background(), &from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("unsupported consumer API/category combination accepted", err)
			}
			if f.nativeTests != 0 || f.creates != 0 || len(f.target.calls) != 0 || f.secrets[from.Key("link_deluge_resource")] != "" {
				t.Fatal("unsupported consumer reached target reads/native tests or acquired ownership")
			}
		})
	}
}

func TestDelugeMissingLabelsAndRejectedPrerequisitesNeverReachNativeArrTests(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, prerequisite := range []string{"plugin", "category", "imported", "password", "daemon", "directory", "app-user", "label-path", "stored-field"} {
			t.Run(consumer.Info().ID+"/"+prerequisite, func(t *testing.T) {
				f := newDelugeLinkContract(t, consumer)
				switch prerequisite {
				case "plugin":
					f.target.web.coreConfig["enabled_plugins"] = []string{}
				case "category":
					delete(f.target.labels, f.category)
				case "imported":
					delugeSetField(f.template, f.importedField, "missing-after-import")
				case "password":
					f.secrets["DELUGE_WEB_PASSWORD"] = "wrong-web-password"
				case "daemon":
					f.target.web.activeDaemon = "remote-daemon"
				case "directory":
					f.exec.denied = "deluge:/data/torrents"
				case "app-user":
					f.to.Config.Exec = nil
				case "label-path":
					f.target.labels[f.category]["apply_move_completed"] = true
					f.target.labels[f.category]["move_completed"] = true
					f.target.labels[f.category]["move_completed_path"] = "/unmounted/label"
				case "stored-field":
					delugeSetField(f.template, "completedDirectory", true)
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal(err)
				}
				if f.nativeTests != 0 || f.creates != 0 || f.updates != 0 || f.labelAdds != 0 || f.ownership() != "" || f.recorder.Status("link-deluge") != stepstatus.StepManual || !strings.Contains(f.recorder.Hint(), "self-host setup "+consumer.Info().ID) {
					t.Fatal("prerequisite was not persisted before native test/mutation", f.recorder.Steps)
				}
			})
		}
	}
}

func TestDelugeLinksVerifySharedAndExplicitLegacyViewsWithoutMigration(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", consumer.Info().ID, legacy), func(t *testing.T) {
				f := newDelugeLinkContract(t, consumer)
				if legacy {
					f.target.web.coreConfig["download_location"] = "/downloads"
					f.exec.remote, f.exec.local = "/downloads", "/legacy/deluge"
					f.mappings = []map[string]any{{"id": 1, "host": "DELUGE", "remotePath": "/downloads", "localPath": "/legacy/deluge"}}
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				id := f.backend.DownloadClients()[0]["id"]
				tests := f.nativeTests
				f.exec.distinct = true
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("incompatible path view accepted", err)
				}
				if f.nativeTests != tests || f.backend.DownloadClients()[0]["id"] != id || f.updates != 0 {
					t.Fatal("path mismatch reached native test or changed connection")
				}
			})
		}
	}
}

func TestDelugeOwnershipUncertaintyAndRemovalRemainIndependentOfTargetPrerequisites(t *testing.T) {
	f := newDelugeLinkContract(t, arr.Sonarr())
	f.uncertain = true
	if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 1 {
		t.Fatal("uncertain creation duplicated", err)
	}
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		t.Run(consumer.Info().ID, func(t *testing.T) {
			f := newDelugeLinkContract(t, consumer)
			f.backend.SeedDownloadClient(map[string]any{"name": "independent qBittorrent", "implementation": "QBittorrent", "enable": true, "fields": []any{map[string]any{"name": "host", "value": "qbittorrent"}}})
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			id := f.backend.DownloadClients()[1]["id"]
			f.target.web.connected = false
			f.secrets["DELUGE_WEB_PASSWORD"] = ""
			calls, native := len(f.target.calls), f.nativeTests
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.ownership() != "" || len(f.backend.DownloadClients()) != 1 || f.backend.DownloadClients()[0]["name"] != "independent qBittorrent" || len(f.target.calls) != calls || f.nativeTests != native || f.deletes != 1 {
				t.Fatal("unlink touched target or unrelated client")
			}
			f.target.web.connected = true
			f.secrets["DELUGE_WEB_PASSWORD"] = "accepted-web-password"
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.backend.DownloadClients()[1]["id"] == id || f.creates != 2 {
				t.Fatal("reinstall reclaimed old identity")
			}
		})
	}
}

func TestDelugeUserEditsAndUnownedClientsAreNeverAdoptedOrOverwritten(t *testing.T) {
	for _, edit := range []string{"port", "password", "enable", "missing-ownership", "native", "schema"} {
		t.Run(edit, func(t *testing.T) {
			f := newDelugeLinkContract(t, arr.Sonarr())
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			client := f.backend.DownloadClients()[0]
			switch edit {
			case "port":
				delugeSetField(client, "port", float64(18000))
			case "password":
				delugeSetField(client, "password", "person-edited-password")
			case "enable":
				client["enable"] = false
			case "missing-ownership":
				f.secrets["SONARR_LINK_DELUGE_RESOURCE"] = ""
			case "native":
				f.rejectNative = true
			case "schema":
				f.template["configContract"] = "WrongSettings"
			}
			owned := f.ownership()
			if err := f.link.Apply(context.Background(), f.from, f.to); err == nil {
				t.Fatal("edited/rejected/unowned client accepted")
			}
			if f.creates != 1 || f.updates != 0 || f.ownership() != owned {
				t.Fatal("user edit or failure changed ownership")
			}
			if edit == "missing-ownership" {
				if err := f.link.Remove(context.Background(), f.from, f.to); err != nil || f.deletes != 0 {
					t.Fatal("unowned client removed", err)
				}
			}
		})
	}
}

type delugeScheduledConsumer struct {
	app.App
	delugeLink app.Link
}

func (a delugeScheduledConsumer) Links() []app.Link {
	return append(a.App.Links(), a.delugeLink)
}

type delugeScheduledRunner struct {
	system.Runner
	paths *delugeLinkPaths
}

func (r delugeScheduledRunner) Run(ctx context.Context, cmd system.Cmd) (system.Result, error) {
	for index, arg := range cmd.Args {
		if arg == "self-host-download-path" && index >= 7 {
			return r.paths.Exec(ctx, cmd.Args[index-7], nil, cmd.Args[index-6:]...)
		}
	}
	return r.Runner.Run(ctx, cmd)
}

func TestDelugeProductionSchedulerBothOrdersUnreadyAndOwnedRemoval(t *testing.T) {
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, order := range []string{"consumer-first", "target-first", "unready-target"} {
			t.Run(consumer.Info().ID+"/"+order, func(t *testing.T) {
				f := newDelugeLinkContract(t, consumer)
				id := consumer.Info().ID
				assembled := delugeScheduledConsumer{App: consumer, delugeLink: f.link}
				e := arrtest.New(t, catalog.New([]app.App{assembled, deluge.App{}}, nil))
				e.Serve(id, f.handler)
				f.target.allowSetup = true
				f.target.web.password = "deluge"
				if order == "unready-target" {
					f.target.web.password = "changed-outside-setup"
				}
				e.Serve("deluge", f.target.handler(t))
				e.Deps.Runner = delugeScheduledRunner{Runner: e.Deps.Runner, paths: f.exec}
				if err := os.MkdirAll(filepath.Join(e.AppDir("deluge"), "config"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.AppDir("deluge"), "config", "kept-data"), []byte("owned-test-data"), 0600); err != nil {
					t.Fatal(err)
				}
				if id == "lidarr" {
					directory := filepath.Join(e.AppDir(id), "config")
					if err := os.MkdirAll(directory, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(directory, "config.xml"), []byte("<Config><ApiKey>key-lidarr</ApiKey></Config>"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if order == "consumer-first" {
					result := e.Run("install", id, "--yes", "--json")
					if result.Code != 0 || result.Step(id, "link-deluge") != nil {
						t.Fatal("absent target scheduled", result.Out)
					}
					result = e.Run("install", "deluge", "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
				} else {
					result := e.Run("install", "deluge", "--yes", "--json")
					if order == "target-first" && result.Code != 0 {
						t.Fatal(result.Out)
					}
					result = e.Run("install", id, "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
					if order == "unready-target" {
						step := result.Step(id, "link-deluge")
						if step == nil || !strings.Contains(step["msg"].(string), "waiting for deluge") {
							t.Fatal("missing target readiness wait", result.Out)
						}
						f.target.web.mu.Lock()
						f.target.web.password = "deluge"
						f.target.web.mu.Unlock()
						result = e.Run("setup", "deluge", "--json")
						if result.Code != 0 {
							t.Fatal(result.Out)
						}
					}
				}
				if len(f.backend.DownloadClients()) != 1 || f.nativeTests == 0 || f.labelAdds != 0 {
					t.Fatal("production scheduler did not verify the Deluge leaf")
				}
				result := e.Run("remove", "deluge", "--yes", "--json")
				if result.Code != 0 || len(f.backend.DownloadClients()) != 0 {
					t.Fatal("production unlink failed", result.Out)
				}
				if _, err := os.Stat(filepath.Join(e.AppDir("deluge"), "config", "kept-data")); err != nil {
					t.Fatal("non-purge removal lost target data", err)
				}
			})
		}
	}
}
