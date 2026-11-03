package servarr_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/lidarr"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/apps/transmission"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/system"
)

type transmissionScheduledConsumer struct {
	app.App
	link app.Link
}

func (a transmissionScheduledConsumer) Links() []app.Link {
	return append(a.App.Links(), a.link)
}

type transmissionScheduledRPC struct {
	mu             sync.Mutex
	t              *testing.T
	user, password string
	unready        bool
	reads          int
}

func (f *transmissionScheduledRPC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	user, password, ok := r.BasicAuth()
	if !ok || user == "" || password == "" {
		f.t.Error("target Setup/Link sent an anonymous credential challenge")
		w.WriteHeader(401)
		return
	}
	if f.user == "" {
		f.user, f.password = user, password
	}
	if user != f.user || password != f.password {
		f.t.Error("target Setup/Link replaced the accepted fixture login")
		w.WriteHeader(401)
		return
	}
	if r.URL.Path == "/transmission/web/" {
		w.Write([]byte("controlled WebUI"))
		return
	}
	if r.URL.Path != "/transmission/rpc" || r.Method != "POST" {
		f.t.Error("unexpected target operation")
		w.WriteHeader(400)
		return
	}
	if f.unready {
		w.WriteHeader(403)
		return
	}
	if r.Header.Get("X-Transmission-Session-Id") != "fixture-session" {
		w.Header().Set("X-Transmission-Session-Id", "fixture-session")
		w.WriteHeader(409)
		return
	}
	var request struct{ Method string }
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		f.t.Error("malformed target request")
		w.WriteHeader(400)
		return
	}
	var arguments any
	switch request.Method {
	case "session-get":
		arguments = map[string]any{"version": "4.1.3", "rpc-version-semver": "6.0.1", "download-dir": "/data/torrents", "incomplete-dir-enabled": true, "incomplete-dir": "/data/torrents/incomplete"}
	case "session-stats":
		arguments = map[string]any{"downloadSpeed": 0, "uploadSpeed": 0, "torrentCount": 0, "activeTorrentCount": 0, "pausedTorrentCount": 0}
	case "torrent-get":
		arguments = map[string]any{"torrents": []any{}}
	default:
		f.t.Error("scheduler invoked a target mutation or torrent operation")
		w.WriteHeader(400)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": arguments})
}

type transmissionScheduledRunner struct{ system.Runner }

func (r transmissionScheduledRunner) Run(ctx context.Context, cmd system.Cmd) (system.Result, error) {
	for index, arg := range cmd.Args {
		if strings.Contains(arg, "rpc-authentication-required") {
			return system.Result{Stdout: []byte("true\n")}, nil
		}
		if arg == "self-host-download-path" && index+1 < len(cmd.Args) {
			directory := cmd.Args[index+1]
			identity, backing := "1:42", "/shared/torrents"
			if strings.Contains(directory, "/media/") {
				identity, backing = "1:99", "/shared/library"
			}
			return system.Result{Stdout: []byte(directory + "\n" + identity + "\n1 0 0:5 " + backing + " " + directory + " rw - ext4 /dev/fixture rw\n")}, nil
		}
	}
	return r.Runner.Run(ctx, cmd)
}

func TestTransmissionProductionSchedulerPreservesBothLidarrWireFamilies(t *testing.T) {
	for _, source := range []struct {
		consumer       app.App
		api, field     string
		category       string
		schemaCategory string
		version, wire  string
	}{
		{arr.Sonarr(), servarr.V3, "tvCategory", "tv", "tv-sonarr", "4.0.20.3014", "torrent"},
		{arr.Radarr(), servarr.V3, "movieCategory", "movies", "radarr", "6.4.4.10685", "torrent"},
		{lidarr.App{}, servarr.V1, "musicCategory", "music", "lidarr", "3.1.0.4875", "torrent"},
		{lidarr.App{}, servarr.V1, "musicCategory", "music", "lidarr", "3.1.6.5079", "TorrentDownloadProtocol"},
	} {
		for _, order := range []string{"consumer-first", "target-first", "unready-target"} {
			t.Run(source.consumer.Info().ID+"/"+source.version+"/"+order, func(t *testing.T) {
				id := source.consumer.Info().ID
				consumer := transmissionScheduledConsumer{App: source.consumer, link: servarr.TransmissionLink(source.api, source.field, source.category)}
				e := arrtest.New(t, catalog.New([]app.App{consumer, transmission.App{}}, nil))
				backend := arrtest.NewArr(id)
				backend.SeedRootFolder("/data/media/" + source.category)
				backend.FolderExists = func(string) bool { return true }
				peer := &transmissionScheduledRPC{t: t, unready: order == "unready-target"}
				e.Serve("transmission", peer)
				e.Deps.Runner = transmissionScheduledRunner{e.Deps.Runner}
				native := backend.Handler()
				e.Serve(id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == source.api+"/system/status" || r.URL.Path == source.api+"/downloadclient/schema" {
						if r.Method != "GET" || r.Header.Get("X-Api-Key") != backend.Key {
							w.WriteHeader(401)
							return
						}
						if strings.HasSuffix(r.URL.Path, "/system/status") {
							json.NewEncoder(w).Encode(map[string]any{"appName": source.consumer.Info().Name, "version": source.version})
							return
						}
						fields := []any{}
						prefix := strings.TrimSuffix(source.field, "Category")
						for name, value := range map[string]any{"host": "localhost", "port": 9091, "useSsl": false, "urlBase": "/transmission/", "username": nil, "password": nil, source.field: source.schemaCategory, prefix + "Directory": nil, prefix + "ImportedCategory": nil, "addPaused": false} {
							fields = append(fields, map[string]any{"name": name, "value": value})
						}
						json.NewEncoder(w).Encode([]map[string]any{{"implementation": "Transmission", "configContract": "TransmissionSettings", "protocol": source.wire, "fields": fields}})
						return
					}
					native.ServeHTTP(w, r)
				}))
				backend.AcceptDownloadProvider = func(item map[string]any) bool {
					peer.mu.Lock()
					defer peer.mu.Unlock()
					return item["implementation"] == "Transmission" && item["configContract"] == "TransmissionSettings" && item["protocol"] == source.wire && arrtest.Field(item, "host") == "transmission" && arrtest.Field(item, "port") == float64(9091) && arrtest.Field(item, "username") == peer.user && arrtest.Field(item, "password") == peer.password
				}
				if id == "lidarr" {
					config := filepath.Join(e.AppDir(id), "config")
					if err := os.MkdirAll(config, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(config, "config.xml"), []byte("<Config><ApiKey>key-lidarr</ApiKey></Config>"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if order == "consumer-first" {
					result := e.Run("install", id, "--yes", "--json")
					if result.Code != 0 || result.Step(id, "link-transmission") != nil || peer.reads != 0 {
						t.Fatal("absent target was scheduled", result.Out)
					}
					if result := e.Run("install", "transmission", "--yes", "--json"); result.Code != 0 {
						t.Fatal(result.Out)
					}
				} else {
					result := e.Run("install", "transmission", "--yes", "--json")
					if order == "target-first" && result.Code != 0 {
						t.Fatal(result.Out)
					}
					result = e.Run("install", id, "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
					if order == "unready-target" {
						if step := result.Step(id, "link-transmission"); step == nil || !strings.Contains(step["msg"].(string), "waiting for transmission") || backend.DownloadClientTests() != 0 {
							t.Fatal("unready target did not remain a consumer wait", result.Out)
						}
						peer.mu.Lock()
						peer.unready = false
						peer.mu.Unlock()
						if result := e.Run("setup", "transmission", "--json"); result.Code != 0 {
							t.Fatal(result.Out)
						}
					}
				}
				clients := backend.DownloadClients()
				if len(clients) != 1 || clients[0]["protocol"] != source.wire || backend.DownloadClientTests() == 0 {
					t.Fatal("actual constructor/scheduler lost the source family's wire contract")
				}
				ownedID := clients[0]["id"]
				if result := e.Run("setup", id, "--json"); result.Code != 0 || backend.DownloadClients()[0]["id"] != ownedID {
					t.Fatal("scheduler rerun replaced owned ID", result.Out)
				}
				peer.mu.Lock()
				peer.unready = true
				reads := peer.reads
				peer.mu.Unlock()
				if result := e.Run("remove", "transmission", "--yes", "--json"); result.Code != 0 || len(backend.DownloadClients()) != 0 || peer.reads != reads {
					t.Fatal("production removal contacted target or retained the owned connection", result.Out)
				}
			})
		}
	}
}
