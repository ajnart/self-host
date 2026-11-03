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
	policy      string
	policyReads int
}

func (e *transmissionLinkExecutor) Exec(ctx context.Context, service string, env map[string]string, args ...string) (system.Result, error) {
	if len(args) == 3 && args[0] == "sh" && args[1] == "-c" && strings.Contains(args[2], "rpc-authentication-required") {
		e.policyReads++
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

type transmissionSource struct {
	consumer, api, field, category, schemaCategory, version, protocol string
}

func transmissionSources() []transmissionSource {
	return []transmissionSource{
		{"sonarr", V3, "tvCategory", "tv", "tv-sonarr", "4.0.20.3014", "torrent"},
		{"radarr", V3, "movieCategory", "movies", "radarr", "6.4.4.10685", "torrent"},
		{"lidarr", V1, "musicCategory", "music", "lidarr", "3.1.0.4875", "torrent"},
		{"lidarr", V1, "musicCategory", "music", "lidarr", "3.1.6.5079", "TorrentDownloadProtocol"},
	}
}

func newTransmissionLinkFixture(t *testing.T, consumer string, legacy bool) *transmissionLinkFixture {
	t.Helper()
	for _, source := range transmissionSources() {
		if source.consumer == consumer {
			return newTransmissionSourceFixture(t, source, legacy)
		}
	}
	t.Fatal("unsupported source fixture")
	return nil
}

func newTransmissionSourceFixture(t *testing.T, source transmissionSource, legacy bool) *transmissionLinkFixture {
	t.Helper()
	consumer, api, field, category := source.consumer, source.api, source.field, source.category
	f := newServarrContract(t, api)
	f.guards = 1
	f.appName, f.version = map[string]string{"sonarr": "Sonarr", "radarr": "Radarr", "lidarr": "Lidarr"}[consumer], source.version
	f.from.App, f.from.Service = consumer, consumer
	f.secrets[strings.ToUpper(consumer)+"_API_KEY"] = "consumer-key"
	f.secrets["ADMIN_USER"], f.secrets["ADMIN_PASSWORD"] = "fixture-user", "fixture-password"
	f.template = provider{"implementation": "Transmission", "configContract": "TransmissionSettings", "protocol": source.protocol, "fields": []any{}, "priority": float64(1), "removeCompletedDownloads": false}
	prefix := strings.TrimSuffix(field, "Category")
	for name, value := range map[string]any{"host": "localhost", "port": float64(9091), "useSsl": false, "urlBase": "/transmission/", "username": nil, "password": nil, field: source.schemaCategory, prefix + "Directory": nil, prefix + "ImportedCategory": nil, "recent" + strings.ToUpper(prefix[:1]) + prefix[1:] + "Priority": float64(0), "older" + strings.ToUpper(prefix[:1]) + prefix[1:] + "Priority": float64(0), "addPaused": false} {
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
	resp, err := f.from.HTTP(t.Context(), Request(f.from, api, helpers.Request{Path: "/system/status", NoRedirect: true}))
	var status struct{ AppName, Version string }
	if err != nil || resp.Status != 200 || resp.Decode(&status) != nil || status.AppName != f.appName || status.Version != source.version {
		t.Fatal("source-version/protocol fixture is inconsistent", err)
	}
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
	for _, source := range transmissionSources() {
		for _, legacy := range []bool{false, true} {
			t.Run(source.consumer+"/"+source.version+map[bool]string{false: "/shared", true: "/legacy"}[legacy], func(t *testing.T) {
				f := newTransmissionSourceFixture(t, source, legacy)
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
	for _, source := range transmissionSources() {
		f := newTransmissionSourceFixture(t, source, false)
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

func TestTransmissionSourceFamiliesPreserveWireLifecycleAndSurvivors(t *testing.T) {
	for _, source := range transmissionSources() {
		t.Run(source.consumer+"/"+source.version, func(t *testing.T) {
			f := newTransmissionSourceFixture(t, source, false)
			if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			key := f.from.Key("link_transmission_resource")
			raw := f.secrets[key]
			var record map[string]any
			if err := json.Unmarshal([]byte(raw), &record); err != nil || len(record) != 4 || record["id"] != float64(17) || record["target"] != "transmission" || record["endpoint"] != "selected-transmission" {
				t.Fatal("ownership was migrated instead of retaining its serialized four-field contract", err)
			}
			f.items[0].setField(f.field, "chosen")
			f.items[0]["priority"] = float64(9)
			f.items[0].setField("addPaused", true)
			f.items[0].setField("password", "********")
			if err := f.link.Apply(t.Context(), f.from, f.to); err != nil || f.secrets[key] != raw || f.updates != 0 {
				t.Fatal("masked rerun changed wire/ownership", err)
			}
			f.rpc.mu.Lock()
			f.rpc.password = "accepted-rotated-by-owner"
			f.rpc.mu.Unlock()
			f.secrets["ADMIN_PASSWORD"] = "accepted-rotated-by-owner"
			before := servarrContractCopy(f.items[0])
			if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			before.setField("password", "accepted-rotated-by-owner")
			want, _ := json.Marshal(before)
			got, _ := json.Marshal(f.items[0])
			if string(want) != string(got) || f.items[0]["protocol"] != source.protocol || f.updates != 1 {
				t.Fatal("credential-only update changed noncredential settings or source wire")
			}
			survivor := servarrContractCopy(f.items[0])
			survivor["id"], survivor["name"] = float64(88), "independent Transmission"
			survivor.setField("host", "independent-host")
			personal := servarrContractCopy(f.items[0])
			personal["id"], personal["name"] = float64(89), "personal Transmission"
			f.items = append(f.items, survivor, personal)
			consumerSecrets := &protocolTrackedSecrets{values: f.secrets}
			targetSecrets := &protocolTrackedSecrets{values: f.secrets}
			f.from.Config.Secrets, f.to.Config.Secrets = consumerSecrets, targetSecrets
			delete(f.secrets, "ADMIN_PASSWORD")
			f.rpc.status, f.exec.policy = 403, "false"
			rpcCalls, native, probes, policyReads := f.rpc.authenticated, f.tests, f.exec.calls, f.exec.policyReads
			if err := f.link.Remove(t.Context(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if targetSecrets.reads != 0 || targetSecrets.writes != 0 || f.rpc.authenticated != rpcCalls || f.tests != native || f.exec.calls != probes || f.exec.policyReads != policyReads || f.secrets[key] != "" || len(f.items) != 2 || f.items[0].id() != 88 || f.items[1].id() != 89 {
				t.Fatal("offline removal contacted target or changed a survivor")
			}
			consumerSecrets.writes = 0
			if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || targetSecrets.reads != 0 || consumerSecrets.writes != 0 || f.tests != native {
				t.Fatal("reinstall adopted a personal same-target entry", err)
			}
			f.items[1].setField("host", "person-selected-host")
			f.secrets["ADMIN_PASSWORD"] = "accepted-rotated-by-owner"
			f.rpc.status, f.exec.policy = 0, "true"
			if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if len(f.items) != 3 || f.items[0].id() != 88 || f.items[1].id() != 89 || f.items[2].id() != 18 || f.items[2]["protocol"] != source.protocol {
				t.Fatal("fresh creation reclaimed an ID or changed protocol/survivors")
			}
			for _, submitted := range append(append(f.nativeItems, f.createdItems...), f.updatedItems...) {
				if submitted["protocol"] != source.protocol {
					t.Fatal("native/create/update body normalized the actual schema wire")
				}
			}
		})
	}
}

func TestTransmissionCommonAdmissionPrecedesTargetAndPrivateWrites(t *testing.T) {
	creationFaults := []string{"schema-sibling", "schema-sibling-string", "schema-lidarr-string", "schema-null", "schema-number", "schema-bool", "schema-unknown", "schema-whitespace", "schema-casing", "schema-contract", "schema-missing", "schema-duplicate", "schema-field-type", "schema-missing-credential", "schema-credential-type", "auth-schema", "auth-resource", "malformed-schema", "malformed-resource", "missing-key", "rejected-key", "schema-forbidden", "resource-forbidden", "wrong-api", "wrong-category"}
	ownedFaults := append(slices.Clone(creationFaults), "resource-crossed", "schema-crossed", "resource-sibling", "resource-contract", "resource-field-type", "resource-extra-field", "resource-disabled")
	for _, source := range transmissionSources() {
		for _, operation := range []string{"create", "apply", "remove"} {
			faults := creationFaults
			if operation != "create" {
				faults = ownedFaults
			}
			for _, fault := range faults {
				if source.consumer == "lidarr" && fault == "schema-lidarr-string" {
					continue
				}
				if operation == "remove" && fault == "resource-disabled" {
					continue
				}
				if source.consumer != "lidarr" && strings.HasSuffix(fault, "-crossed") {
					continue
				}
				t.Run(source.consumer+"/"+source.version+"/"+operation+"/"+fault, func(t *testing.T) {
					f := newTransmissionSourceFixture(t, source, false)
					if operation != "create" {
						if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
							t.Fatal(err)
						}
					}
					key := f.from.Key("link_transmission_resource")
					raw := f.secrets[key]
					other := "TorrentDownloadProtocol"
					if source.protocol == other {
						other = "torrent"
					}
					privateBody, malformed := `{"status":false,"error":"private-admission-marker"}`, `{"private-admission-marker"`
					switch fault {
					case "schema-sibling":
						f.template["protocol"] = "usenet"
					case "schema-sibling-string":
						f.template["protocol"] = "UsenetDownloadProtocol"
					case "schema-lidarr-string":
						f.template["protocol"] = "TorrentDownloadProtocol"
					case "schema-null":
						f.template["protocol"] = nil
					case "schema-number":
						f.template["protocol"] = float64(1)
					case "schema-bool":
						f.template["protocol"] = true
					case "schema-unknown":
						f.template["protocol"] = "unknown-wire"
					case "schema-whitespace":
						f.template["protocol"] = source.protocol + " "
					case "schema-casing":
						f.template["protocol"] = "TORRENT"
					case "schema-contract":
						f.template["configContract"] = "WrongSettings"
					case "schema-missing":
						f.schemas = []provider{}
					case "schema-duplicate":
						f.schemas = []provider{servarrContractCopy(f.template), servarrContractCopy(f.template)}
					case "schema-field-type":
						f.template.setField("port", true)
					case "schema-missing-credential":
						f.template["fields"] = slices.DeleteFunc(f.template["fields"].([]any), func(raw any) bool { return raw.(map[string]any)["name"] == "password" })
					case "schema-credential-type":
						f.template.setField("username", true)
					case "auth-schema":
						f.schemaBody = &privateBody
					case "auth-resource":
						f.resourceBody = &privateBody
					case "malformed-schema":
						f.schemaBody = &malformed
					case "malformed-resource":
						f.resourceBody = &malformed
					case "missing-key":
						delete(f.secrets, f.from.Key("api_key"))
					case "rejected-key":
						f.secrets[f.from.Key("api_key")] = "rejected-key"
					case "schema-forbidden":
						f.schemaStatus = 403
					case "resource-forbidden":
						f.resourceStatus = 401
					case "wrong-api":
						api := V1
						if source.api == V1 {
							api = V3
						}
						f.link = TransmissionLink(api, source.field, source.category)
					case "wrong-category":
						f.link = TransmissionLink(source.api, "unrecognizedCategory", source.category)
					case "resource-crossed":
						f.items[0]["protocol"] = other
					case "schema-crossed":
						f.template["protocol"] = other
					case "resource-sibling":
						f.items[0]["protocol"] = "usenet"
					case "resource-contract":
						f.items[0]["configContract"] = "WrongSettings"
					case "resource-field-type":
						f.items[0].setField("port", true)
					case "resource-extra-field":
						f.items[0]["fields"] = append(f.items[0]["fields"].([]any), map[string]any{"name": "unknown", "value": true})
					case "resource-disabled":
						f.items[0]["enable"] = false
					}
					consumerSecrets, targetSecrets := &protocolTrackedSecrets{values: f.secrets}, &protocolTrackedSecrets{values: f.secrets}
					f.from.Config.Secrets, f.to.Config.Secrets = consumerSecrets, targetSecrets
					rpcCalls, native, probes, policies := f.rpc.authenticated, f.tests, f.exec.calls, f.exec.policyReads
					creates, updates, deletes := f.creates, f.updates, f.deletes
					var err error
					if operation == "remove" {
						err = f.link.Remove(t.Context(), f.from, f.to)
					} else {
						err = f.link.Apply(t.Context(), f.from, f.to)
					}
					if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-admission-marker") {
						t.Fatal("invalid native contract did not return private Manual recovery", err)
					}
					if targetSecrets.reads != 0 || targetSecrets.writes != 0 || consumerSecrets.writes != 0 || f.rpc.authenticated != rpcCalls || f.exec.calls != probes || f.exec.policyReads != policies || f.tests != native || f.creates != creates || f.updates != updates || f.deletes != deletes || f.secrets[key] != raw {
						t.Fatal("common rejection reached target/native/CRUD/private writes")
					}
				})
			}
		}
	}
}

func TestTransmissionWireResponseUncertaintyDoesNotAdvanceOwnership(t *testing.T) {
	for _, source := range transmissionSources()[2:] {
		for _, operation := range []string{"create", "update"} {
			t.Run(source.version+"/"+operation, func(t *testing.T) {
				f := newTransmissionSourceFixture(t, source, false)
				key := f.from.Key("link_transmission_resource")
				if operation == "update" {
					if err := f.link.Apply(t.Context(), f.from, f.to); err != nil {
						t.Fatal(err)
					}
					f.items[0].setField("password", "********")
					f.rpc.password = "accepted-owner-password"
					f.secrets["ADMIN_PASSWORD"] = "accepted-owner-password"
				}
				raw := f.secrets[key]
				other := "TorrentDownloadProtocol"
				if source.protocol == other {
					other = "torrent"
				}
				f.responseEdit = func(item provider) { item["protocol"] = other }
				consumer := &protocolTrackedSecrets{values: f.secrets}
				f.from.Config.Secrets = consumer
				if err := f.link.Apply(t.Context(), f.from, f.to); err == nil {
					t.Fatal("different response spelling was accepted")
				}
				if f.deletes != 0 || f.items[0]["protocol"] != source.protocol {
					t.Fatal("uncertain response caused protocol rewrite or undo")
				}
				if operation == "create" {
					var pending providerOwnership
					if err := json.Unmarshal([]byte(f.secrets[key]), &pending); err != nil || pending.ID != 0 || consumer.writes != 1 || f.creates != 1 {
						t.Fatal("uncertain creation acquired an ID or lost its intent", err)
					}
					native, rpcCalls := f.tests, f.rpc.authenticated
					if err := f.link.Apply(t.Context(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || consumer.writes != 1 || f.creates != 1 || f.tests != native || f.rpc.authenticated != rpcCalls {
						t.Fatal("uncertain creation was retried", err)
					}
				} else if consumer.writes != 0 || f.updates != 1 || f.secrets[key] != raw {
					t.Fatal("uncertain credential response advanced prior fingerprint")
				}
			})
		}
	}
}
