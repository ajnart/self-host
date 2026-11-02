package nzbget_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

type nzbgetConsumerCase struct {
	id, api, categoryField, category string
}

var nzbgetConsumerCases = []nzbgetConsumerCase{
	{"sonarr", servarr.V3, "tvCategory", "tv"},
	{"radarr", servarr.V3, "movieCategory", "Movies"},
	{"lidarr", servarr.V1, "musicCategory", "Music"},
}

type nzbgetLinkFixture struct {
	backend   *arrtest.Arr
	from, to  *helpers.H
	secrets   porttest.Secrets
	recorder  *porttest.Recorder
	template  map[string]any
	link      app.Link
	category  string
	field     string
	pathProbe *nzbgetImageIdentityFixture
}

type nzbgetImageIdentityFixture struct {
	helpers.Executor
	directory, service string
	wrongIdentity      int
}

func (f *nzbgetImageIdentityFixture) Exec(_ context.Context, service string, _ map[string]string, args ...string) (system.Result, error) {
	if len(args) != 8 || args[6] != "self-host-download-path" || service != "nzbget" {
		return system.Result{}, errors.New("unexpected path command in NZBGet identity fixture")
	}
	f.service, f.directory = service, args[7]
	if strings.Join(args[:3], " ") == "s6-setuidgid abc sh" {
		f.wrongIdentity++
		return system.Result{ExitCode: 127}, nil
	}
	return system.Result{}, errors.New("unsupported image identity fixture command")
}

func newNZBGetLinkFixture(t *testing.T, consumer nzbgetConsumerCase) *nzbgetLinkFixture {
	t.Helper()
	backend := arrtest.NewArr(consumer.id)
	backend.SeedRootFolder("/data/media/library")
	priorityField := map[string]string{"sonarr": "Tv", "radarr": "Movie", "lidarr": "Music"}[consumer.id]
	fields := []any{}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"host", "nzbget"}, {"port", float64(6789)}, {"useSsl", false}, {"urlBase", ""},
		{"username", "admin"}, {"password", password}, {consumer.categoryField, consumer.category},
		{"recent" + priorityField + "Priority", float64(0)}, {"older" + priorityField + "Priority", float64(0)}, {"addPaused", false},
	} {
		fields = append(fields, map[string]any{"name": field.name, "value": field.value})
	}
	f := &nzbgetLinkFixture{
		backend: backend, category: consumer.category, field: consumer.categoryField,
		template: map[string]any{"name": "NZBGet", "implementation": "Nzbget", "implementationName": "NZBGet", "configContract": "NzbgetSettings", "protocol": "usenet", "enable": true, "priority": float64(9), "fields": fields},
		secrets:  porttest.Secrets{strings.ToUpper(consumer.id) + "_API_KEY": backend.Key}, recorder: &porttest.Recorder{},
		link: servarr.NZBGetLink(consumer.api, consumer.categoryField, consumer.category), pathProbe: &nzbgetImageIdentityFixture{},
	}
	base := backend.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != consumer.api+"/downloadclient/schema" {
			base.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != backend.Key || r.Method != "GET" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{f.template})
	}))
	t.Cleanup(server.Close)
	f.from = porttest.New(t, porttest.Options{App: consumer.id, Service: consumer.id, Secrets: f.secrets}, server, f.recorder)
	f.to, _ = nzbgetDownloadTarget(t, nzbgetDownloadSettings())
	f.to.Port, f.to.ContainerPort = 16789, 6789
	f.to.Config.Exec = f.pathProbe
	return f
}

func (f *nzbgetLinkFixture) seedOwned(t *testing.T) string {
	t.Helper()
	f.backend.SeedDownloadClient(f.template)
	owned := f.backend.DownloadClients()[0]
	record, err := json.Marshal(map[string]any{"id": owned["id"], "target": "nzbget", "endpoint": "nzbget", "fingerprint": strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	key := f.from.Key("link_nzbget_resource")
	f.secrets[key] = string(record)
	return string(record)
}

func TestNZBGetLinkHoldsUnsupportedImageIdentityBeforeNativeTesting(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("unsupported target App identity must remain Manual", err)
			}
			if f.pathProbe.wrongIdentity == 0 || f.pathProbe.directory != "/data/usenet/complete/"+consumer.category {
				t.Fatal("the selected existing category did not reach the common read-only path guard")
			}
			if f.backend.DownloadClientTests() != 0 || len(f.backend.DownloadClients()) != 0 || f.secrets[f.from.Key("link_nzbget_resource")] != "" {
				t.Fatal("unverified image identity reached native testing or resource mutation")
			}
			if f.recorder.Status("link-nzbget") != stepstatus.StepManual || !strings.Contains(f.recorder.Hint(), "self-host setup "+consumer.id) {
				t.Fatal("path prerequisite lost its consumer-owned persisted recovery")
			}
		})
	}
}

func TestNZBGetLinkUsesStoredCategoryAndPreservesOldOwnershipDuringManualRecovery(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			for _, raw := range f.template["fields"].([]any) {
				field := raw.(map[string]any)
				if field["name"] == consumer.categoryField {
					field["value"] = "Music"
				}
			}
			record := f.seedOwned(t)
			for attempt := 0; attempt < 2; attempt++ {
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("unverified identity must not be silently repaired", err)
				}
			}
			items := f.backend.DownloadClients()
			if len(items) != 1 || f.pathProbe.directory != "/data/usenet/complete/Music" || arrtest.Field(items[0], consumer.categoryField) != "Music" || items[0]["priority"] != float64(9) || f.secrets[f.from.Key("link_nzbget_resource")] != record {
				t.Fatal("Manual recovery changed the selected category, noncredential choices or old-format owned record")
			}
			f.to.Secrets.Set("ADMIN_PASSWORD", "rejected-private-password")
			f.to.Config.Exec = nil
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil || len(f.backend.DownloadClients()) != 0 || f.secrets[f.from.Key("link_nzbget_resource")] != "" {
				t.Fatal("recorded removal must not depend on target authentication/path readiness", err)
			}
		})
	}
}

func TestNZBGetLinkRejectsProviderIdentityAndPreservesUnownedEntries(t *testing.T) {
	for _, field := range []struct{ name, value string }{{"implementation", "NZBGet"}, {"configContract", "NZBGetSettings"}, {"protocol", "torrent"}} {
		t.Run(field.name, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, nzbgetConsumerCases[0])
			f.template[field.name] = field.value
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.backend.DownloadClientTests() != 0 || f.pathProbe.wrongIdentity != 0 {
				t.Fatal("unsupported schema identity reached provider/native checks", err)
			}
		})
	}
	f := newNZBGetLinkFixture(t, nzbgetConsumerCases[0])
	f.backend.SeedDownloadClient(f.template)
	if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("unowned same-target entry was adopted", err)
	}
	if err := f.link.Remove(context.Background(), f.from, f.to); err != nil || len(f.backend.DownloadClients()) != 1 || f.secrets[f.from.Key("link_nzbget_resource")] != "" {
		t.Fatal("unowned entry was removed or acquired ownership", err)
	}
}

func TestNZBGetLinkHoldsCategoryAuthenticationAndUncertainOwnershipBeforeNative(t *testing.T) {
	for _, scenario := range []string{"missing category", "rejected login", "missing login", "uncertain creation", "edited base", "wrong container port"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, nzbgetConsumerCases[0])
			switch scenario {
			case "missing category":
				f.link = servarr.NZBGetLink(servarr.V3, "tvCategory", "missing")
			case "rejected login":
				f.to.Secrets.Set("ADMIN_PASSWORD", "rejected-private-password")
			case "missing login":
				f.to.Secrets.Set("ADMIN_PASSWORD", "")
			case "uncertain creation":
				f.secrets[f.from.Key("link_nzbget_resource")] = `{"id":0,"target":"nzbget","endpoint":"nzbget","fingerprint":"` + strings.Repeat("a", 64) + `"}`
			case "edited base":
				f.seedOwned(t)
				for _, raw := range f.template["fields"].([]any) {
					field := raw.(map[string]any)
					if field["name"] == "urlBase" {
						field["value"] = "/chosen-proxy"
					}
				}
			case "wrong container port":
				f.to.ContainerPort = 0
			}
			before := f.secrets[f.from.Key("link_nzbget_resource")]
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-password") {
				t.Fatal("unsafe state was accepted or disclosed", err)
			}
			if f.backend.DownloadClientTests() != 0 || f.pathProbe.wrongIdentity != 0 || f.secrets[f.from.Key("link_nzbget_resource")] != before {
				t.Fatal("unsafe provider state reached path/native testing or changed ownership")
			}
		})
	}
}
