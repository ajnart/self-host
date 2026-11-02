package servarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	from, to                                 *helpers.H
	recorder                                 *porttest.Recorder
	secrets                                  porttest.Secrets
	p                                        providerLink
}

func servarrContractCopy(item provider) provider {
	data, _ := json.Marshal(item)
	var copied provider
	_ = json.Unmarshal(data, &copied)
	return copied
}

func newServarrContract(t *testing.T, api string) *servarrContractFixture {
	t.Helper()
	f := &servarrContractFixture{nativeBody: `{}`, nativeStatus: 200, items: []provider{}, secrets: porttest.Secrets{"SONARR_API_KEY": "consumer-key"}, recorder: &porttest.Recorder{}}
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
	mux.HandleFunc(api+"/downloadclient/schema", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode([]provider{f.template}) })
	mux.HandleFunc(api+"/downloadclient/test", func(w http.ResponseWriter, r *http.Request) {
		if f.guards == 0 {
			t.Error("native test ran before provider guard")
		}
		f.tests++
		w.WriteHeader(f.nativeStatus)
		w.Write([]byte(f.nativeBody))
	})
	mux.HandleFunc(api+"/downloadclient", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "consumer-key" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" {
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
		item["id"] = float64(17)
		f.items = append(f.items, item)
		if f.uncertain {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(item)
	})
	mux.HandleFunc(api+"/downloadclient/17", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			f.updates++
			var item provider
			json.NewDecoder(r.Body).Decode(&item)
			f.items[0] = item
			json.NewEncoder(w).Encode(item)
		case "DELETE":
			f.deletes++
			f.items = []provider{}
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
