package servarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type servarrContractFixture struct {
	template                                 provider
	items                                    []provider
	nativeBody                               string
	nativeStatus                             int
	tests, creates, updates, deletes, guards int
	guardErr                                 error
	uncertain                                bool
	responseEdit                             func(provider)
	from, to                                 *helpers.H
	recorder                                 *porttest.Recorder
	secrets                                  porttest.Secrets
	p                                        providerLink
	nextID                                   int64
	version, appName                         string
	schemaBody, resourceBody                 *string
	schemaStatus, resourceStatus             int
	schemas                                  []provider
	schemaReads, resourceReads               int
	nativeItems, createdItems, updatedItems  []provider
	beforeResourceRead                       func()
}

func servarrContractCopy(item provider) provider {
	data, _ := json.Marshal(item)
	var copied provider
	_ = json.Unmarshal(data, &copied)
	return copied
}

func newServarrContract(t *testing.T, api string) *servarrContractFixture {
	t.Helper()
	f := &servarrContractFixture{nativeBody: `{}`, nativeStatus: 200, items: []provider{}, secrets: porttest.Secrets{"SONARR_API_KEY": "consumer-key"}, recorder: &porttest.Recorder{}, nextID: 17, schemaStatus: 200, resourceStatus: 200}
	f.template = provider{"implementation": "ContractClient", "configContract": "ContractSettings", "protocol": "usenet", "fields": []any{}}
	for name, value := range map[string]any{"host": "localhost", "port": float64(8080), "useSsl": false, "urlBase": "", "apiKey": "", "tvCategory": "tv"} {
		f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": name, "value": value})
	}
	f.p = providerLink{api: api, path: "/downloadclient", target: "contract", implementation: "ContractClient", name: "Contract", endpointField: "host", categoryField: "tvCategory", category: "tv", configContract: "ContractSettings", protocol: "usenet"}
	f.p.connectionFields = func(_, to *helpers.H) ([]connectionField, error) {
		return []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", ""}, {"apiKey", to.Secret("CONTRACT_API_KEY")}}, nil
	}
	f.p.verify = func(context.Context, *helpers.H, *helpers.H, provider) error { f.guards++; return f.guardErr }
	mux := http.NewServeMux()
	mux.HandleFunc(api+"/system/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"appName": f.appName, "version": f.version})
	})
	mux.HandleFunc(api+"/downloadclient/schema", func(w http.ResponseWriter, r *http.Request) {
		f.schemaReads++
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(f.schemaStatus)
		if f.schemaBody != nil {
			w.Write([]byte(*f.schemaBody))
			return
		}
		if f.schemas != nil {
			json.NewEncoder(w).Encode(f.schemas)
			return
		}
		json.NewEncoder(w).Encode([]provider{f.template})
	})
	mux.HandleFunc(api+"/downloadclient/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			w.WriteHeader(401)
			return
		}
		if f.p.connectionFields != nil && f.guards == 0 {
			t.Error("native test ran before provider guard")
		}
		f.tests++
		var item provider
		if json.NewDecoder(r.Body).Decode(&item) != nil {
			t.Error("invalid native-test body")
		}
		f.nativeItems = append(f.nativeItems, item)
		w.WriteHeader(f.nativeStatus)
		w.Write([]byte(f.nativeBody))
	})
	mux.HandleFunc(api+"/downloadclient", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" {
			f.resourceReads++
			if f.beforeResourceRead != nil {
				f.beforeResourceRead()
			}
			w.WriteHeader(f.resourceStatus)
			if f.resourceBody != nil {
				w.Write([]byte(*f.resourceBody))
				return
			}
			json.NewEncoder(w).Encode(f.items)
			return
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(405)
			return
		}
		f.creates++
		var item provider
		if json.NewDecoder(r.Body).Decode(&item) != nil {
			t.Fatal("invalid creation body")
		}
		f.createdItems = append(f.createdItems, servarrContractCopy(item))
		item["id"] = float64(f.nextID)
		f.nextID++
		f.items = append(f.items, item)
		if f.uncertain {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(201)
		response := servarrContractCopy(item)
		if f.responseEdit != nil {
			f.responseEdit(response)
		}
		json.NewEncoder(w).Encode(response)
	})
	mux.HandleFunc(api+"/downloadclient/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			w.WriteHeader(401)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			w.WriteHeader(400)
			return
		}
		switch r.Method {
		case "PUT":
			f.updates++
			var item provider
			json.NewDecoder(r.Body).Decode(&item)
			f.updatedItems = append(f.updatedItems, servarrContractCopy(item))
			for index, existing := range f.items {
				if existing.id() == id {
					f.items[index] = item
				}
			}
			response := servarrContractCopy(item)
			if f.responseEdit != nil {
				f.responseEdit(response)
			}
			json.NewEncoder(w).Encode(response)
		case "DELETE":
			f.deletes++
			f.items = slices.DeleteFunc(f.items, func(item provider) bool { return item.id() == id })
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected resource method %s", r.Method)
			w.WriteHeader(405)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	f.from = porttest.New(t, porttest.Options{App: "sonarr", Service: "sonarr", Secrets: f.secrets}, server, f.recorder)
	f.to = helpers.New(helpers.Config{App: "contract", Service: "contract", Port: 18080, ContainerPort: 8080, Secrets: f.secrets, Log: f.from.Log})
	f.secrets["CONTRACT_API_KEY"] = "accepted-key"
	return f
}

func (f *servarrContractFixture) link() app.Link {
	return f.p.link("contract", app.LinkStep{Message: "connect", Hint: func(*helpers.H, *helpers.H) string { return "retry consumer" }}, app.LinkStep{Message: "remove", Hint: func(*helpers.H, *helpers.H) string { return "review owned resource" }})
}

func TestDownloadProviderCreatesTestsRerunsAndPreservesChoices(t *testing.T) {
	for _, api := range []string{V3, V1} {
		t.Run(api, func(t *testing.T) {
			f := newServarrContract(t, api)
			link := f.link()
			if err := link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.creates != 1 || f.tests != 2 || f.items[0].field("port") != float64(8080) {
				t.Fatalf("creates=%d tests=%d item=%v", f.creates, f.tests, f.items)
			}
			ownership := f.secrets[f.p.ownershipKey(f.from)]
			f.items[0].setField("tvCategory", "chosen-category")
			f.items[0]["priority"] = float64(7)
			if err := link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.creates != 1 || f.tests != 3 || f.updates != 0 || f.secrets[f.p.ownershipKey(f.from)] != ownership || f.items[0].field("tvCategory") != "chosen-category" || f.items[0]["priority"] != float64(7) {
				t.Fatal("rerun replaced ownership or user choices")
			}
			f.secrets["CONTRACT_API_KEY"] = "new-accepted-key"
			if err := link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.updates != 1 || f.items[0].id() != 17 || f.items[0].field("apiKey") != "new-accepted-key" || f.items[0].field("tvCategory") != "chosen-category" {
				t.Fatal("credential recovery changed resource identity or category")
			}
			f.guardErr = errors.New("unready target")
			delete(f.secrets, "CONTRACT_API_KEY")
			if err := link.Remove(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.deletes != 1 || f.secrets[f.p.ownershipKey(f.from)] != "" {
				t.Fatal("owned removal failed")
			}
		})
	}
}

func TestDownloadProviderIdentityRemainsRequiredForVerifyCreateAndUpdate(t *testing.T) {
	for _, field := range []struct {
		name  string
		value any
	}{{"implementation", "ChangedClient"}, {"configContract", "ChangedSettings"}, {"protocol", "torrent"}, {"enable", false}} {
		for _, operation := range []string{"verify", "create", "update"} {
			t.Run(field.name+"/"+operation, func(t *testing.T) {
				f := newServarrContract(t, V3)
				if operation != "create" {
					if err := f.link().Apply(context.Background(), f.from, f.to); err != nil {
						t.Fatal(err)
					}
				}
				record := f.secrets[f.p.ownershipKey(f.from)]
				if operation == "verify" {
					f.items[0][field.name] = field.value
				} else {
					f.responseEdit = func(item provider) { item[field.name] = field.value }
				}
				if operation == "update" {
					f.secrets["CONTRACT_API_KEY"] = "new-accepted-key"
				}
				if err := f.link().Apply(context.Background(), f.from, f.to); err == nil {
					t.Fatal("changed provider identity was accepted")
				}
				if operation == "create" {
					var pending providerOwnership
					if err := json.Unmarshal([]byte(f.secrets[f.p.ownershipKey(f.from)]), &pending); err != nil || pending.ID != 0 {
						t.Fatal("invalid creation identity acquired an owned ID", err)
					}
				} else if f.secrets[f.p.ownershipKey(f.from)] != record {
					t.Fatal("changed identity advanced the ownership record")
				}
			})
		}
	}
}

func TestDownloadProviderGuardsAndMalformedTestsCannotMutate(t *testing.T) {
	for _, body := range []string{`{"error":"secret-in-response"}`, `[]`, `null`, `true`, ``} {
		t.Run(body, func(t *testing.T) {
			f := newServarrContract(t, V3)
			f.nativeBody = body
			if err := f.link().Apply(context.Background(), f.from, f.to); err == nil || strings.Contains(err.Error(), "secret-in-response") {
				t.Fatalf("invalid test accepted or disclosed: %v", err)
			}
			if f.creates != 0 || f.updates != 0 || f.secrets[f.p.ownershipKey(f.from)] != "" {
				t.Fatal("test failure mutated resource or ownership")
			}
		})
	}
	f := newServarrContract(t, V3)
	f.guardErr = &helpers.ManualError{Hint: "choose an existing category"}
	if err := f.link().Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if f.tests != 0 || f.creates != 0 || f.recorder.Status("link-contract") != stepstatus.StepManual || f.recorder.Hint() != "choose an existing category" {
		t.Fatal("provider prerequisite did not persist before native test")
	}
}

func TestDownloadProviderMaskedCredentialsEditsAndUncertainCreation(t *testing.T) {
	f := newServarrContract(t, V3)
	link := f.link()
	if err := link.Apply(context.Background(), f.from, f.to); err != nil {
		t.Fatal(err)
	}
	f.items[0].setField("apiKey", "********")
	if err := link.Apply(context.Background(), f.from, f.to); err != nil {
		t.Fatal(err)
	}
	if f.updates != 0 || f.tests != 3 {
		t.Fatal("masked no-op did not test the stored resource")
	}
	f.items[0].setField("port", float64(9090))
	if err := link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if f.updates != 0 {
		t.Fatal("user connection edit was overwritten")
	}
	f.items[0].setField("port", float64(8080))
	f.items[0].setField("apiKey", "user-edited-key")
	if err := link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	g := newServarrContract(t, V3)
	g.uncertain = true
	if err := g.link().Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if err := g.link().Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if g.creates != 1 {
		t.Fatal("uncertain creation was duplicated")
	}
}

func TestDownloadProviderUnownedRemovalAndUnsupportedSchema(t *testing.T) {
	f := newServarrContract(t, V3)
	item := servarrContractCopy(f.template)
	item["id"], item["name"] = float64(17), "Contract"
	item.setField("host", "contract")
	f.items = []provider{item}
	if err := f.link().Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if err := f.link().Remove(context.Background(), f.from, f.to); err != nil {
		t.Fatal(err)
	}
	if f.deletes != 0 || len(f.items) != 1 {
		t.Fatal("unowned resource was removed")
	}
	g := newServarrContract(t, V3)
	g.template["configContract"] = "WrongSettings"
	if err := g.link().Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if g.creates != 0 || g.tests != 0 {
		t.Fatal("unsupported schema was submitted")
	}
}

func TestDownloadProviderNativeSuccessShapesAndFailedRerun(t *testing.T) {
	for _, result := range []struct {
		status int
		body   string
	}{{200, `{}`}, {200, `"{}"`}, {204, ``}} {
		f := newServarrContract(t, V3)
		f.nativeStatus, f.nativeBody = result.status, result.body
		if err := f.link().Apply(context.Background(), f.from, f.to); err != nil {
			t.Fatal(err)
		}
		record := f.secrets[f.p.ownershipKey(f.from)]
		f.nativeStatus, f.nativeBody = 400, `[{"propertyName":"APIKey","errorMessage":"private-provider-data"}]`
		if err := f.link().Apply(context.Background(), f.from, f.to); err == nil || strings.Contains(err.Error(), "private-provider-data") {
			t.Fatal("rejected native credentials passed or disclosed their response", err)
		}
		if f.creates != 1 || f.updates != 0 || f.secrets[f.p.ownershipKey(f.from)] != record {
			t.Fatal("failed native rerun replaced a recorded connection")
		}
	}
	for _, field := range []string{"port", "tvCategory"} {
		f := newServarrContract(t, V3)
		f.template.setField(field, true)
		if err := f.link().Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("mistyped schema was accepted", err)
		}
		if f.tests != 0 || f.creates != 0 {
			t.Fatal("mistyped schema reached a native test or creation")
		}
	}
}

func TestLegacyDownloadAndApplicationOwnershipRecordsRemainCurrent(t *testing.T) {
	for _, target := range []string{"qbittorrent", "radarr"} {
		t.Run(target, func(t *testing.T) {
			p := providerLink{api: V3, path: "/downloadclient", target: target, implementation: "QBittorrent", name: "qBittorrent", endpointField: "host"}
			if target == "radarr" {
				p = providerLink{api: V1, path: "/applications", target: target, implementation: "Radarr", name: "Radarr", endpointField: "baseUrl"}
			}
			secrets := porttest.Secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "old-password", "RADARR_API_KEY": "old-api-key"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != p.api+p.path {
					t.Errorf("legacy current path made an unexpected request: %s %s", r.Method, r.URL.Path)
				}
				from := &helpers.H{Config: helpers.Config{App: "sonarr", Service: "sonarr", ContainerPort: 8989, Secrets: secrets}}
				to := &helpers.H{Config: helpers.Config{App: target, Service: target, ContainerPort: 7878, Port: 18080, Secrets: secrets}}
				fields, _ := p.connection(from, to)
				item := provider{"id": float64(12), "name": p.name, "implementation": p.implementation, "enable": true, "fields": []any{}}
				for _, field := range fields {
					item["fields"] = append(item["fields"].([]any), map[string]any{"name": field.Name, "value": field.Value})
				}
				json.NewEncoder(w).Encode([]provider{item})
			}))
			t.Cleanup(server.Close)
			from := porttest.New(t, porttest.Options{App: "sonarr", Service: "sonarr", Secrets: secrets}, server, &porttest.Recorder{})
			from.ContainerPort = 8989
			to := helpers.New(helpers.Config{App: target, Service: target, ContainerPort: 7878, Port: 18080, Secrets: secrets, Log: from.Log})
			fields, _ := p.connection(from, to)
			endpoint := "qbittorrent"
			if target == "radarr" {
				endpoint = "http://radarr:7878"
			}
			raw := `{"id":12,"target":"` + target + `","endpoint":"` + endpoint + `","fingerprint":"` + connectionFingerprint(fields) + `"}`
			secrets[p.ownershipKey(from)] = raw
			if current, err := p.current(context.Background(), from, to, false); err != nil || !current {
				t.Fatal(current, err)
			}
			if secrets[p.ownershipKey(from)] != raw {
				t.Fatal("legacy ownership record changed")
			}
		})
	}
}

type protocolContractCase struct {
	implementation, contract, target, oldProtocol, newProtocol string
	credential, urlBase                                        string
	port                                                       int
	username                                                   bool
	family                                                     downloadProtocolFamily
}

func protocolContractCases() []protocolContractCase {
	return []protocolContractCase{
		{"Sabnzbd", "SabnzbdSettings", "sabnzbd", "usenet", "UsenetDownloadProtocol", "apiKey", "", 8080, false, downloadProtocolUsenet},
		{"Nzbget", "NzbgetSettings", "nzbget", "usenet", "UsenetDownloadProtocol", "password", "", 6789, true, downloadProtocolUsenet},
		{"Deluge", "DelugeSettings", "deluge", "torrent", "TorrentDownloadProtocol", "password", "", 8112, false, downloadProtocolTorrent},
		{"Transmission", "TransmissionSettings", "transmission", "torrent", "TorrentDownloadProtocol", "password", "/transmission/", 9091, true, downloadProtocolTorrent},
	}
}

type protocolTrackedSecrets struct {
	values        porttest.Secrets
	reads, writes int
}

func (s *protocolTrackedSecrets) Get(key string) string {
	s.reads++
	return s.values.Get(key)
}

func (s *protocolTrackedSecrets) Set(key, value string) error {
	s.writes++
	return s.values.Set(key, value)
}

func newProtocolContract(t *testing.T, source protocolContractCase, version string) (*servarrContractFixture, *protocolTrackedSecrets, *protocolTrackedSecrets, *atomic.Int64) {
	t.Helper()
	f := newServarrContract(t, V1)
	f.version, f.appName = version, "Lidarr"
	wire := source.oldProtocol
	if version == "3.1.6.5079" {
		wire = source.newProtocol
	} else if version != "3.1.0.4875" {
		t.Fatal("unknown source fixture version")
	}
	f.from.App, f.from.Service = "lidarr", "lidarr"
	f.secrets["LIDARR_API_KEY"] = "consumer-key"
	f.template = provider{"implementation": source.implementation, "configContract": source.contract, "protocol": wire, "priority": float64(1), "fields": []any{}}
	values := map[string]any{"host": "localhost", "port": float64(source.port), "useSsl": false, "urlBase": nil, source.credential: nil, "musicCategory": "music"}
	switch source.implementation {
	case "Sabnzbd":
		values["username"], values["password"] = nil, nil
	case "Nzbget":
		values["username"], values["password"], values["musicCategory"] = "nzbget", "tegbzn6789", "Music"
	case "Deluge":
		values["password"], values["musicCategory"] = "deluge", "lidarr"
	case "Transmission":
		values["username"], values["urlBase"], values["musicCategory"] = nil, "/transmission/", "lidarr"
	}
	for name, value := range values {
		f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": name, "value": value})
	}
	f.p.implementation, f.p.configContract, f.p.target, f.p.name = source.implementation, source.contract, source.target, source.implementation
	f.p.categoryField, f.p.category = "musicCategory", "music"
	f.p.protocol, f.p.protocolFamily = "not-a-selected-wire-value", source.family
	peerCalls := &atomic.Int64{}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/read-only-contract" {
			t.Error("contract callback attempted a peer mutation")
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(peer.Close)
	f.to = porttest.New(t, porttest.Options{App: source.target, Service: source.target, Secrets: f.secrets}, peer, &porttest.Recorder{})
	f.to.ContainerPort = source.port
	f.p.connectionFields = func(_, to *helpers.H) ([]connectionField, error) {
		key := to.Secret("CONTRACT_API_KEY")
		if key == "" {
			return nil, &helpers.ManualError{Hint: "restore the existing target credential"}
		}
		fields := []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", source.urlBase}, {source.credential, key}}
		if source.username {
			fields = append(fields, connectionField{"username", "fixture-admin"})
		}
		return fields, nil
	}
	f.p.verify = func(ctx context.Context, _, to *helpers.H, _ provider) error {
		f.guards++
		if f.guardErr != nil {
			return f.guardErr
		}
		resp, err := to.HTTP(ctx, helpers.Request{Path: "/read-only-contract", NoRedirect: true})
		if err != nil || resp.Status != 200 {
			return errors.New("fixture peer unavailable")
		}
		return nil
	}
	consumer := &protocolTrackedSecrets{values: f.secrets}
	target := &protocolTrackedSecrets{values: f.secrets}
	f.from.Config.Secrets, f.to.Config.Secrets = consumer, target
	resp, err := f.p.request(context.Background(), f.from, "GET", "/system/status", nil)
	var status struct{ AppName, Version string }
	if err != nil || resp.Status != 200 || resp.Decode(&status) != nil || status.AppName != "Lidarr" || status.Version != version {
		t.Fatal("source-family status fixture is inconsistent", err)
	}
	return f, consumer, target, peerCalls
}

func seedProtocolOwnership(t *testing.T, f *servarrContractFixture) string {
	t.Helper()
	fields, err := f.p.connection(f.from, f.to)
	if err != nil {
		t.Fatal(err)
	}
	item := servarrContractCopy(f.template)
	item["id"], item["name"], item["enable"] = float64(41), "existing managed client", true
	item["priority"], item["removeCompletedDownloads"] = float64(9), false
	for _, field := range fields {
		if err := item.setField(field.Name, field.Value); err != nil {
			t.Fatal(err)
		}
	}
	item.setField(f.p.categoryField, "chosen")
	f.items = []provider{item}
	f.nextID = 42
	raw := `{"id":41,"target":"` + f.p.target + `","endpoint":"` + f.to.Service + `","fingerprint":"` + connectionFingerprint(fields) + `"}`
	f.secrets[f.p.ownershipKey(f.from)] = raw
	return raw
}

func TestDownloadProtocolFamiliesPreserveActualWireAndOwnedLifecycle(t *testing.T) {
	for _, source := range protocolContractCases() {
		for _, version := range []string{"3.1.0.4875", "3.1.6.5079"} {
			t.Run(source.implementation+"/"+version, func(t *testing.T) {
				f, consumer, target, peerCalls := newProtocolContract(t, source, version)
				wire := f.template["protocol"]
				raw := seedProtocolOwnership(t, f)
				consumer.writes, target.reads = 0, 0
				if err := f.link().Apply(context.Background(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if f.secrets[f.p.ownershipKey(f.from)] != raw || consumer.writes != 0 || f.creates != 0 || f.updates != 0 || f.items[0].id() != 41 || f.items[0]["protocol"] != wire || f.items[0].field("musicCategory") != "chosen" || f.items[0]["priority"] != float64(9) || f.items[0]["removeCompletedDownloads"] != false {
					t.Fatal("source-consistent old-format ownership was changed on rerun")
				}
				f.items[0].setField(source.credential, "********")
				if err := f.link().Apply(context.Background(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if f.updates != 0 || f.secrets[f.p.ownershipKey(f.from)] != raw {
					t.Fatal("masked rerun rewrote ownership")
				}
				f.secrets["CONTRACT_API_KEY"] = "renewed-accepted-key"
				if err := f.link().Apply(context.Background(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if f.updates != 1 || f.items[0].id() != 41 || f.items[0]["protocol"] != wire || f.items[0].field("musicCategory") != "chosen" || f.items[0]["priority"] != float64(9) || f.items[0]["removeCompletedDownloads"] != false {
					t.Fatal("credential update changed protocol or noncredential choices")
				}
				survivor := servarrContractCopy(f.items[0])
				survivor["id"], survivor["name"] = float64(88), "independent survivor"
				survivor.setField("host", "independent-downloader")
				personal := servarrContractCopy(f.items[0])
				personal["id"], personal["name"] = float64(89), "personal same-target client"
				f.items = append(f.items, survivor, personal)
				beforePeer, beforeNative := peerCalls.Load(), f.tests
				f.guardErr = errors.New("downloader offline")
				delete(f.secrets, "CONTRACT_API_KEY")
				target.reads = 0
				if err := f.link().Remove(context.Background(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				if target.reads != 0 || target.writes != 0 || peerCalls.Load() != beforePeer || f.tests != beforeNative || len(f.items) != 2 || f.items[0].id() != 88 || f.items[1].id() != 89 || f.items[1].field("host") != f.to.Service || f.secrets[f.p.ownershipKey(f.from)] != "" {
					t.Fatal("removal read the downloader or changed the survivor")
				}
				reads := f.resourceReads + f.schemaReads
				unavailableBody := `false`
				f.schemaBody, f.resourceBody = &unavailableBody, &unavailableBody
				consumer.writes = 0
				if err := f.link().Remove(context.Background(), f.from, f.to); err != nil || f.resourceReads+f.schemaReads != reads || consumer.writes != 0 || target.reads != 0 || target.writes != 0 || peerCalls.Load() != beforePeer {
					t.Fatal("unowned removal discovered or mutated remote state", err)
				}
				f.schemaBody, f.resourceBody = nil, nil
				f.guardErr = nil
				f.secrets["CONTRACT_API_KEY"] = "renewed-accepted-key"
				if err := f.link().Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 0 || target.reads != 0 || f.tests != beforeNative || consumer.writes != 0 {
					t.Fatal("reinstall tried to claim the personal same-target client", err)
				}
				f.items[1].setField("host", "person-selected-downloader")
				if err := f.link().Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 0 || target.reads != 0 || f.tests != beforeNative || consumer.writes != 0 || peerCalls.Load() != beforePeer {
					t.Fatal("reinstall changed selection while enabled same-family survivors remain", err)
				}
				if len(f.items) != 2 || f.items[0].id() != 88 || f.items[1].id() != 89 || f.items[0]["enable"] != true || f.items[1]["enable"] != true || f.items[1].field("host") != "person-selected-downloader" {
					t.Fatal("reinstall changed an enabled survivor")
				}
				for _, submitted := range append(append(f.nativeItems, f.createdItems...), f.updatedItems...) {
					if submitted["protocol"] != wire {
						t.Fatal("a request used a preferred spelling instead of the actual wire protocol")
					}
				}
			})
		}
	}
}

type downloadSelectionSecrets struct {
	values             porttest.Secrets
	adminReads, writes int
}

func (s *downloadSelectionSecrets) Get(key string) string {
	if key == "ADMIN_USER" || key == "ADMIN_PASSWORD" {
		s.adminReads++
	}
	return s.values.Get(key)
}

func (s *downloadSelectionSecrets) Set(key, value string) error {
	s.writes++
	return s.values.Set(key, value)
}

func downloadSelectionCases() []protocolContractCase {
	return append(protocolContractCases(), protocolContractCase{"QBittorrent", "QBittorrentSettings", "qbittorrent", "torrent", "TorrentDownloadProtocol", "password", "", 8080, true, downloadProtocolSingle})
}

type downloadSelectionConsumer struct {
	id, api, field, version string
	modern                  bool
}

func downloadSelectionConsumers() []downloadSelectionConsumer {
	return []downloadSelectionConsumer{
		{"sonarr", V3, "tvCategory", "4.0.20.3014", false},
		{"radarr", V3, "movieCategory", "6.4.4.10685", false},
		{"lidarr", V1, "musicCategory", "3.1.0.4875", false},
		{"lidarr", V1, "musicCategory", "3.1.6.5079", true},
	}
}

func newDownloadSelectionContract(t *testing.T, source protocolContractCase, consumer downloadSelectionConsumer) (*servarrContractFixture, *downloadSelectionSecrets, *protocolTrackedSecrets) {
	t.Helper()
	f := newServarrContract(t, consumer.api)
	f.from.App, f.from.Service = consumer.id, consumer.id
	f.version, f.appName = consumer.version, consumer.id
	f.secrets[f.from.Key("api_key")] = "consumer-key"
	f.secrets["ADMIN_USER"], f.secrets["ADMIN_PASSWORD"] = "accepted-user", "accepted-password"
	wire := source.oldProtocol
	if consumer.modern {
		wire = source.newProtocol
	}
	f.template = provider{"implementation": source.implementation, "configContract": source.contract, "protocol": wire, "priority": float64(1), "tags": []any{}, "fields": []any{}}
	values := map[string]any{"host": "localhost", "port": float64(source.port), "useSsl": false, "urlBase": source.urlBase, source.credential: "", consumer.field: "chosen"}
	if source.username || source.implementation == "Sabnzbd" {
		values["username"] = ""
	}
	if source.implementation == "Sabnzbd" {
		values["password"] = ""
	}
	for name, value := range values {
		f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": name, "value": value})
	}
	f.p.target, f.p.implementation, f.p.name, f.p.configContract = source.target, source.implementation, source.implementation, source.contract
	f.p.categoryField, f.p.category, f.p.protocolFamily = consumer.field, "chosen", source.family
	f.to.App, f.to.Service, f.to.ContainerPort, f.to.Port = source.target, source.target, source.port, 18080
	if source.family == downloadProtocolSingle {
		f.p.connectionFields, f.p.verify = nil, nil
		f.p.configContract, f.p.protocol = "", ""
	} else {
		f.p.connectionFields = func(_, to *helpers.H) ([]connectionField, error) {
			fields := []connectionField{{"host", to.Service}, {"port", float64(to.ContainerPort)}, {"useSsl", false}, {"urlBase", source.urlBase}, {source.credential, to.Secret("CONTRACT_API_KEY")}}
			if source.username {
				fields = append(fields, connectionField{"username", "accepted-user"})
			}
			return fields, nil
		}
	}
	fromSecrets := &downloadSelectionSecrets{values: f.secrets}
	targetSecrets := &protocolTrackedSecrets{values: f.secrets}
	f.from.Config.Secrets, f.to.Config.Secrets = fromSecrets, targetSecrets
	return f, fromSecrets, targetSecrets
}

func downloadSelectionIncumbent(f *servarrContractFixture, wire string) (provider, provider) {
	schema := servarrContractCopy(f.template)
	schema["implementation"], schema["configContract"], schema["protocol"] = "IndependentClient", "IndependentSettings", wire
	item := servarrContractCopy(schema)
	item["id"], item["name"], item["enable"], item["priority"] = float64(10), "person-selected client", true, float64(7)
	item.setField("host", "independent-downloader")
	return schema, item
}

func (f *servarrContractFixture) downloadSelectionLink() app.Link {
	if f.p.target == "qbittorrent" {
		return QBittorrentLink(f.p.api, f.p.categoryField, f.p.category)
	}
	return f.link()
}

type downloadSelectionRequest struct {
	wire          string
	tags          []any
	indexerClient int64
	blocked       []int64
	filterBlocked bool
}

func (r downloadSelectionRequest) pick(items []provider, last *int64) int64 {
	available, matching := []provider{}, []provider{}
	for _, item := range items {
		if item["enable"] != true || item["protocol"] != r.wire {
			continue
		}
		tags, _ := item["tags"].([]any)
		if len(tags) == 0 {
			available = append(available, item)
		}
		if slices.ContainsFunc(tags, func(tag any) bool { return slices.Contains(r.tags, tag) }) {
			matching = append(matching, item)
		}
	}
	if len(matching) != 0 {
		available = matching
	}
	if r.indexerClient > 0 {
		for _, item := range available {
			if item.id() == r.indexerClient && (!r.filterBlocked || !slices.Contains(r.blocked, item.id())) {
				return item.id()
			}
		}
		return 0
	}
	nonBlocked := []provider{}
	for _, item := range available {
		if !slices.Contains(r.blocked, item.id()) {
			nonBlocked = append(nonBlocked, item)
		}
	}
	if len(nonBlocked) != 0 || r.filterBlocked {
		available = nonBlocked
	}
	if len(available) == 0 {
		return 0
	}
	slices.SortFunc(available, func(a, b provider) int {
		if a["priority"] != b["priority"] {
			return int(a["priority"].(float64) - b["priority"].(float64))
		}
		return int(a.id() - b.id())
	})
	selected := available[0]
	for _, item := range available {
		if item["priority"] == available[0]["priority"] && item.id() > *last {
			selected = item
			break
		}
	}
	*last = selected.id()
	return *last
}

func (r downloadSelectionRequest) sequence(items []provider) []int64 {
	last := int64(0)
	result := []int64{}
	for range 4 {
		result = append(result, r.pick(items, &last))
	}
	return result
}

func TestDownloadCreationSelectorExplainsRoundRobinTakeoverAndFallback(t *testing.T) {
	incumbent := provider{"id": float64(10), "enable": true, "protocol": "torrent", "priority": float64(1), "tags": []any{}}
	candidate := provider{"id": float64(20), "enable": true, "protocol": "torrent", "priority": float64(1), "tags": []any{}}
	r := downloadSelectionRequest{wire: "torrent"}
	if !slices.Equal(r.sequence([]provider{incumbent}), []int64{10, 10, 10, 10}) || !slices.Equal(r.sequence([]provider{incumbent, candidate}), []int64{10, 20, 10, 20}) {
		t.Fatal("equal priority does not model the pinned selector's round robin")
	}
	incumbent["priority"] = float64(7)
	if !slices.Equal(r.sequence([]provider{incumbent, candidate}), []int64{20, 20, 20, 20}) {
		t.Fatal("new priority 1 did not take over from priority 7")
	}
	incumbent["priority"], candidate["priority"] = float64(1), float64(7)
	r.blocked = []int64{10}
	if !slices.Equal(r.sequence([]provider{incumbent, candidate}), []int64{20, 20, 20, 20}) {
		t.Fatal("a lower-priority candidate was not a new blocked-client fallback")
	}
}

func TestDownloadCreationHoldsAllFiveBeforeTargetNativeAndPrivateWrites(t *testing.T) {
	for _, source := range downloadSelectionCases() {
		for _, consumer := range downloadSelectionConsumers() {
			for _, scenario := range []string{"equal-priority", "takeover", "worse-priority", "tagged", "nonmatching-tag", "unreadable-routing", "indexer-pinned", "missing-enable", "null-enable", "string-enable", "numeric-enable", "missing-protocol", "null-protocol", "numeric-protocol", "unknown-protocol", "crossed-same-family", "crossed-protocol", "missing-schema", "duplicate-schema", "wrong-contract", "wrong-field-type", "extra-field"} {
				t.Run(source.implementation+"/"+consumer.id+"/"+consumer.version+"/"+scenario, func(t *testing.T) {
					f, fromSecrets, targetSecrets := newDownloadSelectionContract(t, source, consumer)
					wire := f.template["protocol"].(string)
					other := "torrent"
					if consumer.modern {
						other = "TorrentDownloadProtocol"
					}
					if source.oldProtocol == "torrent" {
						other = "usenet"
						if consumer.modern {
							other = "UsenetDownloadProtocol"
						}
					}
					schema, incumbent := downloadSelectionIncumbent(f, other)
					r := downloadSelectionRequest{wire: wire}
					switch scenario {
					case "equal-priority", "takeover", "worse-priority", "tagged", "nonmatching-tag", "unreadable-routing", "indexer-pinned":
						schema["protocol"], incumbent["protocol"] = wire, wire
						if scenario == "equal-priority" {
							incumbent["priority"] = float64(1)
						}
						if scenario == "worse-priority" {
							f.template["priority"], incumbent["priority"] = float64(7), float64(1)
							r.blocked = []int64{10}
						}
						if scenario == "tagged" {
							incumbent["tags"], r.tags = []any{float64(4)}, []any{float64(4)}
						}
						if scenario == "nonmatching-tag" {
							incumbent["tags"], r.tags = []any{float64(4)}, []any{float64(9)}
						}
						if scenario == "unreadable-routing" {
							delete(incumbent, "priority")
							incumbent["tags"] = "unreadable"
						}
						if scenario == "indexer-pinned" {
							r.indexerClient = 10
						}
					case "missing-enable":
						delete(incumbent, "enable")
					case "null-enable":
						incumbent["enable"] = nil
					case "string-enable":
						incumbent["enable"] = "false"
					case "numeric-enable":
						incumbent["enable"] = float64(0)
					case "missing-protocol":
						delete(incumbent, "protocol")
					case "null-protocol":
						incumbent["protocol"] = nil
					case "numeric-protocol":
						incumbent["protocol"] = float64(1)
					case "unknown-protocol":
						incumbent["protocol"] = "future-wire"
					case "crossed-same-family":
						incumbent["protocol"] = source.newProtocol
						if consumer.modern {
							incumbent["protocol"] = source.oldProtocol
						}
						schema["protocol"] = incumbent["protocol"]
					case "crossed-protocol":
						schema["protocol"] = wire
					case "wrong-contract":
						schema["configContract"] = "UnrelatedSettings"
					case "wrong-field-type":
						incumbent.setField("port", true)
					case "extra-field":
						incumbent["fields"] = append(incumbent["fields"].([]any), map[string]any{"name": "future-field", "value": true})
					}
					f.items = []provider{incumbent}
					f.schemas = []provider{f.template, schema}
					if scenario == "missing-schema" {
						f.schemas = []provider{f.template}
					}
					if scenario == "duplicate-schema" {
						f.schemas = append(f.schemas, servarrContractCopy(schema))
					}
					before, err := json.Marshal(f.items)
					if err != nil {
						t.Fatal(err)
					}
					selected := r.sequence(f.items)
					if err := f.downloadSelectionLink().Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "priorities, tags, indexer assignments or enabled flags") {
						t.Fatal("unproven fresh selection did not return actionable Manual", err)
					}
					after, _ := json.Marshal(f.items)
					if string(after) != string(before) || !slices.Equal(selected, r.sequence(f.items)) || fromSecrets.adminReads != 0 || fromSecrets.writes != 0 || targetSecrets.reads != 0 || targetSecrets.writes != 0 || f.guards != 0 || f.tests != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || f.secrets[f.p.ownershipKey(f.from)] != "" {
						t.Fatal("selection refusal changed selection/resources or reached target/native/private activity")
					}
				})
			}
		}
	}
}

func TestDownloadCreationEmptyDisabledAndOppositeFamilyPreserveWireAndSelection(t *testing.T) {
	for _, source := range downloadSelectionCases() {
		for _, consumer := range downloadSelectionConsumers() {
			for _, scenario := range []string{"empty", "disabled", "opposite"} {
				t.Run(source.implementation+"/"+consumer.id+"/"+consumer.version+"/"+scenario, func(t *testing.T) {
					f, _, _ := newDownloadSelectionContract(t, source, consumer)
					wire := f.template["protocol"]
					other := "torrent"
					if consumer.modern {
						other = "TorrentDownloadProtocol"
					}
					if source.oldProtocol == "torrent" {
						other = "usenet"
						if consumer.modern {
							other = "UsenetDownloadProtocol"
						}
					}
					schema, incumbent := downloadSelectionIncumbent(f, other)
					f.schemas = []provider{f.template, schema}
					if scenario == "disabled" {
						incumbent["enable"], incumbent["protocol"] = false, "unproven-disabled-wire"
					}
					if scenario != "empty" {
						f.items = []provider{incumbent}
					}
					before := servarrContractCopy(incumbent)
					r := downloadSelectionRequest{wire: other}
					selected := r.sequence(f.items)
					if err := f.downloadSelectionLink().Apply(t.Context(), f.from, f.to); err != nil {
						t.Fatal(err)
					}
					created := f.items[len(f.items)-1]
					if created["enable"] != true || created["protocol"] != wire || f.creates != 1 || !slices.Equal(selected, r.sequence(f.items)) {
						t.Fatal("fresh admission normalized wire, created a disabled resource or changed an opposite selector")
					}
					if scenario != "empty" {
						got, _ := json.Marshal(f.items[0])
						want, _ := json.Marshal(before)
						if string(got) != string(want) {
							t.Fatal("fresh creation modified an existing resource")
						}
					}
					if source.family == downloadProtocolSingle && (f.p.protocolFamily != downloadProtocolSingle || f.p.protocol != "" || f.p.connectionFields != nil || f.tests != 0 || f.guards != 0) {
						t.Fatal("narrow qBittorrent creation admission changed its owned verification contract")
					}
				})
			}
		}
	}
}

func TestDownloadCreationRechecksInventoryBetweenCheckAndDo(t *testing.T) {
	f, fromSecrets, targetSecrets := newDownloadSelectionContract(t, downloadSelectionCases()[0], downloadSelectionConsumers()[0])
	schema, incumbent := downloadSelectionIncumbent(f, "usenet")
	f.schemas = []provider{f.template, schema}
	f.beforeResourceRead = func() {
		if f.resourceReads == 2 {
			f.items = []provider{incumbent}
		}
	}
	if err := f.link().Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("Step Do reused the earlier empty inventory", err)
	}
	if f.resourceReads != 2 || f.schemaReads != 2 || fromSecrets.writes != 0 || targetSecrets.reads != 0 || f.tests != 0 || f.guards != 0 || f.creates != 0 {
		t.Fatal("changed selection reached target/native/intent or duplicated authenticated reads")
	}
}

func TestDownloadCreationLegacyCandidateRejectsUnreadableAuthenticatedSnapshots(t *testing.T) {
	for _, consumer := range downloadSelectionConsumers() {
		for _, fault := range []string{"missing-key", "rejected-key", "inventory-denied", "schema-denied", "inventory-auth-false", "schema-auth-false", "inventory-null", "schema-null", "inventory-malformed", "schema-malformed", "duplicate-resource", "duplicate-field", "duplicate-schema", "missing-schema-field", "mistyped-schema-field", "wrong-contract", "wrong-protocol"} {
			t.Run(consumer.id+"/"+consumer.version+"/"+fault, func(t *testing.T) {
				sources := downloadSelectionCases()
				f, fromSecrets, targetSecrets := newDownloadSelectionContract(t, sources[len(sources)-1], consumer)
				privateBody := `{"status":false,"error":"fixture-private-response"}`
				nullBody, malformedBody := `null`, `{"private":"fixture-private-response"`
				switch fault {
				case "missing-key":
					delete(f.secrets, f.from.Key("api_key"))
				case "rejected-key":
					f.secrets[f.from.Key("api_key")] = "rejected-key"
				case "inventory-denied":
					f.resourceStatus = 403
				case "schema-denied":
					f.schemaStatus = 401
				case "inventory-auth-false":
					f.resourceBody = &privateBody
				case "schema-auth-false":
					f.schemaBody = &privateBody
				case "inventory-null":
					f.resourceBody = &nullBody
				case "schema-null":
					f.schemaBody = &nullBody
				case "inventory-malformed":
					f.resourceBody = &malformedBody
				case "schema-malformed":
					f.schemaBody = &malformedBody
				case "duplicate-resource":
					_, item := downloadSelectionIncumbent(f, "usenet")
					f.items = []provider{item, servarrContractCopy(item)}
				case "duplicate-field":
					f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": "host", "value": "localhost"})
				case "duplicate-schema":
					f.schemas = []provider{f.template, servarrContractCopy(f.template)}
				case "missing-schema-field":
					f.template["fields"] = slices.DeleteFunc(f.template["fields"].([]any), func(raw any) bool { return raw.(map[string]any)["name"] == "password" })
				case "mistyped-schema-field":
					f.template.setField("port", true)
				case "wrong-contract":
					f.template["configContract"] = "UnknownSettings"
				case "wrong-protocol":
					f.template["protocol"] = "usenet"
				}
				if err := f.downloadSelectionLink().Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "fixture-private-response") {
					t.Fatal("unreadable creation facts did not return safe Manual recovery", err)
				}
				if fromSecrets.adminReads != 0 || fromSecrets.writes != 0 || targetSecrets.reads != 0 || targetSecrets.writes != 0 || f.guards != 0 || f.tests != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || f.secrets[f.p.ownershipKey(f.from)] != "" {
					t.Fatal("unreadable consumer snapshots reached target/native/private effects")
				}
			})
		}
	}
}

func TestDownloadCreationCannotReplaceUncertainMissingOrDisabledOwnership(t *testing.T) {
	for _, source := range downloadSelectionCases() {
		for _, state := range []string{"uncertain", "missing", "disabled"} {
			t.Run(source.implementation+"/"+state, func(t *testing.T) {
				f, fromSecrets, targetSecrets := newDownloadSelectionContract(t, source, downloadSelectionConsumers()[3])
				raw := seedProtocolOwnership(t, f)
				if state == "uncertain" {
					raw = strings.Replace(raw, `"id":41`, `"id":0`, 1)
					f.secrets[f.p.ownershipKey(f.from)] = raw
				}
				if state == "missing" {
					f.items = []provider{}
				}
				if state == "disabled" {
					f.items[0]["enable"] = false
				}
				fromSecrets.adminReads, targetSecrets.reads = 0, 0
				if err := f.downloadSelectionLink().Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.secrets[f.p.ownershipKey(f.from)] != raw {
					t.Fatal("existing ownership was reclassified as a fresh creation", err)
				}
				if fromSecrets.adminReads != 0 || fromSecrets.writes != 0 || targetSecrets.reads != 0 || targetSecrets.writes != 0 || f.guards != 0 || f.tests != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 {
					t.Fatal("uncertain, missing or disabled ownership reached target/native/private effects")
				}
				if state == "uncertain" && (f.resourceReads != 0 || f.schemaReads != 0) {
					t.Fatal("uncertain creation was discovered or reissued")
				}
			})
		}
	}
}

func TestDownloadCreationUsesTheAdmittedTemplateWithoutAnotherCreationLookup(t *testing.T) {
	for _, source := range downloadSelectionCases() {
		t.Run(source.implementation, func(t *testing.T) {
			f, _, _ := newDownloadSelectionContract(t, source, downloadSelectionConsumers()[3])
			if f.p.connectionFields != nil {
				connection := f.p.connectionFields
				f.p.connectionFields = func(from, to *helpers.H) ([]connectionField, error) {
					fields, err := connection(from, to)
					f.template["priority"] = float64(37)
					return fields, err
				}
			}
			if err := f.downloadSelectionLink().Apply(t.Context(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.createdItems[0]["priority"] != float64(1) || f.createdItems[0]["protocol"] != f.template["protocol"] {
				t.Fatal("creation replaced the admitted template after credential resolution")
			}
			wantReads := 5
			if source.family == downloadProtocolSingle {
				wantReads = 2
			}
			if f.schemaReads != wantReads || f.resourceReads != 3 {
				t.Fatal("creation added a schema or inventory lookup instead of reusing the read snapshot")
			}
		})
	}
}

func TestDownloadCreationExistingOwnershipPreservesIntentionalEnabledSelection(t *testing.T) {
	for _, source := range downloadSelectionCases() {
		for _, consumer := range downloadSelectionConsumers() {
			t.Run(source.implementation+"/"+consumer.id+"/"+consumer.version, func(t *testing.T) {
				f, fromSecrets, targetSecrets := newDownloadSelectionContract(t, source, consumer)
				raw := seedProtocolOwnership(t, f)
				f.items[0]["tags"] = []any{float64(4)}
				_, incumbent := downloadSelectionIncumbent(f, f.template["protocol"].(string))
				incumbent["tags"] = []any{float64(4)}
				f.items = append(f.items, incumbent)
				r := downloadSelectionRequest{wire: f.template["protocol"].(string), tags: []any{float64(4)}}
				before := r.sequence(f.items)
				link := f.downloadSelectionLink()
				if err := link.Apply(t.Context(), f.from, f.to); err != nil || f.secrets[f.p.ownershipKey(f.from)] != raw || fromSecrets.writes != 0 || f.creates != 0 || f.updates != 0 || !slices.Equal(before, r.sequence(f.items)) {
					t.Fatal("owned rerun applied creation policy or changed the old record/selection", err)
				}
				f.items[0].setField(source.credential, "********")
				if err := link.Apply(t.Context(), f.from, f.to); err != nil || f.secrets[f.p.ownershipKey(f.from)] != raw || fromSecrets.writes != 0 || f.updates != 0 || !slices.Equal(before, r.sequence(f.items)) {
					t.Fatal("masked owned rerun changed intentional selection or legacy ownership", err)
				}
				want := servarrContractCopy(f.items[0])
				if source.family == downloadProtocolSingle {
					f.secrets["ADMIN_PASSWORD"] = "accepted-replacement"
				} else {
					f.secrets["CONTRACT_API_KEY"] = "accepted-replacement"
				}
				want.setField(source.credential, "accepted-replacement")
				if err := link.Apply(t.Context(), f.from, f.to); err != nil {
					t.Fatal(err)
				}
				gotData, _ := json.Marshal(f.items[0])
				wantData, _ := json.Marshal(want)
				if string(gotData) != string(wantData) || f.updates != 1 || f.items[0].id() != 41 || !slices.Equal(before, r.sequence(f.items)) {
					t.Fatal("credential recovery modified an intentional noncredential selection choice")
				}
				native := f.tests
				targetSecrets.reads, fromSecrets.adminReads = 0, 0
				delete(f.secrets, "CONTRACT_API_KEY")
				delete(f.secrets, "ADMIN_PASSWORD")
				if err := link.Remove(t.Context(), f.from, f.to); err != nil || len(f.items) != 1 || f.items[0].id() != 10 || f.items[0]["enable"] != true || targetSecrets.reads != 0 || fromSecrets.adminReads != 0 || f.tests != native {
					t.Fatal("owned removal read target credentials or changed an enabled survivor", err)
				}
				fromSecrets.writes = 0
				if err := link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || targetSecrets.reads != 0 || fromSecrets.adminReads != 0 || fromSecrets.writes != 0 || f.tests != native || f.creates != 0 {
					t.Fatal("reinstall changed the remaining enabled selection", err)
				}
			})
		}
	}
}

func TestDownloadCreationSelectorRetainsTagsIndexerPinAndBlockedPolicy(t *testing.T) {
	untagged := provider{"id": float64(10), "enable": true, "protocol": "torrent", "priority": float64(1), "tags": []any{}}
	tagged := provider{"id": float64(20), "enable": true, "protocol": "torrent", "priority": float64(7), "tags": []any{float64(4)}}
	items := []provider{untagged, tagged}
	for _, tc := range []struct {
		request downloadSelectionRequest
		want    int64
	}{
		{downloadSelectionRequest{wire: "torrent", tags: []any{float64(4)}}, 20},
		{downloadSelectionRequest{wire: "torrent", tags: []any{float64(9)}}, 10},
		{downloadSelectionRequest{wire: "torrent", indexerClient: 20}, 0},
		{downloadSelectionRequest{wire: "torrent", tags: []any{float64(4)}, indexerClient: 20}, 20},
		{downloadSelectionRequest{wire: "torrent", blocked: []int64{10}, filterBlocked: true}, 0},
		{downloadSelectionRequest{wire: "torrent", blocked: []int64{10}}, 10},
		{downloadSelectionRequest{wire: "usenet"}, 0},
	} {
		last := int64(0)
		if got := tc.request.pick(items, &last); got != tc.want {
			t.Fatal("test selector did not match the pinned route/filter order", got, tc.want)
		}
	}
}

func TestDownloadProtocolFamilyAdmissionIsClosedAndConsumerBounded(t *testing.T) {
	for _, source := range protocolContractCases() {
		for _, consumer := range []struct{ app, api, field string }{{"sonarr", V3, "tvCategory"}, {"radarr", V3, "movieCategory"}, {"lidarr", V1, "musicCategory"}} {
			p := providerLink{api: consumer.api, categoryField: consumer.field, protocolFamily: source.family}
			from := helpers.New(helpers.Config{App: consumer.app})
			if !p.admitsDownloadProtocol(from, source.oldProtocol) || p.admitsDownloadProtocol(from, source.newProtocol) != (consumer.app == "lidarr") {
				t.Fatal("known source-family admission is incorrect", source.implementation, consumer.app)
			}
			for _, invalid := range []any{nil, true, float64(1), []any{}, map[string]any{}, "", "unknown", " " + source.oldProtocol, strings.ToUpper(source.oldProtocol), source.newProtocol + " "} {
				if p.admitsDownloadProtocol(from, invalid) {
					t.Fatal("an unknown or coerced protocol was admitted")
				}
			}
			for _, other := range protocolContractCases() {
				if source.family != other.family && (p.admitsDownloadProtocol(from, other.oldProtocol) || p.admitsDownloadProtocol(from, other.newProtocol)) {
					t.Fatal("a sibling protocol family was admitted")
				}
			}
			p.protocolFamily = downloadProtocolFamily(255)
			if p.admitsDownloadProtocol(from, source.oldProtocol) {
				t.Fatal("an unsupported family option was admitted")
			}
			p.protocolFamily, p.protocol = downloadProtocolSingle, source.oldProtocol
			if !p.admitsDownloadProtocol(from, source.oldProtocol) || p.admitsDownloadProtocol(from, source.newProtocol) {
				t.Fatal("the singleton default was widened")
			}
		}
		for _, invalid := range []struct{ app, api, field string }{{"unknown", V1, "musicCategory"}, {"lidarr", V3, "musicCategory"}, {"lidarr", V1, "tvCategory"}, {"sonarr", V1, "tvCategory"}, {"radarr", V3, "tvCategory"}, {"lidarr", "/api/v2", "musicCategory"}} {
			p := providerLink{api: invalid.api, categoryField: invalid.field, protocolFamily: source.family}
			if p.admitsDownloadProtocol(helpers.New(helpers.Config{App: invalid.app}), source.oldProtocol) || p.admitsDownloadProtocol(helpers.New(helpers.Config{App: invalid.app}), source.newProtocol) {
				t.Fatal("an unsupported consumer/API/category tuple was admitted")
			}
		}
	}
}

func TestDownloadProtocolPreflightFailuresCannotReachPeerOrWrites(t *testing.T) {
	faults := []string{"schema-crossed", "resource-crossed", "schema-sibling", "resource-sibling", "schema-null", "resource-number", "schema-casing", "schema-whitespace", "schema-unknown", "schema-contract", "resource-contract", "schema-implementation", "duplicate-schema", "duplicate-resource", "schema-missing-field", "resource-missing-field", "schema-field-type", "resource-field-type", "schema-auth-false", "resource-auth-false", "schema-malformed", "resource-malformed", "schema-unavailable", "resource-unavailable", "missing-consumer-key", "rejected-consumer-key", "unknown-option", "schema-missing-credential", "schema-credential-type", "resource-credential-type", "schema-duplicate-field", "resource-extra-field", "unknown-consumer", "wrong-category-field", "absent-resource-schema-unavailable"}
	creationFaults := []string{"schema-sibling", "schema-null", "schema-casing", "schema-whitespace", "schema-unknown", "schema-contract", "schema-implementation", "duplicate-schema", "schema-missing-field", "schema-field-type", "schema-auth-false", "resource-auth-false", "schema-malformed", "resource-malformed", "schema-unavailable", "resource-unavailable", "missing-consumer-key", "rejected-consumer-key", "unknown-option", "schema-missing-credential", "schema-credential-type", "schema-duplicate-field", "unknown-consumer", "wrong-category-field"}
	for _, source := range protocolContractCases() {
		for _, version := range []string{"3.1.0.4875", "3.1.6.5079"} {
			for _, operation := range []string{"create", "apply", "remove"} {
				cases := faults
				if operation == "create" {
					cases = creationFaults
				}
				for _, fault := range cases {
					t.Run(source.implementation+"/"+version+"/"+operation+"/"+fault, func(t *testing.T) {
						f, consumer, target, peerCalls := newProtocolContract(t, source, version)
						raw := ""
						if operation != "create" {
							raw = seedProtocolOwnership(t, f)
						}
						other := source.newProtocol
						if version == "3.1.6.5079" {
							other = source.oldProtocol
						}
						sibling := "usenet"
						if source.oldProtocol == "usenet" {
							sibling = "torrent"
						}
						privateBody := `{"status":false,"error":"fixture-private-response"}`
						malformedBody := `{"private":"fixture-private-response"`
						switch fault {
						case "schema-crossed":
							f.template["protocol"] = other
						case "resource-crossed":
							f.items[0]["protocol"] = other
						case "schema-sibling":
							f.template["protocol"] = sibling
						case "resource-sibling":
							f.items[0]["protocol"] = sibling
						case "schema-null":
							f.template["protocol"] = nil
						case "resource-number":
							f.items[0]["protocol"] = float64(1)
						case "schema-casing":
							f.template["protocol"] = strings.ToUpper(source.oldProtocol)
						case "schema-whitespace":
							f.template["protocol"] = source.oldProtocol + " "
						case "schema-unknown":
							f.template["protocol"] = "unknown"
						case "schema-contract":
							f.template["configContract"] = "WrongSettings"
						case "resource-contract":
							f.items[0]["configContract"] = "WrongSettings"
						case "schema-implementation":
							f.template["implementation"] = "WrongClient"
						case "duplicate-schema":
							f.schemas = []provider{servarrContractCopy(f.template), servarrContractCopy(f.template)}
						case "duplicate-resource":
							f.items = append(f.items, servarrContractCopy(f.items[0]))
						case "schema-missing-field":
							f.template["fields"] = slices.DeleteFunc(f.template["fields"].([]any), func(raw any) bool { return raw.(map[string]any)["name"] == "port" })
						case "resource-missing-field":
							f.items[0]["fields"] = slices.DeleteFunc(f.items[0]["fields"].([]any), func(raw any) bool { return raw.(map[string]any)["name"] == "port" })
						case "schema-field-type":
							f.template.setField("port", true)
						case "resource-field-type":
							f.items[0].setField("port", true)
						case "schema-auth-false":
							f.schemaBody = &privateBody
						case "resource-auth-false":
							f.resourceBody = &privateBody
						case "schema-malformed":
							f.schemaBody = &malformedBody
						case "resource-malformed":
							f.resourceBody = &malformedBody
						case "schema-unavailable":
							f.schemaStatus = 403
						case "resource-unavailable":
							f.resourceStatus = 503
						case "missing-consumer-key":
							delete(f.secrets, "LIDARR_API_KEY")
						case "rejected-consumer-key":
							f.secrets["LIDARR_API_KEY"] = "rejected-key"
						case "unknown-option":
							f.p.protocolFamily = downloadProtocolFamily(255)
						case "schema-missing-credential":
							f.template["fields"] = slices.DeleteFunc(f.template["fields"].([]any), func(raw any) bool { return raw.(map[string]any)["name"] == source.credential })
						case "schema-credential-type":
							f.template.setField(source.credential, true)
						case "resource-credential-type":
							f.items[0].setField(source.credential, true)
						case "schema-duplicate-field":
							f.template["fields"] = append(f.template["fields"].([]any), map[string]any{"name": "host", "value": "localhost"})
						case "resource-extra-field":
							f.items[0]["fields"] = append(f.items[0]["fields"].([]any), map[string]any{"name": "unexpectedField", "value": true})
						case "unknown-consumer":
							f.from.App = "unsupported-arr"
							f.secrets[f.from.Key("api_key")] = "consumer-key"
							f.secrets[f.p.ownershipKey(f.from)] = raw
						case "wrong-category-field":
							f.p.categoryField = "tvCategory"
						case "absent-resource-schema-unavailable":
							f.items = []provider{}
							f.schemaStatus = 503
						}
						consumer.writes, target.reads = 0, 0
						var err error
						if operation == "remove" {
							err = f.link().Remove(context.Background(), f.from, f.to)
						} else {
							err = f.link().Apply(context.Background(), f.from, f.to)
						}
						if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "fixture-private-response") || strings.Contains(err.Error(), "consumer-key") {
							t.Fatal("invalid consumer contract did not return safe Manual recovery", err)
						}
						if f.guards != 0 || peerCalls.Load() != 0 || f.tests != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || consumer.writes != 0 || target.reads != 0 || target.writes != 0 || f.secrets[f.p.ownershipKey(f.from)] != raw {
							t.Fatal("invalid consumer contract reached target/native/resource/ownership activity")
						}
					})
				}
			}
		}
	}
}

func TestDownloadProtocolResponseMismatchCannotAdvanceOwnership(t *testing.T) {
	for _, source := range protocolContractCases() {
		for _, version := range []string{"3.1.0.4875", "3.1.6.5079"} {
			for _, operation := range []string{"create", "update"} {
				t.Run(source.implementation+"/"+version+"/"+operation, func(t *testing.T) {
					f, consumer, _, _ := newProtocolContract(t, source, version)
					raw := ""
					if operation == "update" {
						raw = seedProtocolOwnership(t, f)
						f.secrets["CONTRACT_API_KEY"] = "renewed-accepted-key"
					}
					other := source.newProtocol
					if version == "3.1.6.5079" {
						other = source.oldProtocol
					}
					f.responseEdit = func(item provider) { item["protocol"] = other }
					consumer.writes = 0
					if err := f.link().Apply(context.Background(), f.from, f.to); err == nil {
						t.Fatal("a different admitted response literal was accepted")
					}
					if f.deletes != 0 || f.items[0]["protocol"] != f.template["protocol"] {
						t.Fatal("response uncertainty caused an undo or protocol rewrite")
					}
					if operation == "create" {
						var intent providerOwnership
						if err := json.Unmarshal([]byte(f.secrets[f.p.ownershipKey(f.from)]), &intent); err != nil || intent.ID != 0 || intent.Fingerprint == "" || consumer.writes != 1 || f.creates != 1 {
							t.Fatal("uncertain creation acquired an owned ID or lost its intent", err)
						}
						writes, nativeTests, guards := consumer.writes, f.tests, f.guards
						if err := f.link().Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || consumer.writes != writes || f.creates != 1 || f.tests != nativeTests || f.guards != guards {
							t.Fatal("uncertain creation was retried or its ownership advanced", err)
						}
					} else if f.updates != 1 || f.secrets[f.p.ownershipKey(f.from)] != raw || consumer.writes != 0 {
						t.Fatal("uncertain credential response advanced the old fingerprint")
					}
				})
			}
		}
	}
}
