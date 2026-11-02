package seerr_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/emby"
	"github.com/ajnart/self-host/internal/apps/plex"
	"github.com/ajnart/self-host/internal/apps/seerr"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func backendFixture(t *testing.T, id string, libraries []map[string]any) (*arrtest.Seerr, *helpers.H, *helpers.H, *[]helpers.StepResult) {
	t.Helper()
	fake := arrtest.NewSeerr()
	fake.APIKey = "seerr-key"
	kind, port := 3, 8096
	settings := map[string]any{"ip": id, "port": port, "useSsl": false, "urlBase": "", "serverId": "server-id", "apiKey": "seerr-emby-key"}
	if id == "plex" {
		kind, port = 1, 32400
		settings = map[string]any{"ip": id, "port": port, "useSsl": false, "machineId": "server-id"}
	}
	fake.ConfigureMediaServer(kind, true, settings, libraries)
	server := httptest.NewServer(fake.Handler())
	t.Cleanup(server.Close)
	target := httptest.NewServer(backendTargetHandler(t, id, "server-id", false))
	t.Cleanup(target.Close)
	secrets := secretMap{"SEERR_API_KEY": "seerr-key", "PLEX_API_KEY": "pms-key", "PLEX_SERVER_ID": "server-id"}
	var results []helpers.StepResult
	from := helpers.New(helpers.Config{App: "seerr", Service: "seerr", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Secrets: secrets, Log: events.Discard(), Inputs: map[string]string{"media_server": id}, Record: func(result helpers.StepResult) { results = append(results, result) }})
	to := helpers.New(helpers.Config{App: id, Service: id, ContainerPort: port, BaseURL: target.URL, HTTP: target.Client(), Secrets: secrets, Log: events.Discard(), Inputs: map[string]string{"verified_server_id": "server-id"}})
	return fake, from, to, &results
}

func backendTargetHandler(t *testing.T, id, serverID string, reject bool) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected target mutation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(405)
			return
		}
		if reject {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if id == "emby" {
			if r.URL.Path == "/System/Info/Public" {
				if r.Header.Get("X-Emby-Token") != "" {
					t.Error("public identity received a private credential")
				}
				json.NewEncoder(w).Encode(map[string]string{"Id": serverID})
				return
			}
			if r.Header.Get("X-Emby-Token") != "seerr-emby-key" {
				t.Error("Emby did not receive Seerr's existing private credential")
				w.WriteHeader(401)
				return
			}
			switch r.URL.Path {
			case "/emby/System/Info":
				json.NewEncoder(w).Encode(map[string]string{"Id": serverID, "Version": "4.10.2.0"})
			case "/Library/MediaFolders":
				w.Write([]byte(`{"Items":[{"Id":"movies","Type":"CollectionFolder","CollectionType":"movies","ServerId":"` + serverID + `"},{"Id":"shows","Type":"CollectionFolder","CollectionType":"tvshows","ServerId":"` + serverID + `"}],"TotalRecordCount":2}`))
			default:
				t.Errorf("unexpected Emby path: %s", r.URL.Path)
				w.WriteHeader(404)
			}
			return
		}
		switch r.URL.Path {
		case "/identity":
			json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"machineIdentifier": serverID, "claimed": true}})
		case "/library/sections":
			if r.Header.Get("X-Plex-Token") == "" {
				w.WriteHeader(401)
				return
			}
			if r.Header.Get("X-Plex-Token") != "pms-key" {
				t.Error("Plex did not receive its verified Project credential")
			}
			w.Write([]byte(`{"MediaContainer":{"size":2,"Directory":[{"key":"movies","type":"movie","agent":"com.plexapp.agents.themoviedb"},{"key":"shows","type":"show","agent":"tv.plex.agents.series"}]}}`))
		case "/prefs":
			w.Write([]byte(`{"MediaContainer":{}}`))
		case "/status/sessions", "/library/recentlyAdded":
			w.Write([]byte(`{"MediaContainer":{"size":0,"Metadata":[]}}`))
		default:
			t.Errorf("unexpected Plex path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
}

func savedBackendLibraries() []map[string]any {
	return []map[string]any{{"id": "movies", "name": "Movies", "enabled": true, "type": "movie"}, {"id": "shows", "name": "Shows", "enabled": false, "type": "show"}}
}

func requireReadOnlyBackend(t *testing.T, fake *arrtest.Seerr) {
	t.Helper()
	for _, request := range fake.Requests() {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("Seerr authentication, settings, sync or library mutation: %s", request)
		}
	}
}

func TestEmbyBackendPreservesLibraryFlagsAndRecordedIdentity(t *testing.T) {
	for _, allDisabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed", true: "all-disabled"}[allDisabled], func(t *testing.T) {
			libraries := savedBackendLibraries()
			if allDisabled {
				libraries[0]["enabled"] = false
			}
			before, _ := json.Marshal(libraries)
			fake, from, to, _ := backendFixture(t, "emby", libraries)
			link := linkTo(t, "emby")
			if err := link.Apply(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			record := from.Secret("SEERR_BACKEND_IDENTITY")
			if !strings.Contains(record, `"serverId":"server-id"`) {
				t.Fatal("verified stable identity was not recorded")
			}
			if err := link.Apply(context.Background(), from, to); err != nil || from.Secret("SEERR_BACKEND_IDENTITY") != record {
				t.Fatal("rerun changed the stable identity", err)
			}
			after, _ := json.Marshal(libraries)
			if string(after) != string(before) {
				t.Fatal("library selections changed")
			}
			requireReadOnlyBackend(t, fake)
		})
	}
}

func TestPlexBackendNeverClaimsReadbackAsSeerrAuthorization(t *testing.T) {
	fake, from, to, results := backendFixture(t, "plex", savedBackendLibraries())
	err := linkTo(t, "plex").Apply(context.Background(), from, to)
	if !errors.Is(err, helpers.ErrManual) || len(*results) != 1 || !strings.Contains((*results)[0].Hint, "Seerr's own Plex authorization") {
		t.Fatalf("cached Plex settings must remain specific Manual: %v, %+v", err, *results)
	}
	requireReadOnlyBackend(t, fake)
}

func TestBackendRejectsMalformedOrStaleLibrarySelectionsWithoutResync(t *testing.T) {
	for _, mode := range []string{"null", "duplicate", "missing-enabled", "wrong-type", "stale"} {
		t.Run(mode, func(t *testing.T) {
			libraries := savedBackendLibraries()
			switch mode {
			case "null":
				libraries = nil
			case "duplicate":
				libraries = append(libraries, libraries[0])
			case "missing-enabled":
				delete(libraries[0], "enabled")
			case "wrong-type":
				libraries[0]["type"] = "show"
			case "stale":
				libraries[1]["id"] = "removed-library"
			}
			fake, from, to, _ := backendFixture(t, "emby", libraries)
			if err := linkTo(t, "emby").Apply(context.Background(), from, to); err == nil {
				t.Fatal("invalid libraries completed the Link")
			}
			if from.Secret("SEERR_BACKEND_IDENTITY") != "" {
				t.Fatal("invalid selection recorded an accepted identity")
			}
			requireReadOnlyBackend(t, fake)
		})
	}
}

func TestBackendExistingSelectionWinsAndUnselectedBranchesDoNothing(t *testing.T) {
	fake, from, to, _ := backendFixture(t, "emby", savedBackendLibraries())
	from.Inputs["media_server"] = "plex"
	for _, link := range (seerr.App{}).Links()[:3] {
		if _, err := link.Applicable(context.Background(), from); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("conflicting explicit selection did not require migration review", link.Target, err)
		}
		if err := link.Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("direct apply bypassed migration review", link.Target, err)
		}
	}
	delete(from.Inputs, "media_server")
	for _, link := range (seerr.App{}).Links()[:3] {
		selected, err := link.Applicable(context.Background(), from)
		if err != nil || selected != (link.Target == "emby") {
			t.Fatal("existing backend was not retained", link.Target, selected, err)
		}
	}
	requireReadOnlyBackend(t, fake)
}

func TestFreshAndInterruptedBackendsNeverAuthenticateOrInitialize(t *testing.T) {
	for _, id := range []string{"plex", "emby"} {
		for _, kind := range []int{4, map[string]int{"plex": 1, "emby": 3}[id]} {
			t.Run(id+strconv.Itoa(kind), func(t *testing.T) {
				fake, from, to, _ := backendFixture(t, id, savedBackendLibraries())
				fake.ConfigureMediaServer(kind, false, map[string]any{}, savedBackendLibraries())
				if err := linkTo(t, id).Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("fresh/interrupted Setup did not remain Manual", err)
				}
				requireReadOnlyBackend(t, fake)
			})
		}
	}
}

func TestBackendRemovalPreservesHistoricalIdentityAndUnrelatedServers(t *testing.T) {
	fake, from, to, _ := backendFixture(t, "emby", savedBackendLibraries())
	if err := linkTo(t, "emby").Apply(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	record := from.Secret("SEERR_BACKEND_IDENTITY")
	from.Inputs["media_server"] = "plex"
	if err := linkTo(t, "emby").Remove(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("historical removal did not preserve the connection", err)
	}
	if err := linkTo(t, "plex").Remove(context.Background(), from, to); err != nil {
		t.Fatal("removing an unrelated target interfered with Emby", err)
	}
	if from.Secret("SEERR_BACKEND_IDENTITY") != record {
		t.Fatal("removal released an unowned connection identity")
	}
	requireReadOnlyBackend(t, fake)
}

func TestBackendRejectsChangedIdentityEndpointAndCredentials(t *testing.T) {
	for _, mode := range []string{"identity", "endpoint", "credential", "record", "seerr-key"} {
		t.Run(mode, func(t *testing.T) {
			fake, from, to, _ := backendFixture(t, "emby", savedBackendLibraries())
			switch mode {
			case "identity":
				server := httptest.NewServer(backendTargetHandler(t, "emby", "different-id", false))
				t.Cleanup(server.Close)
				to.BaseURL = server.URL
			case "endpoint":
				to.ContainerPort++
			case "credential":
				server := httptest.NewServer(backendTargetHandler(t, "emby", "server-id", true))
				t.Cleanup(server.Close)
				to.BaseURL = server.URL
			case "record":
				from.SetSecret("SEERR_BACKEND_IDENTITY", `{"backend":"emby","serverId":"other-id","host":"emby","port":8096}`)
			case "seerr-key":
				from.SetSecret("SEERR_API_KEY", "rejected-key")
			}
			if err := linkTo(t, "emby").Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("changed state was not Manual", err)
			}
			requireReadOnlyBackend(t, fake)
		})
	}
}

func TestBackendRecordMismatchStopsBeforeTargetCredentialsOrSeerrProbes(t *testing.T) {
	for _, id := range []string{"emby", "plex"} {
		for _, changed := range []string{"server", "host", "port"} {
			t.Run(id+"-"+changed, func(t *testing.T) {
				fake, from, to, _ := backendFixture(t, id, savedBackendLibraries())
				settings := map[string]any{"ip": id, "port": to.ContainerPort, "useSsl": false, "urlBase": "", "serverId": "server-B", "machineId": "server-B", "apiKey": "seerr-emby-key"}
				kind := 3
				if id == "plex" {
					kind = 1
					to.SetSecret("PLEX_SERVER_ID", "server-B")
					to.Inputs["verified_server_id"] = "server-B"
				}
				fake.ConfigureMediaServer(kind, true, settings, savedBackendLibraries())
				record := map[string]any{"backend": id, "serverId": "server-B", "host": id, "port": to.ContainerPort}
				switch changed {
				case "server":
					record["serverId"] = "server-A"
				case "host":
					record["host"] = "previous-server"
				case "port":
					record["port"] = to.ContainerPort + 1
				}
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				from.SetSecret("SEERR_BACKEND_IDENTITY", string(raw))
				var targetRequests, credentialRequests atomic.Int32
				targetHandler := backendTargetHandler(t, id, "server-B", false)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					targetRequests.Add(1)
					if r.Header.Get("X-Emby-Token") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Plex-Token") != "" {
						credentialRequests.Add(1)
					}
					targetHandler.ServeHTTP(w, r)
				}))
				t.Cleanup(server.Close)
				to.BaseURL, to.Config.HTTP = server.URL, server.Client()
				if err := linkTo(t, id).Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("record mismatch did not stop as Manual", err)
				}
				if targetRequests.Load() != 0 || credentialRequests.Load() != 0 {
					t.Fatalf("changed target received requests before record validation: total=%d credential-bearing=%d", targetRequests.Load(), credentialRequests.Load())
				}
				for _, request := range fake.Requests() {
					if strings.HasSuffix(request, "/users") {
						t.Fatal("record mismatch allowed a Seerr connection probe")
					}
				}
				if from.Secret("SEERR_BACKEND_IDENTITY") != string(raw) {
					t.Fatal("mismatched identity was replaced")
				}
				requireReadOnlyBackend(t, fake)
			})
		}
	}
}

func TestSeerrOwnsConsumerRecoveryForNeutralTargetFailures(t *testing.T) {
	for _, id := range []string{"emby", "plex"} {
		t.Run(id, func(t *testing.T) {
			fake, from, to, results := backendFixture(t, id, savedBackendLibraries())
			if id == "emby" {
				fake.ConfigureMediaServer(3, true, map[string]any{"ip": "emby", "port": 8096, "useSsl": false, "urlBase": "", "serverId": "server-id", "apiKey": ""}, savedBackendLibraries())
			} else {
				to.Inputs["verified_server_id"] = ""
			}
			if err := linkTo(t, id).Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("target prerequisite did not remain Manual", err)
			}
			if len(*results) != 1 || !strings.Contains((*results)[0].Hint, "Seerr's existing "+id) || !strings.Contains((*results)[0].Hint, "self-host setup "+id+" seerr") {
				t.Fatalf("Seerr did not supply consumer-owned recovery: %+v", *results)
			}
			requireReadOnlyBackend(t, fake)
		})
	}
}

type backendReadyEmby struct{ emby.App }

func (backendReadyEmby) Setup(context.Context, *helpers.H) error { return nil }
func (backendReadyEmby) Check(context.Context, *helpers.H) error { return nil }

type backendUnreadyPlex struct{ plex.App }

func (backendUnreadyPlex) Setup(ctx context.Context, h *helpers.H) error {
	return h.Step(ctx, "account", "verify account", helpers.StepSpec{Do: func(context.Context) error { return helpers.ErrManual }})
}

func TestEmbyBackendBothInstallationOrdersIgnoreUnselectedUnreadyPlex(t *testing.T) {
	for _, order := range [][]string{{"seerr", "emby"}, {"emby", "seerr"}} {
		t.Run(strings.Join(order, "-"), func(t *testing.T) {
			e := newEnv(t, seerr.App{}, backendReadyEmby{}, backendUnreadyPlex{})
			e.Serve("emby", backendTargetHandler(t, "emby", "server-id", false))
			e.Seerr.ConfigureMediaServer(3, true, map[string]any{"ip": "emby", "port": 8096, "useSsl": false, "urlBase": "", "serverId": "server-id", "apiKey": "seerr-emby-key"}, savedBackendLibraries())
			e.Run("install", "plex", "--yes")
			for _, id := range order {
				r := e.Run("install", id, "--yes", "--json")
				if r.Code != 0 {
					t.Fatal(r.Out)
				}
			}
			r := e.Run("setup", "seerr", "--json")
			if r.Code != 0 || r.StepEvent("seerr", "link-emby") != "step.ok" {
				t.Fatal(r.Out)
			}
			step := r.Step("seerr", "link-plex")
			if step == nil || step["msg"] != "not selected: media server: Plex" {
				t.Fatal("unselected Plex retained a readiness wait", r.Out)
			}
			requireReadOnlyBackend(t, e.Seerr)
		})
	}
}

func TestEmbyTargetRecoveryClearsOtherManualBackendSelections(t *testing.T) {
	e := newEnv(t, seerr.App{}, backendReadyEmby{}, backendUnreadyPlex{})
	e.Serve("emby", backendTargetHandler(t, "emby", "server-id", false))
	e.Run("install", "plex", "--yes")
	if r := e.Run("install", "emby", "--yes"); r.Code != 0 {
		t.Fatal(r.Out)
	}
	r := e.Run("install", "seerr", "--yes", "--json")
	if r.StepEvent("seerr", "link-plex") != "step.manual" || r.StepEvent("seerr", "link-emby") != "step.manual" {
		t.Fatal("fresh ambiguous selection did not leave both Links Manual", r.Out)
	}
	e.Seerr.ConfigureMediaServer(3, true, map[string]any{"ip": "emby", "port": 8096, "useSsl": false, "urlBase": "", "serverId": "server-id", "apiKey": "seerr-emby-key"}, savedBackendLibraries())
	r = e.Run("setup", "emby", "--json")
	if r.StepEvent("seerr", "link-emby") != "step.ok" || r.StepEvent("seerr", "link-plex") != "step.skip" {
		t.Fatal("target-triggered recovery retained the other Manual selection", r.Out)
	}
	p := &project.Project{Root: clitest.Root(t, e.Deps)}
	state, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Apps["seerr"].Setup != stepstatus.SetupOK || state.Apps["seerr"].Steps["link-plex"].Hint != "" {
		t.Fatalf("recovered consumer retained Manual selection state: %+v", state.Apps["seerr"])
	}
	requireReadOnlyBackend(t, e.Seerr)
}

func TestBackendPublicStateRequiresExplicitFields(t *testing.T) {
	for _, body := range []string{`{}`, `{"initialized":null,"mediaServerType":3}`, `{"initialized":true,"mediaServerType":null}`, `{"initialized":true,"mediaServerType":0}`, `{"initialized":true,"mediaServerType":5}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			t.Cleanup(server.Close)
			h := helpers.New(helpers.Config{App: "seerr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: secretMap{}})
			if err := (seerr.App{}).Check(context.Background(), h); err == nil {
				t.Fatal("incomplete public state was accepted")
			}
		})
	}
}

func TestBackendRequiresUsefulEmbyDataThroughSeerrItself(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `[{"id":"","username":"admin"}]`, `[{"id":"owner","username":"admin"},{"id":"owner","username":"other"}]`, `{"error":"unavailable"}`} {
		t.Run(body, func(t *testing.T) {
			fake, from, to, _ := backendFixture(t, "emby", savedBackendLibraries())
			handler := fake.Handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/settings/jellyfin/users" {
					w.Write([]byte(body))
					return
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			from.BaseURL, from.Config.HTTP = server.URL, server.Client()
			if err := linkTo(t, "emby").Apply(context.Background(), from, to); err == nil {
				t.Fatal("target identity plus cached Seerr settings falsely completed native verification")
			}
			if from.Secret("SEERR_BACKEND_IDENTITY") != "" {
				t.Fatal("failed native verification recorded an accepted identity")
			}
			requireReadOnlyBackend(t, fake)
		})
	}
}

func TestMultipleFreshBackendsRequireChoiceWithoutChangingTheSingleJellyfinDefault(t *testing.T) {
	fake, from, _, _ := backendFixture(t, "emby", savedBackendLibraries())
	fake.ConfigureMediaServer(4, false, nil, []map[string]any{})
	delete(from.Inputs, "media_server")
	from.Peer = func(id string) *helpers.H { return &helpers.H{Config: helpers.Config{App: id}} }
	for _, link := range (seerr.App{}).Links()[:3] {
		if _, err := link.Applicable(context.Background(), from); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("multiple installed servers silently selected a backend", link.Target, err)
		}
	}
	from.Peer = func(id string) *helpers.H {
		if id == "jellyfin" {
			return &helpers.H{Config: helpers.Config{App: id}}
		}
		return nil
	}
	if applicable, err := linkTo(t, "jellyfin").Applicable(context.Background(), from); err != nil || !applicable {
		t.Fatal("the legacy single-Jellyfin default changed", err)
	}
	requireReadOnlyBackend(t, fake)
}

func TestEstablishedJellyfinPreservesDisabledLibrariesAndRejectsMigration(t *testing.T) {
	fake, from, _, _ := backendFixture(t, "emby", savedBackendLibraries())
	libraries := savedBackendLibraries()
	libraries[0]["enabled"] = false
	fake.ConfigureMediaServer(2, true, map[string]any{"ip": "jellyfin", "port": 8096, "useSsl": false, "urlBase": "", "serverId": "jellyfin-server"}, libraries)
	from.Inputs["media_server"] = "jellyfin"
	server := httptest.NewServer(arrtest.NewJellyfin().Handler())
	t.Cleanup(server.Close)
	to := helpers.New(helpers.Config{App: "jellyfin", Service: "jellyfin", ContainerPort: 8096, BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: secretMap{}})
	if err := linkTo(t, "jellyfin").Apply(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if enabled := fake.EnabledLibraries(); len(enabled) != 0 {
		t.Fatal("disabled Jellyfin libraries were reenabled", enabled)
	}
	from.Inputs["media_server"] = "emby"
	if err := linkTo(t, "jellyfin").Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("Jellyfin migration was not held", err)
	}
	requireReadOnlyBackend(t, fake)
}

func TestBackendLostOrMalformedObservationCannotBootstrapOrReleaseOwnership(t *testing.T) {
	for _, record := range []string{`{}`, `{"backend":"emby","serverId":"server-id","host":"emby","port":8096}`} {
		fake, from, to, _ := backendFixture(t, "emby", savedBackendLibraries())
		from.SetSecret("SEERR_BACKEND_IDENTITY", record)
		fake.ConfigureMediaServer(4, false, nil, []map[string]any{})
		if err := linkTo(t, "emby").Apply(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("lost configuration replayed initial setup", err)
		}
		if err := linkTo(t, "emby").Remove(context.Background(), from, to); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("historical identity was silently released", err)
		}
		if from.Secret("SEERR_BACKEND_IDENTITY") != record {
			t.Fatal("observation changed during uncertain recovery")
		}
		requireReadOnlyBackend(t, fake)
	}
}

func TestMediaServerInputAcceptsOnlySupportedChoices(t *testing.T) {
	inputs := (seerr.App{}).Inputs()
	var input app.Input
	for _, candidate := range inputs {
		if candidate.Key == "media_server" {
			input = candidate
		}
	}
	if input.Validate == nil || input.Default != "" || input.Private {
		t.Fatal("missing public explicit selection surface")
	}
	for _, choice := range []string{"", "plex", "emby", "jellyfin"} {
		if err := input.Validate(choice); err != nil {
			t.Fatal(err)
		}
	}
	if input.Validate("overseerr") == nil || input.Validate("PLEX") == nil {
		t.Fatal("unsupported backend accepted")
	}
	if !reflect.DeepEqual([]string{inputs[1].Key, inputs[2].Key}, []string{"sonarr_profile", "radarr_profile"}) {
		t.Fatal("request-service selection inputs changed")
	}
}
