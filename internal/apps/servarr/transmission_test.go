package servarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

type transmissionRPCFixture struct {
	mu                               sync.Mutex
	t                                *testing.T
	config                           map[string]any
	user, password, location, body   string
	status, conflicts, authenticated int
	missingSession, malformedQueue   bool
	methods                          []string
}

func (f *transmissionRPCFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, password, ok := r.BasicAuth()
	if !ok || user != f.user || password != f.password {
		f.t.Error("RPC did not receive accepted Basic credentials before session negotiation")
		w.WriteHeader(401)
		return
	}
	f.authenticated++
	if r.Method != "POST" || r.URL.Path != "/transmission/rpc" {
		f.t.Error("unexpected RPC request")
		w.WriteHeader(400)
		return
	}
	if f.body != "" {
		w.Write([]byte(f.body))
		return
	}
	if f.status != 0 {
		w.Header().Set("Location", f.location)
		w.WriteHeader(f.status)
		w.Write([]byte("private-response-marker"))
		return
	}
	if r.Header.Get("X-Transmission-Session-Id") != "accepted-session" || f.conflicts > 0 {
		if f.conflicts > 0 {
			f.conflicts--
		}
		if !f.missingSession {
			w.Header().Set("X-Transmission-Session-Id", "accepted-session")
		}
		w.WriteHeader(409)
		return
	}
	var request struct {
		Method string `json:"method"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		f.t.Error("invalid RPC envelope")
		w.WriteHeader(400)
		return
	}
	f.methods = append(f.methods, request.Method)
	var arguments any
	switch request.Method {
	case "session-get":
		arguments = f.config
	case "session-stats":
		arguments = map[string]any{"downloadSpeed": 0, "uploadSpeed": 0, "torrentCount": 0, "activeTorrentCount": 0, "pausedTorrentCount": 0}
	case "torrent-get":
		arguments = map[string]any{"torrents": []any{}}
		if f.malformedQueue {
			arguments = map[string]any{"torrents": nil}
		}
	default:
		f.t.Error("Link attempted a target mutation or unknown RPC method")
		w.WriteHeader(400)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": arguments})
}

type transmissionLinkExecutor struct {
	*servarrPathExecutor
	policy string
}

func (e *transmissionLinkExecutor) Exec(ctx context.Context, service string, env map[string]string, args ...string) (system.Result, error) {
	if len(args) == 3 && args[0] == "sh" && args[1] == "-c" && strings.Contains(args[2], "rpc-authentication-required") {
		return system.Result{Stdout: []byte(e.policy)}, nil
	}
	return e.servarrPathExecutor.Exec(ctx, service, env, args...)
}

type transmissionLinkFixture struct {
	*servarrContractFixture
	rpc      *transmissionRPCFixture
	exec     *transmissionLinkExecutor
	link     app.Link
	category string
	field    string
}

func newTransmissionLinkFixture(t *testing.T, consumer string, legacy bool) *transmissionLinkFixture {
	t.Helper()
	api, protocol, field, category := V3, "torrent", "tvCategory", "tv"
	if consumer == "radarr" {
		field, category = "movieCategory", "movies"
	}
	if consumer == "lidarr" {
		api, protocol, field, category = V1, "TorrentDownloadProtocol", "musicCategory", "music"
	}
	f := newServarrContract(t, api)
	f.guards = 1
	f.from.App, f.from.Service = consumer, consumer
	f.secrets[strings.ToUpper(consumer)+"_API_KEY"] = "consumer-key"
	f.secrets["ADMIN_USER"], f.secrets["ADMIN_PASSWORD"] = "fixture-user", "fixture-password"
	f.template = provider{"implementation": "Transmission", "configContract": "TransmissionSettings", "protocol": protocol, "fields": []any{}, "priority": float64(1), "removeCompletedDownloads": false}
	for name, value := range map[string]any{"host": "localhost", "port": float64(9091), "useSsl": false, "urlBase": "/transmission/", "username": "", "password": "", field: category, strings.TrimSuffix(field, "Category") + "Directory": nil, strings.TrimSuffix(field, "Category") + "ImportedCategory": "", "addPaused": false} {
		f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": name, "value": value})
	}
	root, local, library := "/data/torrents", "/data/torrents", "/data/media/library"
	if legacy {
		root, local, library = "/downloads/complete", "/legacy-transmission/complete", "/media/library"
	}
	rpc := &transmissionRPCFixture{t: t, user: "fixture-user", password: "fixture-password", config: map[string]any{"version": "4.1.3", "rpc-version-semver": "6.0.1", "download-dir": root, "incomplete-dir-enabled": true, "incomplete-dir": root + "/incomplete"}}
	target := httptest.NewServer(rpc)
	t.Cleanup(target.Close)
	exec := &transmissionLinkExecutor{policy: "true", servarrPathExecutor: &servarrPathExecutor{paths: map[string]string{}, backing: map[string]string{}}}
	for index, suffix := range []string{"", "/" + category, "/incomplete", "/chosen", "/override"} {
		for service, directory := range map[string]string{"selected-transmission": root + suffix, consumer: local + suffix} {
			key := service + ":" + directory
			exec.paths[key] = directory + "\n1:" + strconv.Itoa(42+index) + "\n"
			exec.backing[key] = "/shared/torrents" + suffix
		}
	}
	exec.paths[consumer+":"+library] = library + "\n1:99\n"
	f.from.Config.Exec = exec
	to := porttest.New(t, porttest.Options{App: "transmission", Service: "selected-transmission", Secrets: f.secrets}, target, &porttest.Recorder{})
	to.ContainerPort, to.Port, to.Config.Exec = 9091, 19091, exec
	f.to = to
	paths := http.NewServeMux()
	paths.HandleFunc("GET "+api+"/rootfolder", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			t.Error("library read omitted consumer API authentication")
		}
		json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "path": library}})
	})
	paths.HandleFunc("GET "+api+"/remotepathmapping", func(w http.ResponseWriter, r *http.Request) {
		mappings := []map[string]any{}
		if legacy {
			mappings = append(mappings, map[string]any{"id": 1, "host": "selected-transmission", "remotePath": root, "localPath": local})
		}
		json.NewEncoder(w).Encode(mappings)
	})
	pathServer := httptest.NewServer(paths)
	t.Cleanup(pathServer.Close)
	f.from.Config.HTTP = &http.Client{Transport: transmissionPathTransport{base: http.DefaultTransport, paths: pathServer.URL}}
	return &transmissionLinkFixture{servarrContractFixture: f, rpc: rpc, exec: exec, link: TransmissionLink(api, field, category), category: category, field: field}
}

type transmissionPathTransport struct {
	base  http.RoundTripper
	paths string
}

func (t transmissionPathTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/rootfolder") || strings.HasSuffix(r.URL.Path, "/remotepathmapping") {
		copy := r.Clone(r.Context())
		address := *r.URL
		address.Host = strings.TrimPrefix(t.paths, "http://")
		copy.URL = &address
		return t.base.RoundTrip(copy)
	}
	return t.base.RoundTrip(r)
}

func TestTransmissionLinksAllConsumersVerifyPathsAndPreserveChoices(t *testing.T) {
	for _, consumer := range []string{"sonarr", "radarr", "lidarr"} {
		for _, legacy := range []bool{false, true} {
			t.Run(consumer+map[bool]string{false: "/shared", true: "/legacy"}[legacy], func(t *testing.T) {
				f := newTransmissionLinkFixture(t, consumer, legacy)
				if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				key := f.from.Key("link_transmission_resource")
				record := f.secrets[key]
				item := f.items[0]
				if item.field("host") != "selected-transmission" || item.field("port") != float64(9091) || item.field("username") != "fixture-user" || item.field("password") != "fixture-password" {
					t.Fatal("connection did not preserve target identity, credentials or container port")
				}
				item.setField(f.field, "chosen")
				item["priority"] = float64(9)
				item.setField("addPaused", true)
				if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if f.creates != 1 || f.updates != 0 || f.tests != 3 || f.secrets[key] != record || item.field(f.field) != "chosen" || item["priority"] != float64(9) || item.field("addPaused") != true {
					t.Fatal("rerun changed owned ID, category, priority or paused choice")
				}
				item.setField("password", "********")
				if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if f.tests != 4 || f.updates != 0 {
					t.Fatal("masked stored resource was not tested in place")
				}
				f.rpc.mu.Lock()
				f.rpc.password = "new-accepted-password"
				f.rpc.mu.Unlock()
				f.secrets["ADMIN_PASSWORD"] = "new-accepted-password"
				if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if f.updates != 1 || f.items[0].id() != 17 || f.items[0].field(f.field) != "chosen" {
					t.Fatal("accepted target credential recovery replaced resource or category")
				}
				f.rpc.mu.Lock()
				if f.rpc.authenticated == 0 || len(f.rpc.methods) == 0 {
					t.Error("authenticated session and queue reads did not run")
				}
				f.rpc.mu.Unlock()
			})
		}
	}
}

func TestTransmissionDirectoryChoicesUseNativeConsumerValidation(t *testing.T) {
	for _, consumer := range []string{"sonarr", "radarr", "lidarr"} {
		f := newTransmissionLinkFixture(t, consumer, false)
		if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
			t.Fatal(err)
		}
		directoryField := strings.TrimSuffix(f.field, "Category") + "Directory"
		f.items[0].setField(directoryField, "/data/torrents/override")
		delete(f.exec.paths, "selected-transmission:/data/torrents")
		delete(f.exec.paths, "selected-transmission:/data/torrents/"+f.category)
		f.nativeStatus, f.nativeBody = 400, `[{"propertyName":"Category","errorMessage":"Cannot use Category and Directory"}]`
		if err := f.link.Apply(t.Context(), f.from, f.to); err == nil {
			t.Fatal("stable consumer's native category/directory rejection was ignored")
		}
		f.nativeStatus, f.nativeBody = 200, `{}`
		if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
			t.Fatal("leaf imposed a universal category/directory prohibition", err)
		}
		if f.items[0].field(directoryField) != "/data/torrents/override" || f.creates != 1 || f.updates != 0 {
			t.Fatal("person's explicit directory was overwritten")
		}
	}
}

func TestTransmissionUsesAcceptedTargetCredentials(t *testing.T) {
	f := newTransmissionLinkFixture(t, "sonarr", false)
	f.to.Config.Secrets = porttest.Secrets{"ADMIN_USER": "fixture-user", "ADMIN_PASSWORD": "fixture-password"}
	f.secrets["ADMIN_USER"], f.secrets["ADMIN_PASSWORD"] = "consumer-user", "consumer-password"
	if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
		t.Fatal(err)
	}
	if f.items[0].field("username") != "fixture-user" || f.items[0].field("password") != "fixture-password" || f.from.AdminPassword() != "consumer-password" {
		t.Fatal("consumer credentials replaced the accepted target identity")
	}
}

func TestTransmissionPrerequisitesStopBeforeNativeTestOrOwnership(t *testing.T) {
	for _, scenario := range []string{"policy", "missing-login", "401", "403", "421", "session", "expired-session", "queue", "version", "malformed-rpc", "failed-rpc", "incomplete-policy", "incomplete-path", "legacy-mismatch", "missing-category-directory", "category", "schema", "directory-type", "directory-missing", "root-missing", "root-traversal"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransmissionLinkFixture(t, "sonarr", false)
			switch scenario {
			case "policy":
				f.exec.policy = "false"
			case "missing-login":
				delete(f.secrets, "ADMIN_PASSWORD")
			case "401":
				f.rpc.status = 401
			case "403":
				f.rpc.status = 403
			case "421":
				f.rpc.status = 421
			case "session":
				f.rpc.missingSession = true
			case "expired-session":
				f.rpc.conflicts = 9
			case "queue":
				f.rpc.malformedQueue = true
			case "version":
				f.rpc.config["version"] = "3.0.0"
			case "malformed-rpc":
				f.rpc.body = `{"private-response-marker"`
			case "failed-rpc":
				f.rpc.body = `{"result":"private-response-marker","arguments":{}}`
			case "incomplete-policy":
				delete(f.rpc.config, "incomplete-dir-enabled")
			case "incomplete-path":
				f.rpc.config["incomplete-dir"] = "/downloads/incomplete"
			case "legacy-mismatch":
				f.exec.paths["sonarr:/data/torrents"] = "/data/torrents\n1:77\n"
			case "missing-category-directory":
				delete(f.exec.paths, "selected-transmission:/data/torrents/tv")
			case "category":
				f.link = TransmissionLink(V3, "tvCategory", "../unsafe")
			case "schema":
				f.template["configContract"] = "UnrelatedSettings"
			case "directory-type":
				f.template.setField("tvDirectory", true)
			case "directory-missing":
				fields := f.template["fields"].([]any)
				for index, raw := range fields {
					if raw.(map[string]any)["name"] == "tvDirectory" {
						f.template["fields"] = append(fields[:index], fields[index+1:]...)
						break
					}
				}
			case "root-missing":
				delete(f.rpc.config, "download-dir")
			case "root-traversal":
				f.rpc.config["download-dir"] = "/data/../private"
			}
			if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-response-marker") {
				t.Fatal("prerequisite did not fail as private Manual", err)
			}
			if f.tests != 0 || f.creates != 0 || f.updates != 0 || f.secrets[f.from.Key("link_transmission_resource")] != "" || f.recorder.Status("link-transmission") != stepstatus.StepManual || f.recorder.Hint() == "" {
				t.Fatal("prerequisite mutated ownership, ran native test or lost recovery")
			}
			if scenario == "expired-session" && f.rpc.authenticated != 3 {
				t.Fatal("session negotiation was not bounded")
			}
		})
	}
}

func TestTransmissionDisabledIncompleteFolderAndExpiredSessionRecoverReadOnly(t *testing.T) {
	f := newTransmissionLinkFixture(t, "sonarr", false)
	f.rpc.config["incomplete-dir-enabled"] = false
	delete(f.rpc.config, "incomplete-dir")
	f.rpc.conflicts = 2
	delete(f.exec.paths, "selected-transmission:/data/torrents/incomplete")
	delete(f.exec.paths, "sonarr:/data/torrents/incomplete")
	before, err := json.Marshal(f.rpc.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
		t.Fatal("bounded session refresh or disabled incomplete folder was rejected", err)
	}
	f.rpc.mu.Lock()
	after, err := json.Marshal(f.rpc.config)
	f.rpc.mu.Unlock()
	if err != nil || string(before) != string(after) || f.creates != 1 {
		t.Fatal("Link changed target configuration", err)
	}
}

func TestTransmissionRedirectIsNotFollowed(t *testing.T) {
	f := newTransmissionLinkFixture(t, "sonarr", false)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("RPC followed a redirect") }))
	t.Cleanup(sink.Close)
	f.rpc.status, f.rpc.location = 302, sink.URL
	if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.tests != 0 || f.creates != 0 {
		t.Fatal("redirect reached native test or creation", err)
	}
}

func TestTransmissionOwnershipRecoveryAndRemovalDoNotNeedTarget(t *testing.T) {
	for _, scenario := range []string{"uncertain", "unowned", "user-edit", "unready-remove"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransmissionLinkFixture(t, "sonarr", false)
			key := f.from.Key("link_transmission_resource")
			if scenario == "uncertain" {
				f.uncertain = true
			}
			err := f.link.Apply(t.Context(), f.from, f.to)
			if scenario == "uncertain" {
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatal(err)
				}
				if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 1 {
					t.Fatal("uncertain creation was retried", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			record := f.secrets[key]
			if scenario == "user-edit" {
				f.items[0].setField("password", "person-owned-password")
				if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.updates != 0 || f.secrets[key] != record {
					t.Fatal("edited credential was overwritten", err)
				}
				return
			}
			if scenario == "unowned" {
				delete(f.secrets, key)
				if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 1 {
					t.Fatal("user-owned collision was adopted", err)
				}
			}
			delete(f.secrets, "ADMIN_PASSWORD")
			f.exec.policy, f.rpc.status = "false", 403
			if err := f.link.Remove(t.Context(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if scenario == "unowned" {
				if f.deletes != 0 || len(f.items) != 1 {
					t.Fatal("unowned entry was removed")
				}
			} else if f.deletes != 1 || len(f.items) != 0 || f.secrets[key] != "" {
				t.Fatal("owned cleanup depended on target credentials or paths")
			}
		})
	}
}
