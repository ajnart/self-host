package arr_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/lidarr"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
)

type sabnzbdConsumerSource struct {
	consumer                                       app.App
	id, api, field, category, version              string
	protocol, torrentProtocol, otherUsenetProtocol string
}

func sabnzbdConsumerSources() []sabnzbdConsumerSource {
	return []sabnzbdConsumerSource{
		{arr.Sonarr(), "sonarr", servarr.V3, "tvCategory", "tv", "4.0.0", "usenet", "torrent", "UsenetDownloadProtocol"},
		{arr.Radarr(), "radarr", servarr.V3, "movieCategory", "movies", "4.0.0", "usenet", "torrent", "UsenetDownloadProtocol"},
		{lidarr.App{}, "lidarr", servarr.V1, "musicCategory", "music", "3.1.0.4875", "usenet", "torrent", "UsenetDownloadProtocol"},
		{lidarr.App{}, "lidarr", servarr.V1, "musicCategory", "music", "3.1.6.5079", "UsenetDownloadProtocol", "TorrentDownloadProtocol", "usenet"},
	}
}

func (s sabnzbdConsumerSource) name() string { return s.id + "/" + s.version }

func (s sabnzbdConsumerSource) schemas() []map[string]any {
	torrentCategory := s.id
	if s.id == "sonarr" {
		torrentCategory = "tv-sonarr"
	}
	fields := func(values map[string]any) []any {
		result := []any{}
		for name, value := range values {
			result = append(result, map[string]any{"name": name, "value": value})
		}
		return result
	}
	return []map[string]any{
		{"implementation": "Sabnzbd", "configContract": "SabnzbdSettings", "protocol": s.protocol, "priority": 1, "enable": false,
			"fields": fields(map[string]any{"host": "localhost", "port": 8080, "useSsl": false, "urlBase": nil, "apiKey": nil, "username": nil, "password": nil, s.field: s.category})},
		{"implementation": "QBittorrent", "configContract": "QBittorrentSettings", "protocol": s.torrentProtocol, "priority": 1, "enable": false,
			"fields": fields(map[string]any{"host": "localhost", "port": 8080, "useSsl": false, "username": nil, "password": nil, s.field: torrentCategory})},
	}
}

func sabnzbdSourceHandler(t *testing.T, backend *arrtest.Arr, source sabnzbdConsumerSource) http.Handler {
	t.Helper()
	handler := backend.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && (r.URL.Path == source.api+"/system/status" || r.URL.Path == source.api+"/downloadclient/schema") {
			if r.Header.Get("X-Api-Key") != backend.Key {
				w.WriteHeader(401)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/system/status") {
				name := source.id
				if name == "lidarr" {
					name = "Lidarr"
				}
				json.NewEncoder(w).Encode(map[string]any{"appName": name, "version": source.version})
				return
			}
			json.NewEncoder(w).Encode(source.schemas())
			return
		}
		handler.ServeHTTP(w, r)
	})
}

type sabnzbdTrackedSecrets struct {
	values        porttest.Secrets
	reads, writes atomic.Int64
}

func (s *sabnzbdTrackedSecrets) Get(key string) string {
	s.reads.Add(1)
	return s.values.Get(key)
}

func (s *sabnzbdTrackedSecrets) Set(key, value string) error {
	s.writes.Add(1)
	return s.values.Set(key, value)
}

func TestSABnzbdRejectsCrossedSchemaAndOwnedProtocolBeforePeerRPC(t *testing.T) {
	for _, source := range sabnzbdConsumerSources() {
		for _, operation := range []string{"create", "apply", "remove"} {
			for _, scenario := range []string{"schema-crossed", "resource-crossed", "schema-torrent", "schema-auth-false", "resource-malformed", "consumer-offline"} {
				if operation == "create" && (scenario == "resource-crossed" || scenario == "schema-crossed" && source.id == "lidarr") {
					continue
				}
				t.Run(source.name()+"/"+operation+"/"+scenario, func(t *testing.T) {
					backend := arrtest.NewArr(source.id)
					backend.SeedRootFolder("/data/media/library")
					handler := sabnzbdSourceHandler(t, backend, source)
					broken := false
					var writes atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != "GET" {
							writes.Add(1)
						}
						if broken {
							switch {
							case scenario == "consumer-offline":
								w.WriteHeader(503)
								return
							case scenario == "schema-auth-false" && strings.HasSuffix(r.URL.Path, "/schema"):
								w.Write([]byte(`{"status":false,"error":"private-consumer-data"}`))
								return
							case scenario == "resource-malformed" && r.URL.Path == source.api+"/downloadclient":
								w.Write([]byte(`{"private":"private-consumer-data"`))
								return
							case strings.HasSuffix(r.URL.Path, "/schema") && (scenario == "schema-crossed" || scenario == "schema-torrent"):
								schemas := source.schemas()
								schemas[0]["protocol"] = source.otherUsenetProtocol
								if scenario == "schema-torrent" {
									schemas[0]["protocol"] = source.torrentProtocol
								}
								json.NewEncoder(w).Encode(schemas)
								return
							}
						}
						handler.ServeHTTP(w, r)
					}))
					t.Cleanup(server.Close)
					fixture := newSabnzbdLinkFixture()
					target := httptest.NewServer(fixture.handler(t))
					t.Cleanup(target.Close)
					secrets := porttest.Secrets{strings.ToUpper(source.id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": fixture.key}
					consumerSecrets := &sabnzbdTrackedSecrets{values: secrets}
					targetSecrets := &sabnzbdTrackedSecrets{values: secrets}
					from := porttest.New(t, porttest.Options{App: source.id, Service: source.id, Secrets: secrets}, server, &porttest.Recorder{})
					executor := &sabnzbdLinkExecutor{}
					from.Config.Exec, from.Config.Secrets = executor, consumerSecrets
					to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
					to.ContainerPort, to.Config.Exec, to.Config.Secrets = 8080, executor, targetSecrets
					link := sabnzbdConsumerLink(t, source.consumer)
					if operation != "create" {
						if err := link.Apply(context.Background(), from, to); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "resource-crossed" {
						backend.DownloadClients()[0]["protocol"] = source.otherUsenetProtocol
					}
					broken = true
					fixture.mu.Lock()
					fixture.calls = nil
					fixture.mu.Unlock()
					consumerSecrets.writes.Store(0)
					targetSecrets.reads.Store(0)
					writes.Store(0)
					executor.calls.Store(0)
					record := secrets[from.Key("link_sabnzbd_resource")]
					clients, nativeTests := len(backend.DownloadClients()), backend.DownloadClientTests()
					var err error
					if operation == "remove" {
						err = link.Remove(context.Background(), from, to)
					} else {
						err = link.Apply(context.Background(), from, to)
					}
					if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-consumer-data") {
						t.Fatal("invalid consumer contract did not return safe Manual recovery", err)
					}
					fixture.mu.Lock()
					peerCalls := len(fixture.calls)
					fixture.mu.Unlock()
					if peerCalls != 0 || executor.calls.Load() != 0 || targetSecrets.reads.Load() != 0 || targetSecrets.writes.Load() != 0 || consumerSecrets.writes.Load() != 0 || writes.Load() != 0 || backend.DownloadClientTests() != nativeTests || len(backend.DownloadClients()) != clients || secrets[from.Key("link_sabnzbd_resource")] != record {
						t.Fatal("invalid consumer contract reached peer credentials/API/exec/native/resource/ownership activity")
					}
				})
			}
		}
	}
}

func TestSABnzbdUnsupportedConsumerContractStopsBeforePeerRPC(t *testing.T) {
	for _, consumer := range []struct{ id, api, field string }{
		{"sonarr", servarr.V1, "tvCategory"},
		{"radarr", servarr.V1, "movieCategory"},
		{"lidarr", servarr.V3, "musicCategory"},
		{"unknown", servarr.V3, "tvCategory"},
		{"sonarr", servarr.V3, "movieCategory"},
		{"radarr", servarr.V3, "tvCategory"},
		{"lidarr", servarr.V1, "tvCategory"},
		{"lidarr", "/api/v2", "musicCategory"},
	} {
		t.Run(consumer.id+consumer.api+"/"+consumer.field, func(t *testing.T) {
			var peerCalls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("X-Api-Key") != "consumer-key" {
					t.Error("unsupported contract attempted an unauthenticated read or write")
					w.WriteHeader(400)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/schema") {
					json.NewEncoder(w).Encode(sabnzbdConsumerSources()[0].schemas())
					return
				}
				w.Write([]byte(`[]`))
			}))
			t.Cleanup(server.Close)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peerCalls.Add(1)
				w.WriteHeader(400)
			}))
			t.Cleanup(target.Close)
			secrets := porttest.Secrets{strings.ToUpper(consumer.id) + "_API_KEY": "consumer-key", "SABNZBD_API_KEY": "accepted-key"}
			from := porttest.New(t, porttest.Options{App: consumer.id, Service: consumer.id, Secrets: secrets}, server, &porttest.Recorder{})
			to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
			to.ContainerPort = 8080
			if err := servarr.SABnzbdLink(consumer.api, consumer.field, "category").Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("unsupported SABnzbd consumer contract did not stop explicitly", err)
			}
			if peerCalls.Load() != 0 || secrets[from.Key("link_sabnzbd_resource")] != "" {
				t.Fatal("unsupported consumer contract reached peer operations or wrote ownership")
			}
		})
	}
}

func TestSABnzbdProtocolResponseMismatchPreservesUncertainty(t *testing.T) {
	for _, source := range sabnzbdConsumerSources() {
		for _, operation := range []string{"create", "update"} {
			t.Run(source.name()+"/"+operation, func(t *testing.T) {
				backend := arrtest.NewArr(source.id)
				backend.SeedRootFolder("/data/media/library")
				handler := sabnzbdSourceHandler(t, backend, source)
				corrupt := false
				var mutations atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "DELETE" {
						t.Error("uncertain response must not cause an undo")
					}
					if r.Method == "POST" || r.Method == "PUT" {
						mutations.Add(1)
					}
					if corrupt && (r.Method == "PUT" || r.Method == "POST" && r.URL.Path == source.api+"/downloadclient") {
						recorder := httptest.NewRecorder()
						handler.ServeHTTP(recorder, r)
						var item map[string]any
						if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &item) != nil {
							t.Error("fixture did not save the authorized request")
							w.WriteHeader(500)
							return
						}
						item["protocol"] = source.otherUsenetProtocol
						json.NewEncoder(w).Encode(item)
						return
					}
					handler.ServeHTTP(w, r)
				}))
				t.Cleanup(server.Close)
				fixture := newSabnzbdLinkFixture()
				target := httptest.NewServer(fixture.handler(t))
				t.Cleanup(target.Close)
				secrets := porttest.Secrets{strings.ToUpper(source.id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": fixture.key}
				from := porttest.New(t, porttest.Options{App: source.id, Service: source.id, Secrets: secrets}, server, &porttest.Recorder{})
				executor := &sabnzbdLinkExecutor{}
				from.Config.Exec = executor
				to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
				to.ContainerPort, to.Config.Exec = 8080, executor
				link := sabnzbdConsumerLink(t, source.consumer)
				if operation == "update" {
					if err := link.Apply(context.Background(), from, to); err != nil {
						t.Fatal(err)
					}
					fixture.mu.Lock()
					fixture.key = "renewed-accepted-key"
					fixture.mu.Unlock()
					secrets["SABNZBD_API_KEY"] = "renewed-accepted-key"
				}
				raw := secrets[from.Key("link_sabnzbd_resource")]
				corrupt = true
				if err := link.Apply(context.Background(), from, to); err == nil {
					t.Fatal("mismatched response protocol was accepted")
				}
				if len(backend.DownloadClients()) != 1 || backend.DownloadClients()[0]["protocol"] != source.protocol {
					t.Fatal("response corruption changed the submitted resource or caused an undo")
				}
				if operation == "update" {
					if secrets[from.Key("link_sabnzbd_resource")] != raw {
						t.Fatal("uncertain update advanced the stored fingerprint")
					}
					return
				}
				var intent struct {
					ID          int64  `json:"id"`
					Fingerprint string `json:"fingerprint"`
				}
				if err := json.Unmarshal([]byte(secrets[from.Key("link_sabnzbd_resource")]), &intent); err != nil || intent.ID != 0 || intent.Fingerprint == "" {
					t.Fatal("uncertain creation lost its intent or acquired an owned ID", err)
				}
				before := mutations.Load()
				if err := link.Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) || mutations.Load() != before {
					t.Fatal("uncertain creation was blindly retried", err)
				}
			})
		}
	}
}

func TestSABnzbdSelectionAdmissionPreservesEnabledOppositeAndOwnedSurvivors(t *testing.T) {
	for _, source := range sabnzbdConsumerSources() {
		for _, scenario := range []string{"fresh-competing-usenet", "fresh-opposite-torrent", "owned-intentional-usenet"} {
			t.Run(source.name()+"/"+scenario, func(t *testing.T) {
				backend := arrtest.NewArr(source.id)
				backend.SeedRootFolder("/data/media/library")
				category := source.category
				if source.id == "radarr" {
					category = "Movies"
				}
				if source.id == "lidarr" {
					category = "Music"
				}
				fields := []any{}
				for name, value := range map[string]any{"host": "localhost", "port": 6789, "useSsl": false, "urlBase": nil, "username": "nzbget", "password": "tegbzn6789", source.field: category} {
					fields = append(fields, map[string]any{"name": name, "value": value})
				}
				schema := map[string]any{"implementation": "Nzbget", "configContract": "NzbgetSettings", "protocol": source.protocol, "priority": 1, "fields": fields}
				if scenario == "fresh-opposite-torrent" {
					schema = source.schemas()[1]
				}
				schemas := append(source.schemas(), schema)
				if scenario == "fresh-opposite-torrent" {
					schemas = source.schemas()
				}
				base := sabnzbdSourceHandler(t, backend, source)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" && r.URL.Path == source.api+"/downloadclient/schema" && r.Header.Get("X-Api-Key") == backend.Key {
						json.NewEncoder(w).Encode(schemas)
						return
					}
					base.ServeHTTP(w, r)
				}))
				t.Cleanup(server.Close)
				fixture := newSabnzbdLinkFixture()
				target := httptest.NewServer(fixture.handler(t))
				t.Cleanup(target.Close)
				secrets := porttest.Secrets{strings.ToUpper(source.id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": fixture.key}
				from := porttest.New(t, porttest.Options{App: source.id, Service: source.id, Secrets: secrets}, server, &porttest.Recorder{})
				to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
				executor := &sabnzbdLinkExecutor{}
				from.Config.Exec, to.Config.Exec, to.ContainerPort = executor, executor, 8080
				consumerSecrets := &sabnzbdTrackedSecrets{values: secrets}
				targetSecrets := &sabnzbdTrackedSecrets{values: secrets}
				from.Config.Secrets, to.Config.Secrets = consumerSecrets, targetSecrets
				link := sabnzbdConsumerLink(t, source.consumer)
				if scenario == "owned-intentional-usenet" {
					if err := link.Apply(t.Context(), from, to); err != nil {
						t.Fatal(err)
					}
				}
				data, err := json.Marshal(schema)
				if err != nil {
					t.Fatal(err)
				}
				var survivor map[string]any
				if err := json.Unmarshal(data, &survivor); err != nil {
					t.Fatal(err)
				}
				survivor["name"], survivor["enable"], survivor["priority"] = "person-selected enabled survivor", true, float64(7)
				for _, raw := range survivor["fields"].([]any) {
					field := raw.(map[string]any)
					if field["name"] == "host" {
						field["value"] = "independent-downloader"
					}
				}
				backend.SeedDownloadClient(survivor)
				survivorData, err := json.Marshal(survivor)
				if err != nil {
					t.Fatal(err)
				}
				before, err := json.Marshal(backend.DownloadClients())
				if err != nil {
					t.Fatal(err)
				}
				raw := secrets[from.Key("link_sabnzbd_resource")]
				native := backend.DownloadClientTests()
				consumerSecrets.writes.Store(0)
				targetSecrets.reads.Store(0)
				executor.calls.Store(0)
				fixture.mu.Lock()
				fixture.calls = nil
				fixture.mu.Unlock()
				err = link.Apply(t.Context(), from, to)
				if scenario == "fresh-competing-usenet" {
					fixture.mu.Lock()
					calls := len(fixture.calls)
					fixture.mu.Unlock()
					after, _ := json.Marshal(backend.DownloadClients())
					if !errors.Is(err, helpers.ErrManual) || targetSecrets.reads.Load() != 0 || targetSecrets.writes.Load() != 0 || consumerSecrets.writes.Load() != 0 || executor.calls.Load() != 0 || calls != 0 || backend.DownloadClientTests() != native || string(after) != string(before) || secrets[from.Key("link_sabnzbd_resource")] != "" {
						t.Fatal("same-protocol creation changed selection or reached target/native/private effects", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(backend.DownloadClients()) != 2 || survivor["enable"] != true || survivor["priority"] != float64(7) {
					t.Fatal("successful coexistence changed the enabled meaningful survivor")
				}
				var preserved []byte
				for _, item := range backend.DownloadClients() {
					if item["id"] == survivor["id"] {
						preserved, err = json.Marshal(item)
					}
				}
				if err != nil || string(preserved) != string(survivorData) {
					t.Fatal("successful coexistence changed an existing survivor's ID or options", err)
				}
				if scenario == "owned-intentional-usenet" {
					after, _ := json.Marshal(backend.DownloadClients())
					if string(after) != string(before) || secrets[from.Key("link_sabnzbd_resource")] != raw || consumerSecrets.writes.Load() != 0 {
						t.Fatal("owned intentional multi-client rerun changed old options or ownership")
					}
				}
				target.Close()
				delete(secrets, "SABNZBD_API_KEY")
				targetSecrets.reads.Store(0)
				executor.calls.Store(0)
				native = backend.DownloadClientTests()
				if err := link.Remove(t.Context(), from, to); err != nil || len(backend.DownloadClients()) != 1 || backend.DownloadClients()[0]["id"] != survivor["id"] || survivor["enable"] != true || targetSecrets.reads.Load() != 0 || executor.calls.Load() != 0 || backend.DownloadClientTests() != native {
					t.Fatal("owned removal changed an enabled survivor or required target readiness", err)
				}
				preserved, err = json.Marshal(backend.DownloadClients()[0])
				if err != nil || string(preserved) != string(survivorData) {
					t.Fatal("owned removal changed surviving options", err)
				}
				if scenario == "owned-intentional-usenet" {
					consumerSecrets.writes.Store(0)
					if err := link.Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) || targetSecrets.reads.Load() != 0 || consumerSecrets.writes.Load() != 0 || executor.calls.Load() != 0 || backend.DownloadClientTests() != native {
						t.Fatal("reinstall ignored the surviving person's active Usenet selection", err)
					}
				}
			})
		}
	}
}

func sabnzbdCaptureWire(t *testing.T, source sabnzbdConsumerSource, next http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == "POST" || r.Method == "PUT") && strings.Contains(r.URL.Path, "/downloadclient") {
			data, err := io.ReadAll(r.Body)
			var item map[string]any
			if err != nil || json.Unmarshal(data, &item) != nil {
				t.Error("unreadable download-client request")
			} else if item["implementation"] == "Sabnzbd" && item["protocol"] != source.protocol || item["implementation"] == "QBittorrent" && item["protocol"] != source.torrentProtocol {
				t.Error("native test or write did not retain the source schema's wire protocol")
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		next.ServeHTTP(w, r)
	})
}
