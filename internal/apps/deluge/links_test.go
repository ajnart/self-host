package deluge_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type delugeConsumerSource struct {
	consumer          app.App
	version, protocol string
}

func delugeConsumerSources() []delugeConsumerSource {
	return []delugeConsumerSource{
		{arr.Sonarr(), "4.0.0", "torrent"},
		{arr.Radarr(), "4.0.0", "torrent"},
		{lidarr.App{}, "3.1.0.4875", "torrent"},
		{lidarr.App{}, "3.1.6.5079", "TorrentDownloadProtocol"},
	}
}

func (s delugeConsumerSource) name() string {
	return s.consumer.Info().ID + "/" + s.version
}

type delugeLinkSecrets struct {
	porttest.Secrets
	reads, writes int
}

func (s *delugeLinkSecrets) Get(key string) string {
	s.reads++
	return s.Secrets.Get(key)
}

func (s *delugeLinkSecrets) Set(key, value string) error {
	s.writes++
	return s.Secrets.Set(key, value)
}

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
	consumerSecrets, targetSecrets                    *delugeLinkSecrets
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
	schemaReads, resourceReads                        int
	schemaStatus, resourceStatus                      int
	schemaBody, resourceBody, responseProtocol        string
	nativeItems, createdItems, updatedItems           []map[string]any
}

func newDelugeLinkContract(t *testing.T, source delugeConsumerSource) *delugeLinkContract {
	t.Helper()
	consumer := source.consumer
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
	defaultCategory := id
	if id == "sonarr" {
		defaultCategory = "tv-sonarr"
	}
	prefix := strings.TrimSuffix(f.categoryField, "Category")
	prioritySuffix := strings.ToUpper(prefix[:1]) + prefix[1:] + "Priority"
	f.template = map[string]any{"implementation": "Deluge", "configContract": "DelugeSettings", "protocol": source.protocol, "priority": 1, "fields": []any{}}
	for name, value := range map[string]any{"host": "localhost", "port": 8112, "useSsl": false, "urlBase": nil, "password": "deluge", f.categoryField: defaultCategory, f.importedField: nil, "recent" + prioritySuffix: 0, "older" + prioritySuffix: 0, "downloadDirectory": nil, "completedDirectory": nil, "addPaused": false} {
		f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": name, "value": value})
	}
	f.exec = &delugeLinkPaths{remote: "/data/torrents", local: "/data/torrents"}
	f.backend.SeedRootFolder("/data/media/library")
	f.backend.AcceptDownloadProvider = func(client map[string]any) bool {
		if client["implementation"] != "Deluge" {
			return client["implementation"] == "QBittorrent" && client["configContract"] == "QBittorrentSettings" && client["protocol"] == source.protocol && arrtest.Field(client, "host") == "qbittorrent" && arrtest.Field(client, "username") == "survivor-admin" && arrtest.Field(client, "password") == "survivor-password"
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
		return !f.rejectNative && client["protocol"] == source.protocol && arrtest.Field(client, "host") == "deluge" && arrtest.Field(client, "port") == float64(8112) && (password == f.target.web.password || password == "********")
	}
	base := f.backend.Handler()
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" || r.URL.Path == "/login" {
			base.ServeHTTP(w, r)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == f.api+"/downloadclient/schema":
			f.schemaReads++
		case r.Method == "GET" && r.URL.Path == f.api+"/downloadclient":
			f.resourceReads++
		case r.Method == "POST" && r.URL.Path == f.api+"/downloadclient/test":
			f.nativeTests++
		case r.Method == "POST" && r.URL.Path == f.api+"/downloadclient":
			f.creates++
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, f.api+"/downloadclient/"):
			f.updates++
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, f.api+"/downloadclient/"):
			f.deletes++
		}
		if r.Header.Get("X-Api-Key") != f.backend.Key {
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, f.api+"/downloadclient") && (r.Method == "POST" || r.Method == "PUT") {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			var item map[string]any
			if json.Unmarshal(data, &item) != nil {
				t.Error("invalid download-client request")
				w.WriteHeader(400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			switch {
			case r.URL.Path == f.api+"/downloadclient/test":
				f.nativeItems = append(f.nativeItems, item)
			case r.Method == "POST":
				f.createdItems = append(f.createdItems, item)
			case r.Method == "PUT":
				f.updatedItems = append(f.updatedItems, item)
			}
		}
		switch r.URL.Path {
		case f.api + "/system/status":
			json.NewEncoder(w).Encode(map[string]any{"appName": consumer.Info().Name, "version": source.version})
			return
		case f.api + "/downloadclient/schema":
			if f.schemaStatus != 0 {
				w.WriteHeader(f.schemaStatus)
				return
			}
			if f.schemaBody != "" {
				w.Write([]byte(f.schemaBody))
				return
			}
			json.NewEncoder(w).Encode([]any{f.template})
			return
		case f.api + "/remotepathmapping":
			json.NewEncoder(w).Encode(f.mappings)
			return
		case f.api + "/downloadclient/test":
			if f.nativeBody != "" {
				w.Write([]byte(f.nativeBody))
				return
			}
		case f.api + "/downloadclient":
			if r.Method == "GET" {
				if f.resourceStatus != 0 {
					w.WriteHeader(f.resourceStatus)
					return
				}
				if f.resourceBody != "" {
					w.Write([]byte(f.resourceBody))
					return
				}
			}
			if r.Method == "POST" {
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
		if f.responseProtocol != "" && (r.Method == "PUT" || r.Method == "POST" && r.URL.Path == f.api+"/downloadclient") {
			response := httptest.NewRecorder()
			base.ServeHTTP(response, r)
			var item map[string]any
			if json.Unmarshal(response.Body.Bytes(), &item) != nil {
				t.Error("invalid fixture mutation response")
				w.WriteHeader(500)
				return
			}
			item["protocol"] = f.responseProtocol
			w.WriteHeader(response.Code)
			json.NewEncoder(w).Encode(item)
			return
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
	f.consumerSecrets = &delugeLinkSecrets{Secrets: f.secrets}
	f.targetSecrets = &delugeLinkSecrets{Secrets: f.secrets}
	f.from.Config.Secrets, f.to.Config.Secrets = f.consumerSecrets, f.targetSecrets
	f.from.Config.Exec, f.to.Config.Exec = f.exec, f.exec
	f.link = servarr.DelugeLink(f.api, f.categoryField, f.category)
	response, err := f.from.HTTP(context.Background(), servarr.Request(f.from, f.api, helpers.Request{Path: "/system/status", NoRedirect: true}))
	var status struct{ AppName, Version string }
	if err != nil || response.Status != 200 || response.Decode(&status) != nil || status.AppName != consumer.Info().Name || status.Version != source.version {
		t.Fatal("inconsistent source-family status fixture", err)
	}
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

func delugeJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func delugeCopyClient(t *testing.T, client map[string]any) map[string]any {
	t.Helper()
	var copy map[string]any
	if err := json.Unmarshal([]byte(delugeJSON(t, client)), &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func delugeSurvivor(f *delugeLinkContract, source delugeConsumerSource) map[string]any {
	prefix := strings.TrimSuffix(f.categoryField, "Category")
	prioritySuffix := strings.ToUpper(prefix[:1]) + prefix[1:] + "Priority"
	values := map[string]any{"host": "qbittorrent", "port": 8080, "useSsl": false, "urlBase": nil, "username": "survivor-admin", "password": "survivor-password", f.categoryField: "independent", f.importedField: nil, "recent" + prioritySuffix: 0, "older" + prioritySuffix: 0, "initialState": 0, "sequentialOrder": false, "firstAndLast": false, "contentLayout": 0}
	if source.consumer.Info().ID != "lidarr" || source.version == "3.1.6.5079" {
		values["apiKey"] = nil
	}
	if source.consumer.Info().ID == "sonarr" {
		values["addSeriesTags"], values["seedTimeType"] = false, 0
	}
	fields := []any{}
	for name, value := range values {
		fields = append(fields, map[string]any{"name": name, "value": value})
	}
	return map[string]any{"name": "independent qBittorrent", "implementation": "QBittorrent", "configContract": "QBittorrentSettings", "protocol": source.protocol, "enable": true, "priority": 4, "fields": fields}
}

type delugeLinkActivity struct {
	targetReads, targetWrites, consumerWrites, rpc, exec int
	native, creates, updates, deletes, labels            int
}

func (f *delugeLinkContract) activity() delugeLinkActivity {
	return delugeLinkActivity{f.targetSecrets.reads, f.targetSecrets.writes, f.consumerSecrets.writes, len(f.target.calls), len(f.exec.observed), f.nativeTests, f.creates, f.updates, f.deletes, f.labelAdds}
}

func TestDelugeLinksAllConsumersPreserveActualChoicesAndOwnedIdentity(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		t.Run(source.name(), func(t *testing.T) {
			f := newDelugeLinkContract(t, source)
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
			clients[0]["removeCompletedDownloads"] = false
			delugeSetField(clients[0], "addPaused", true)
			choices := delugeJSON(t, clients[0])
			writes := f.consumerSecrets.writes
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.ownership() != ownership || f.creates != 1 || f.updates != 0 || f.consumerSecrets.writes != writes || delugeJSON(t, f.backend.DownloadClients()[0]) != choices || f.labelAdds != 0 {
				t.Fatal("rerun changed ownership or actual choices")
			}
			for _, path := range []string{"deluge:/data/torrents/chosen", source.consumer.Info().ID + ":/data/torrents/completed"} {
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
			delugeSetField(clients[0], "password", "new-accepted-web-password")
			if delugeJSON(t, f.backend.DownloadClients()[0]) != delugeJSON(t, clients[0]) {
				t.Fatal("credential update changed a noncredential option")
			}
			var record map[string]any
			if json.Unmarshal([]byte(f.ownership()), &record) != nil || len(record) != 4 || record["id"] != float64(id.(int)) || record["target"] != "deluge" || record["endpoint"] != "deluge" || record["fingerprint"] == "" || f.ownership() == ownership {
				t.Fatal("credential update changed the persisted ownership format or failed to advance its fingerprint")
			}
			for _, item := range append(append(f.nativeItems, f.createdItems...), f.updatedItems...) {
				if item["protocol"] != source.protocol {
					t.Fatal("submitted protocol did not preserve the exact source schema")
				}
			}
		})
	}
}

func TestDelugeExistingSerializedOwnershipDoesNotMigrateEitherFamily(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		t.Run(source.name(), func(t *testing.T) {
			f := newDelugeLinkContract(t, source)
			client := delugeCopyClient(t, f.template)
			client["name"], client["enable"], client["priority"] = "existing managed Deluge", true, 9
			delugeSetField(client, "host", "deluge")
			delugeSetField(client, "port", float64(8112))
			delugeSetField(client, "urlBase", "")
			delugeSetField(client, "password", "accepted-web-password")
			delugeSetField(client, f.categoryField, "chosen")
			delugeSetField(client, f.importedField, "done")
			delugeSetField(client, "addPaused", true)
			f.backend.SeedDownloadClient(client)
			fingerprint := sha256.Sum256([]byte(`[{"Name":"host","Value":"deluge"},{"Name":"port","Value":8112},{"Name":"useSsl","Value":false},{"Name":"urlBase","Value":""},{"Name":"password","Value":"accepted-web-password"}]`))
			raw := fmt.Sprintf(`{"id":1,"target":"deluge","endpoint":"deluge","fingerprint":"%x"}`, fingerprint)
			f.secrets[f.from.Key("link_deluge_resource")] = raw
			before := delugeJSON(t, client)
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.ownership() != raw || f.consumerSecrets.writes != 0 || f.creates != 0 || f.updates != 0 || f.nativeTests != 1 || delugeJSON(t, f.backend.DownloadClients()[0]) != before {
				t.Fatal("existing four-field ownership or provider was migrated")
			}
		})
	}
}

func TestDelugeActualLeafPreflightFailuresNeverReachTargetNativeOrPrivateWrites(t *testing.T) {
	creationFaults := []string{"schema-sibling", "schema-null", "schema-number", "schema-bool", "schema-casing", "schema-whitespace", "schema-unknown", "schema-contract", "schema-implementation", "schema-duplicate", "schema-missing", "schema-field", "schema-credential", "schema-denied", "resource-denied", "schema-auth-false", "resource-auth-false", "schema-malformed", "resource-malformed", "missing-key", "rejected-key"}
	ownedFaults := append(slices.Clone(creationFaults), "schema-crossed", "resource-crossed", "resource-sibling", "resource-contract", "resource-field", "resource-credential")
	for _, source := range delugeConsumerSources() {
		for _, operation := range []string{"create", "apply", "remove"} {
			faults := creationFaults
			if operation != "create" {
				faults = ownedFaults
			}
			for _, fault := range faults {
				t.Run(source.name()+"/"+operation+"/"+fault, func(t *testing.T) {
					f := newDelugeLinkContract(t, source)
					if operation != "create" {
						if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
							t.Fatal(err)
						}
					}
					other := "TorrentDownloadProtocol"
					if source.protocol == other {
						other = "torrent"
					}
					switch fault {
					case "schema-crossed":
						f.template["protocol"] = other
					case "resource-crossed":
						f.backend.DownloadClients()[0]["protocol"] = other
					case "schema-sibling":
						f.template["protocol"] = "usenet"
					case "resource-sibling":
						f.backend.DownloadClients()[0]["protocol"] = "UsenetDownloadProtocol"
					case "schema-null":
						f.template["protocol"] = nil
					case "schema-number":
						f.template["protocol"] = 1
					case "schema-bool":
						f.template["protocol"] = true
					case "schema-casing":
						f.template["protocol"] = strings.ToUpper(source.protocol)
					case "schema-whitespace":
						f.template["protocol"] = source.protocol + " "
					case "schema-unknown":
						f.template["protocol"] = "unknown"
					case "schema-contract":
						f.template["configContract"] = "WrongSettings"
					case "resource-contract":
						f.backend.DownloadClients()[0]["configContract"] = "WrongSettings"
					case "schema-implementation":
						f.template["implementation"] = "WrongClient"
					case "schema-duplicate":
						f.schemaBody = delugeJSON(t, []any{f.template, f.template})
					case "schema-missing":
						f.schemaBody = "[]"
					case "schema-field":
						delugeSetField(f.template, "port", true)
					case "resource-field":
						delugeSetField(f.backend.DownloadClients()[0], "port", true)
					case "schema-credential":
						delugeSetField(f.template, "password", true)
					case "resource-credential":
						delugeSetField(f.backend.DownloadClients()[0], "password", true)
					case "schema-denied":
						f.schemaStatus = 403
					case "resource-denied":
						f.resourceStatus = 401
					case "schema-auth-false":
						f.schemaBody = `{"status":false,"error":"private-fixture-response"}`
					case "resource-auth-false":
						f.resourceBody = `{"status":false,"error":"private-fixture-response"}`
					case "schema-malformed":
						f.schemaBody = `{"private":"private-fixture-response"`
					case "resource-malformed":
						f.resourceBody = `{"private":"private-fixture-response"`
					case "missing-key":
						delete(f.secrets, f.from.Key("api_key"))
					case "rejected-key":
						f.secrets[f.from.Key("api_key")] = "rejected-key"
					}
					before, ownership := f.activity(), f.ownership()
					resources := delugeJSON(t, f.backend.DownloadClients())
					var err error
					if operation == "remove" {
						err = f.link.Remove(context.Background(), f.from, f.to)
					} else {
						err = f.link.Apply(context.Background(), f.from, f.to)
					}
					if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-fixture-response") || strings.Contains(err.Error(), f.backend.Key) {
						t.Fatal("invalid preflight did not produce safe Manual recovery", err)
					}
					if f.activity() != before || f.ownership() != ownership || delugeJSON(t, f.backend.DownloadClients()) != resources {
						t.Fatal("failed preflight reached target/native/path/resource/private writes", before, f.activity())
					}
				})
			}
		}
	}
}

func TestDelugeConsumerProtocolsMatchUpstreamWithoutNormalization(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		t.Run(source.name(), func(t *testing.T) {
			f := newDelugeLinkContract(t, source)
			if f.template["protocol"] != source.protocol {
				t.Fatal("fixture does not model the pinned upstream consumer protocol")
			}
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal("actual consumer protocol rejected", err)
			}
			client := f.backend.DownloadClients()[0]
			if client["protocol"] != source.protocol || f.nativeTests != 2 {
				t.Fatal("protocol was normalized or native verification was skipped")
			}
			owned, tests := f.ownership(), f.nativeTests
			other := "TorrentDownloadProtocol"
			if source.protocol == other {
				other = "torrent"
			}
			client["protocol"] = other
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("wrong stored protocol accepted", err)
			}
			if f.nativeTests != tests || f.updates != 0 || f.ownership() != owned {
				t.Fatal("wrong stored protocol reached native tests or changed ownership")
			}
			if source.consumer.Info().ID != "lidarr" {
				g := newDelugeLinkContract(t, source)
				g.template["protocol"] = other
				if err := g.link.Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("Lidarr-only schema protocol accepted on v3", err)
				}
				if g.nativeTests != 0 || g.creates != 0 || g.ownership() != "" {
					t.Fatal("wrong schema protocol reached native tests or resource creation")
				}
			}
		})
	}
}

func TestDelugeRejectsUnsupportedActualConsumerContracts(t *testing.T) {
	for _, scenario := range []struct {
		source delugeConsumerSource
		actual string
	}{
		{delugeConsumerSources()[0], "readarr"},
		{delugeConsumerSources()[0], "radarr"},
		{delugeConsumerSources()[2], "sonarr"},
		{delugeConsumerSources()[3], "sonarr"},
	} {
		t.Run(scenario.source.name()+"/"+scenario.actual, func(t *testing.T) {
			f := newDelugeLinkContract(t, scenario.source)
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
	for _, source := range delugeConsumerSources() {
		for _, prerequisite := range []string{"plugin", "category", "imported", "password", "daemon", "directory", "app-user", "label-path", "stored-field"} {
			t.Run(source.name()+"/"+prerequisite, func(t *testing.T) {
				f := newDelugeLinkContract(t, source)
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
				if f.nativeTests != 0 || f.creates != 0 || f.updates != 0 || f.labelAdds != 0 || f.consumerSecrets.writes != 0 || f.ownership() != "" || f.recorder.Status("link-deluge") != stepstatus.StepManual || !strings.Contains(f.recorder.Hint(), "self-host setup "+source.consumer.Info().ID) {
					t.Fatal("prerequisite was not persisted before native test/mutation", f.recorder.Steps)
				}
			})
		}
	}
}

func TestDelugeExistingLabelsRemainRequiredBeforeRerunOrCredentialTests(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		for _, stage := range []string{"create", "rerun", "credential-update"} {
			for _, label := range []string{"normal", "post-import"} {
				t.Run(source.name()+"/"+stage+"/"+label, func(t *testing.T) {
					f := newDelugeLinkContract(t, source)
					delugeSetField(f.template, f.importedField, "done")
					if stage != "create" {
						if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
							t.Fatal(err)
						}
					}
					if stage == "credential-update" {
						f.secrets["DELUGE_WEB_PASSWORD"] = "renewed-accepted-web-password"
						f.target.web.password = "renewed-accepted-web-password"
					}
					missing := f.category
					if label == "post-import" {
						missing = "done"
					}
					delete(f.target.labels, missing)
					before, ownership := f.activity(), f.ownership()
					resources, labels := delugeJSON(t, f.backend.DownloadClients()), delugeJSON(t, f.target.labels)
					if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
						t.Fatal("missing existing label was accepted", err)
					}
					after := f.activity()
					after.rpc, after.targetReads = before.rpc, before.targetReads
					if after != before || f.ownership() != ownership || delugeJSON(t, f.backend.DownloadClients()) != resources || delugeJSON(t, f.target.labels) != labels || f.target.web.rotations != 0 || f.target.web.connects != 0 || f.target.web.configWrites != 0 {
						t.Fatal("label refusal reached native tests, writes or path probes")
					}
				})
			}
		}
	}
}

func TestDelugeLinksVerifySharedAndExplicitLegacyViewsWithoutMigration(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", source.name(), legacy), func(t *testing.T) {
				f := newDelugeLinkContract(t, source)
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

func TestDelugeOfflineRemovalAndReinstallPreservePersonalClientsAndSurvivors(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		t.Run(source.name(), func(t *testing.T) {
			f := newDelugeLinkContract(t, source)
			survivor := delugeSurvivor(f, source)
			f.backend.SeedDownloadClient(survivor)
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			client := f.backend.DownloadClients()[1]
			id := client["id"]
			personal := delugeCopyClient(t, client)
			personal["name"] = "personal same-target client"
			f.backend.SeedDownloadClient(personal)
			remaining := delugeJSON(t, []any{survivor, personal})
			f.target.web.connected = false
			f.secrets["DELUGE_WEB_PASSWORD"] = ""
			f.from.Config.Exec, f.to.Config.Exec = nil, nil
			before := f.activity()
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			after := before
			after.deletes++
			after.consumerWrites++
			if f.ownership() != "" || delugeJSON(t, f.backend.DownloadClients()) != remaining || f.activity() != after {
				t.Fatal("unlink touched target or unrelated client")
			}
			reads := f.resourceReads + f.schemaReads
			f.schemaBody, f.resourceBody = "false", "false"
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil || f.activity() != after || f.resourceReads+f.schemaReads != reads {
				t.Fatal("unowned removal discovered or changed anything", err)
			}
			f.schemaBody, f.resourceBody = "", ""
			f.target.web.connected = true
			f.secrets["DELUGE_WEB_PASSWORD"] = "accepted-web-password"
			f.from.Config.Exec, f.to.Config.Exec = f.exec, f.exec
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.activity() != after {
				t.Fatal("reinstall tried to adopt the personal same-target client", err)
			}
			delugeSetField(personal, "host", "person-selected-downloader")
			remaining = delugeJSON(t, []any{survivor, personal})
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			clients := f.backend.DownloadClients()
			if len(clients) != 3 || clients[2]["id"] == id || clients[2]["protocol"] != source.protocol || f.creates != 2 || delugeJSON(t, clients[:2]) != remaining || !f.backend.AcceptDownloadProvider(clients[0]) {
				t.Fatal("reinstall reclaimed old identity")
			}
		})
	}
}

func TestDelugeUnverifiableResponsesPreserveUncertaintyAndFingerprint(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		for _, fault := range []string{"unverifiable-create", "crossed-create", "crossed-update"} {
			t.Run(source.name()+"/"+fault, func(t *testing.T) {
				f := newDelugeLinkContract(t, source)
				if fault == "crossed-update" {
					if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
						t.Fatal(err)
					}
					f.secrets["DELUGE_WEB_PASSWORD"] = "renewed-accepted-web-password"
					f.target.web.password = "renewed-accepted-web-password"
				}
				owned := f.ownership()
				if fault == "unverifiable-create" {
					f.uncertain = true
				} else {
					f.responseProtocol = "TorrentDownloadProtocol"
					if source.protocol == f.responseProtocol {
						f.responseProtocol = "torrent"
					}
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); err == nil {
					t.Fatal("unverifiable mutation response was accepted")
				}
				client := f.backend.DownloadClients()[0]
				if client["protocol"] != source.protocol || f.creates != 1 {
					t.Fatal("submitted protocol was normalized or resource was replaced")
				}
				if fault == "crossed-update" {
					if f.ownership() != owned || f.updates != 1 {
						t.Fatal("failed update response advanced ownership")
					}
					return
				}
				var intent struct{ ID int }
				if json.Unmarshal([]byte(f.ownership()), &intent) != nil || f.ownership() == "" || intent.ID != 0 {
					t.Fatal("uncertain creation acquired or discarded an owned ID")
				}
				before := f.activity()
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.activity() != before {
					t.Fatal("uncertain creation was retried or adopted", err)
				}
			})
		}
	}
}

func TestDelugeUserEditsAndUnownedClientsAreNeverAdoptedOrOverwritten(t *testing.T) {
	for _, source := range delugeConsumerSources() {
		for _, edit := range []string{"port", "password", "enable", "missing-ownership", "native", "schema"} {
			t.Run(source.name()+"/"+edit, func(t *testing.T) {
				f := newDelugeLinkContract(t, source)
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
					f.secrets[f.from.Key("link_deluge_resource")] = ""
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
	for _, source := range delugeConsumerSources() {
		for _, order := range []string{"consumer-first", "target-first", "unready-target"} {
			t.Run(source.name()+"/"+order, func(t *testing.T) {
				f := newDelugeLinkContract(t, source)
				id := source.consumer.Info().ID
				e := arrtest.New(t, catalog.New([]app.App{source.consumer, deluge.App{}}, nil))
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
				clientID := f.backend.DownloadClients()[0]["id"]
				result := e.Run("setup", id, "--json")
				if result.Code != 0 || f.creates != 1 || f.updates != 0 || f.backend.DownloadClients()[0]["id"] != clientID || f.backend.DownloadClients()[0]["protocol"] != source.protocol {
					t.Fatal("scheduler rerun changed the source-family resource", result.Out)
				}
				for _, item := range append(f.nativeItems, f.createdItems...) {
					if item["protocol"] != source.protocol {
						t.Fatal("scheduler submitted a normalized protocol")
					}
				}
				result = e.Run("remove", "deluge", "--yes", "--json")
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
