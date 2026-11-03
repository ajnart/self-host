package nzbget_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	id, api, categoryField, category, protocol, version, survivorProtocol string
}

var nzbgetConsumerCases = []nzbgetConsumerCase{
	{"sonarr", servarr.V3, "tvCategory", "tv", "usenet", "4.0.0", "torrent"},
	{"radarr", servarr.V3, "movieCategory", "Movies", "usenet", "4.0.0", "torrent"},
	{"lidarr", servarr.V1, "musicCategory", "Music", "usenet", "3.1.0.4875", "torrent"},
	{"lidarr", servarr.V1, "musicCategory", "Music", "UsenetDownloadProtocol", "3.1.6.5079", "TorrentDownloadProtocol"},
}

func nzbgetSurvivor(consumer nzbgetConsumerCase) map[string]any {
	category := consumer.id
	if consumer.id == "sonarr" {
		category = "tv-sonarr"
	}
	prefix := strings.TrimSuffix(consumer.categoryField, "Category")
	priority := strings.ToUpper(prefix[:1]) + prefix[1:]
	fields := []any{}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"host", "localhost"}, {"port", float64(8080)}, {"useSsl", false}, {"urlBase", nil},
		{"username", nil}, {"password", nil}, {consumer.categoryField, category}, {prefix + "ImportedCategory", nil},
		{"recent" + priority + "Priority", float64(0)}, {"older" + priority + "Priority", float64(0)},
		{"initialState", float64(0)}, {"sequentialOrder", false}, {"firstAndLast", false}, {"contentLayout", float64(0)},
	} {
		fields = append(fields, map[string]any{"name": field.name, "value": field.value})
	}
	if consumer.version == "3.1.6.5079" {
		fields = append(fields, map[string]any{"name": "apiKey", "value": nil})
	}
	return map[string]any{
		"name": "Independent qBittorrent", "implementation": "QBittorrent", "configContract": "QBittorrentSettings", "protocol": consumer.survivorProtocol, "enable": true, "priority": float64(4), "tags": []any{float64(2)},
		"fields": fields,
	}
}

type nzbgetLinkSecrets struct {
	helpers.Secrets
	reads, writes int
}

func (s *nzbgetLinkSecrets) Get(key string) string {
	s.reads++
	return s.Secrets.Get(key)
}

func (s *nzbgetLinkSecrets) Set(key, value string) error {
	s.writes++
	return s.Secrets.Set(key, value)
}

type nzbgetLinkFixture struct {
	backend                                     *arrtest.Arr
	from, to                                    *helpers.H
	secrets                                     porttest.Secrets
	recorder                                    *porttest.Recorder
	template                                    map[string]any
	link                                        app.Link
	category, field                             string
	pathProbe                                   *nzbgetImageIdentityFixture
	targetSettings                              *sync.Map
	handler                                     http.Handler
	mappings                                    []map[string]any
	roots                                       []map[string]any
	mask, uncertain, missingRoots, rejectNative bool
	nativeBody                                  string
	nativeStatus                                int
	creates, updates, deletes, nativeTests      int
	lastNativeTargets, lastNativeConsumers      int
	version                                     string
	statusReads, schemaReads, resourceReads     int
	schemaStatus, resourceStatus                int
	schemaBody, resourceBody                    *string
	schemas                                     []map[string]any
	responseProtocol                            string
	nativeItems, createdItems, updatedItems     []map[string]any
	consumerSecrets, targetSecrets              *nzbgetLinkSecrets
}

type nzbgetImageIdentityFixture struct {
	helpers.Executor
	directory, service string
	wrongIdentity      int
	supported          bool
	targetService      string
	remote, local      string
	denied             string
	distinct, overlap  bool
	targets, consumers int
	observed           []string
}

func (f *nzbgetImageIdentityFixture) Exec(_ context.Context, service string, _ map[string]string, args ...string) (system.Result, error) {
	if len(args) != 8 || args[6] != "self-host-download-path" {
		return system.Result{}, errors.New("unexpected path command in NZBGet identity fixture")
	}
	target := f.targetService
	if target == "" {
		target = "nzbget"
	}
	prefix := ""
	if service == target {
		f.service, f.directory = service, args[7]
		if strings.Join(args[:3], " ") != "su-exec user:users sh" || !f.supported {
			f.wrongIdentity++
			return system.Result{ExitCode: 127}, nil
		}
		start := strings.Index(args[5], `test -d "$1"`)
		for _, assertion := range []string{`test "$(id -u)" -ne 0`, `test "$(id -g)" -ne 0`, `test "$(id -un)" = "user"`, `test "$(id -gn)" = "users"`} {
			if index := strings.Index(args[5], assertion); index < 0 || start < 0 || index >= start {
				return system.Result{}, errors.New("exact nonroot child identity was not checked before filesystem access")
			}
		}
		prefix = "user\nusers\n"
		f.targets++
	} else {
		if strings.Join(args[:3], " ") != "s6-setuidgid abc sh" {
			return system.Result{}, errors.New("consumer path probe did not retain S6 abc")
		}
		f.consumers++
	}
	directory := args[7]
	f.observed = append(f.observed, service+":"+directory)
	if f.denied == service+":"+directory {
		return system.Result{ExitCode: 1}, nil
	}
	identity, backing := "1:42", "/fixture/downloads"
	if strings.Contains(directory, "/media/") {
		identity, backing = "1:99", "/fixture/library"
	} else {
		base := f.local
		if service == target {
			base = f.remote
		}
		if base == "" {
			base = "/data/usenet/complete"
		}
		if directory != base && !strings.HasPrefix(directory, base+"/") {
			return system.Result{ExitCode: 1}, nil
		}
		backing += strings.TrimPrefix(directory, base)
		if f.distinct && service != target {
			identity = "1:77"
		}
		if f.overlap {
			backing = "/fixture/library"
		}
	}
	mountEscape := strings.NewReplacer("\\", `\134`, " ", `\040`, "\t", `\011`, "\n", `\012`)
	return system.Result{Stdout: []byte(prefix + directory + "\n" + identity + "\n1 0 0:5 " + mountEscape.Replace(backing) + " " + mountEscape.Replace(directory) + " rw - ext4 /dev/fixture rw\n")}, nil
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
	if consumer.id == "lidarr" {
		for _, raw := range fields {
			field := raw.(map[string]any)
			switch field["name"] {
			case "host":
				field["value"] = "localhost"
			case "urlBase":
				field["value"] = nil
			case "username":
				field["value"] = "nzbget"
			case "password":
				field["value"] = "tegbzn6789"
			}
		}
	}
	f := &nzbgetLinkFixture{
		backend: backend, category: consumer.category, field: consumer.categoryField,
		template: map[string]any{"name": "NZBGet", "implementation": "Nzbget", "implementationName": "NZBGet", "configContract": "NzbgetSettings", "protocol": consumer.protocol, "enable": true, "priority": float64(9), "fields": fields},
		secrets:  porttest.Secrets{strings.ToUpper(consumer.id) + "_API_KEY": backend.Key}, recorder: &porttest.Recorder{},
		link: servarr.NZBGetLink(consumer.api, consumer.categoryField, consumer.category), pathProbe: &nzbgetImageIdentityFixture{}, mappings: []map[string]any{},
		version: consumer.version, schemaStatus: http.StatusOK, resourceStatus: http.StatusOK,
	}
	survivorSchema := nzbgetSurvivor(consumer)
	survivorSchema["name"], survivorSchema["enable"], survivorSchema["priority"], survivorSchema["tags"] = nil, false, float64(1), []any{}
	f.schemas = []map[string]any{f.template, survivorSchema}
	backend.AcceptDownloadProvider = func(item map[string]any) bool {
		if item["implementation"] != "Nzbget" {
			return true
		}
		accepted := password
		if value, changed := f.targetSettings.Load("rpc_password"); changed {
			accepted = value.(string)
		}
		host := f.pathProbe.targetService
		if host == "" {
			host = "nzbget"
		}
		value := arrtest.Field(item, "password")
		return !f.rejectNative && item["protocol"] == consumer.protocol && arrtest.Field(item, "host") == host && arrtest.Field(item, "port") == float64(6789) && arrtest.Field(item, "username") == "admin" && (value == accepted || value == "********")
	}
	base := backend.Handler()
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" || r.URL.Path == "/login" {
			base.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != backend.Key {
			w.WriteHeader(401)
			return
		}
		var submitted map[string]any
		if strings.HasPrefix(r.URL.Path, consumer.api+"/downloadclient") && (r.Method == "POST" || r.Method == "PUT") {
			body, err := io.ReadAll(r.Body)
			if err != nil || json.Unmarshal(body, &submitted) != nil {
				t.Error("invalid download-client fixture request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		switch r.URL.Path {
		case consumer.api + "/system/status":
			f.statusReads++
			if consumer.id == "lidarr" {
				json.NewEncoder(w).Encode(map[string]any{"appName": "Lidarr", "version": f.version})
				return
			}
		case consumer.api + "/downloadclient/schema":
			f.schemaReads++
			w.WriteHeader(f.schemaStatus)
			if f.schemaBody != nil {
				w.Write([]byte(*f.schemaBody))
				return
			}
			if f.schemas != nil {
				json.NewEncoder(w).Encode(f.schemas)
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{f.template})
			return
		case consumer.api + "/remotepathmapping":
			json.NewEncoder(w).Encode(f.mappings)
			return
		case consumer.api + "/rootfolder":
			if f.roots != nil && r.Method == "GET" {
				json.NewEncoder(w).Encode(f.roots)
				return
			}
			if f.missingRoots && r.Method == "GET" {
				w.Write([]byte(`[]`))
				return
			}
		case consumer.api + "/downloadclient/test":
			if f.pathProbe.targets <= f.lastNativeTargets || f.pathProbe.consumers < f.lastNativeConsumers+2 {
				t.Error("native test preceded a fresh guarded target/consumer path verification")
			}
			f.lastNativeTargets, f.lastNativeConsumers = f.pathProbe.targets, f.pathProbe.consumers
			f.nativeTests++
			f.nativeItems = append(f.nativeItems, submitted)
			if f.nativeBody != "" {
				w.Write([]byte(f.nativeBody))
				return
			}
			if f.nativeStatus != 0 {
				result := httptest.NewRecorder()
				base.ServeHTTP(result, r)
				if result.Code != http.StatusOK {
					w.WriteHeader(result.Code)
					w.Write(result.Body.Bytes())
					return
				}
				w.WriteHeader(f.nativeStatus)
				return
			}
		case consumer.api + "/downloadclient":
			if r.Method == "POST" {
				f.creates++
				f.createdItems = append(f.createdItems, submitted)
				if f.uncertain {
					base.ServeHTTP(httptest.NewRecorder(), r)
					w.Write([]byte(`{}`))
					return
				}
			}
			if r.Method == "GET" {
				f.resourceReads++
				if f.resourceBody != nil || f.resourceStatus != http.StatusOK {
					w.WriteHeader(f.resourceStatus)
					if f.resourceBody != nil {
						w.Write([]byte(*f.resourceBody))
					}
					return
				}
			}
			if r.Method == "GET" && f.mask {
				data, _ := json.Marshal(backend.DownloadClients())
				var items []map[string]any
				json.Unmarshal(data, &items)
				for _, item := range items {
					if item["implementation"] == "Nzbget" {
						nzbgetSetClientField(item, "password", "********")
					}
				}
				json.NewEncoder(w).Encode(items)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, consumer.api+"/downloadclient/") {
			if r.Method == "PUT" {
				f.updates++
				f.updatedItems = append(f.updatedItems, submitted)
			}
			if r.Method == "DELETE" {
				f.deletes++
			}
		}
		if f.responseProtocol != "" && (r.Method == "POST" && r.URL.Path == consumer.api+"/downloadclient" || r.Method == "PUT" && strings.HasPrefix(r.URL.Path, consumer.api+"/downloadclient/")) {
			result := httptest.NewRecorder()
			base.ServeHTTP(result, r)
			var response map[string]any
			if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &response) != nil {
				t.Error("cannot corrupt unsuccessful fixture response")
			}
			response["protocol"] = f.responseProtocol
			w.WriteHeader(result.Code)
			json.NewEncoder(w).Encode(response)
			return
		}
		base.ServeHTTP(w, r)
	})
	server := httptest.NewServer(f.handler)
	t.Cleanup(server.Close)
	f.from = porttest.New(t, porttest.Options{App: consumer.id, Service: consumer.id, Secrets: f.secrets}, server, f.recorder)
	f.to, f.targetSettings = nzbgetDownloadTarget(t, nzbgetDownloadSettings())
	f.to.Port, f.to.ContainerPort = 16789, 6789
	f.to.Config.Exec = f.pathProbe
	f.from.Config.Exec = f.pathProbe
	f.consumerSecrets = &nzbgetLinkSecrets{Secrets: f.from.Secrets}
	f.targetSecrets = &nzbgetLinkSecrets{Secrets: f.to.Secrets}
	f.from.Secrets, f.to.Secrets = f.consumerSecrets, f.targetSecrets
	return f
}

func nzbgetSetClientField(item map[string]any, name string, value any) {
	for _, raw := range item["fields"].([]any) {
		field := raw.(map[string]any)
		if field["name"] == name {
			field["value"] = value
			return
		}
	}
}

func (f *nzbgetLinkFixture) seedOwned(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(f.template)
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"host": "nzbget", "port": float64(6789), "useSsl": false, "urlBase": "", "username": "admin", "password": password} {
		nzbgetSetClientField(item, name, value)
	}
	f.backend.SeedDownloadClient(item)
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
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
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
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
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
				nzbgetSetClientField(f.backend.DownloadClients()[0], "urlBase", "/chosen-proxy")
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
