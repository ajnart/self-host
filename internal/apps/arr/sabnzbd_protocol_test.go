package arr_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestSABnzbdRejectsWrongNativeSchemaAndOwnedProtocolBeforePeerRPC(t *testing.T) {
	for _, consumer := range []struct{ id, api, field, category, wrongProtocol string }{
		{"sonarr", servarr.V3, "tvCategory", "tv", "UsenetDownloadProtocol"},
		{"radarr", servarr.V3, "movieCategory", "movies", "UsenetDownloadProtocol"},
		{"lidarr", servarr.V1, "musicCategory", "music", "usenet"},
	} {
		for _, scenario := range []string{"schema", "owned"} {
			t.Run(consumer.id+"/"+scenario, func(t *testing.T) {
				backend := arrtest.NewArr(consumer.id)
				backend.SeedRootFolder("/data/media/library")
				handler := backend.Handler()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if scenario != "schema" || r.URL.Path != consumer.api+"/downloadclient/schema" {
						handler.ServeHTTP(w, r)
						return
					}
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					var schemas []map[string]any
					if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &schemas) != nil {
						t.Error("authenticated native schema could not be read")
						w.WriteHeader(500)
						return
					}
					for _, schema := range schemas {
						if schema["implementation"] == "Sabnzbd" {
							schema["protocol"] = consumer.wrongProtocol
						}
					}
					json.NewEncoder(w).Encode(schemas)
				}))
				t.Cleanup(server.Close)
				fixture := newSabnzbdLinkFixture()
				target := httptest.NewServer(fixture.handler(t))
				t.Cleanup(target.Close)
				secrets := porttest.Secrets{strings.ToUpper(consumer.id) + "_API_KEY": backend.Key, "SABNZBD_API_KEY": fixture.key}
				from := porttest.New(t, porttest.Options{App: consumer.id, Service: consumer.id, Secrets: secrets}, server, &porttest.Recorder{})
				executor := &sabnzbdLinkExecutor{}
				from.Config.Exec = executor
				to := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: secrets}, target, &porttest.Recorder{})
				to.ContainerPort, to.Config.Exec = 8080, executor
				link := servarr.SABnzbdLink(consumer.api, consumer.field, consumer.category)
				if scenario == "owned" {
					if err := link.Apply(context.Background(), from, to); err != nil {
						t.Fatal(err)
					}
					backend.DownloadClients()[0]["protocol"] = consumer.wrongProtocol
					fixture.mu.Lock()
					fixture.calls = nil
					fixture.mu.Unlock()
				}
				record := secrets[from.Key("link_sabnzbd_resource")]
				clients, nativeTests := len(backend.DownloadClients()), backend.DownloadClientTests()
				if err := link.Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("wrong native protocol was accepted", err)
				}
				fixture.mu.Lock()
				peerCalls := len(fixture.calls)
				fixture.mu.Unlock()
				if peerCalls != 0 || backend.DownloadClientTests() != nativeTests || len(backend.DownloadClients()) != clients || secrets[from.Key("link_sabnzbd_resource")] != record {
					t.Fatal("wrong protocol reached peer RPC/native testing or changed ownership")
				}
				if scenario == "owned" && backend.DownloadClients()[0]["protocol"] != consumer.wrongProtocol {
					t.Fatal("an incompatible recorded protocol was silently migrated")
				}
			})
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
			unexpectedCalls, peerCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == consumer.api+"/downloadclient" && r.Header.Get("X-Api-Key") == "consumer-key" {
					w.Write([]byte(`[]`))
					return
				}
				unexpectedCalls++
				w.WriteHeader(400)
			}))
			t.Cleanup(server.Close)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peerCalls++
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
			if peerCalls != 0 || unexpectedCalls != 0 || secrets[from.Key("link_sabnzbd_resource")] != "" {
				t.Fatal("unsupported consumer contract reached schema/peer/native operations or wrote ownership")
			}
		})
	}
}
