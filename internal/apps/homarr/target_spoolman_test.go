package homarr_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/homarr"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type spoolmanLinkFixture struct {
	mu              sync.Mutex
	fake            *fakeHomarr
	from            *helpers.H
	to              *helpers.H
	values          map[string]any
	metrics         []any
	requiredSecrets [][]string
	inventory       string
	info            string
	protectedPath   string
	protectedStatus int
	refreshPatch    map[string]any
	noAdvance       bool
	missingPassword bool
	timestamp       int
	catalogs        int
	snapshots       int
	refreshes       int
	peerReads       int
	homarrCalls     int
	writes          int
	results         []helpers.StepResult
}

func newSpoolmanLinkFixture() *spoolmanLinkFixture {
	fake := newFake()
	fake.version = "2.10.1"
	fake.apiKey = "keyid.secret"
	return &spoolmanLinkFixture{
		fake: fake, timestamp: 1000, inventory: "[]",
		info:            `{"version":"0.27.1","db_type":"DatabaseType.POSTGRES","debug_mode":false,"automatic_backups":false,"data_dir":"/private/data","logs_dir":"/private/logs","backups_dir":"/private/backups"}`,
		values:          map[string]any{"spools": float64(0), "remainingWeight": nil},
		metrics:         []any{map[string]any{"key": "spools", "unit": "count"}, map[string]any{"key": "remainingWeight", "unit": "grams"}},
		requiredSecrets: [][]string{{}},
	}
}

func (f *spoolmanLinkFixture) link(t *testing.T) app.Link {
	t.Helper()
	fallback := f.fake.handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			f.peerReads++
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("ApiKey") != "" {
				t.Error("Spoolman must only receive read-only requests without unrelated credentials")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.URL.Path == f.protectedPath {
				w.Header().Set("Location", "/private-inventory-login")
				w.WriteHeader(f.protectedStatus)
				w.Write([]byte("private-inventory-id"))
				return
			}
			switch r.URL.Path {
			case "/api/v1/health":
				w.Write([]byte(`{"status":"healthy"}`))
			case "/api/v1/info":
				w.Write([]byte(f.info))
			case "/api/v1/spool":
				w.Write([]byte(f.inventory))
			default:
				t.Error("unexpected Spoolman endpoint")
				http.NotFound(w, r)
			}
			return
		}
		f.homarrCalls++
		if !f.fake.keyOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			f.writes++
		}
		if r.URL.Path == "/api/trpc/integration.getKinds" {
			trpc(w, []any{map[string]any{"kind": "spoolman", "requiredSecrets": f.requiredSecrets}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
			var input struct {
				JSON map[string]any `json:"json"`
			}
			if r.Method == http.MethodGet {
				if err := json.Unmarshal([]byte(r.URL.Query().Get("input")), &input); err != nil {
					t.Error(err)
				}
			} else {
				input.JSON = inputOf(r)
			}
			if input.JSON["integrationId"] != f.from.Secret("HOMARR_LINKED_SPOOLMAN") {
				t.Error("Stats must read the recorded owned Spoolman integration")
			}
			switch r.URL.Path {
			case "/api/trpc/widget.stats.catalog":
				f.catalogs++
				trpc(w, map[string]any{"metrics": f.metrics})
			case "/api/trpc/widget.stats.snapshot":
				f.snapshots++
				trpc(w, map[string]any{"updatedAt": f.timestamp})
			case "/api/trpc/widget.stats.refresh":
				f.refreshes++
				if r.Method != http.MethodPost || input.JSON["force"] != true {
					t.Error("Spoolman must use the central forced refresh")
				}
				if !f.noAdvance {
					f.timestamp++
				}
				sample := map[string]any{"updatedAt": f.timestamp, "error": false, "stale": false, "retryAt": 0, "values": f.values}
				maps.Copy(sample, f.refreshPatch)
				trpc(w, sample)
			default:
				t.Error("unexpected additional Stats request")
				http.NotFound(w, r)
			}
			return
		}
		fallback.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	f.from = homarrH(t, server, fakeSecrets{"HOMARR_API_KEY": f.fake.apiKey})
	f.from.Record = func(result helpers.StepResult) { f.results = append(f.results, result) }
	peerSecrets := fakeSecrets{"SPOOLMAN_DB_PASSWORD": "private-database-secret"}
	if f.missingPassword {
		delete(peerSecrets, "SPOOLMAN_DB_PASSWORD")
	}
	f.to = targetH(t, server, "spoolman", 17912, peerSecrets)
	f.to.ContainerPort = 8000
	return homarr.SpoolmanTestLink()
}

func (f *spoolmanLinkFixture) requireSamples(t *testing.T, want int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.catalogs != want || f.snapshots != want || f.refreshes != want {
		t.Fatalf("catalog/snapshot/refresh counts = %d/%d/%d, want %d each", f.catalogs, f.snapshots, f.refreshes, want)
	}
}

func TestSpoolmanLinkUsesOneCentralSampleForEachVerificationAndStableOwnedIDs(t *testing.T) {
	for _, populated := range []bool{false, true} {
		fixture := newSpoolmanLinkFixture()
		if populated {
			fixture.inventory = `[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0,"remaining_weight":null}]`
			fixture.values["spools"] = float64(1)
		}
		link := fixture.link(t)
		if err := link.Apply(t.Context(), fixture.from, fixture.to); err != nil {
			t.Fatal(err)
		}
		fixture.requireSamples(t, 2)
		integrationID := fixture.from.Secret("HOMARR_LINKED_SPOOLMAN")
		ownership := fixture.from.Secret(fixture.from.Key("widgets_" + integrationID))
		if integrationID == "" || ownership == "" {
			t.Fatal("Link did not record integration and widget ownership")
		}
		if err := link.Apply(t.Context(), fixture.from, fixture.to); err != nil {
			t.Fatal(err)
		}
		fixture.requireSamples(t, 3)
		if fixture.from.Secret("HOMARR_LINKED_SPOOLMAN") != integrationID || fixture.from.Secret(fixture.from.Key("widgets_"+integrationID)) != ownership {
			t.Fatal("rerun changed recorded owned IDs")
		}
		fixture.fake.mu.Lock()
		if len(fixture.fake.integrations) != 1 {
			t.Fatal("rerun duplicated the integration")
		}
		integration := fixture.fake.integrations[0]
		secrets, valid := integration["secrets"].([]any)
		if !valid || len(secrets) != 0 || integration["kind"] != "spoolman" || integration["url"] != "http://spoolman:8000" {
			t.Fatal("Link must submit the exact no-secret native kind and internal endpoint")
		}
		fixture.fake.mu.Unlock()
		fixture.mu.Lock()
		if fixture.values["remainingWeight"] != nil {
			t.Fatal("unknown remaining weight was synthesized into zero")
		}
		fixture.mu.Unlock()
	}
}

func TestSpoolmanLinkRejectsInvalidFirstFreshSampleWithoutAdditionalAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch map[string]any
	}{
		{"missing count", map[string]any{"values": map[string]any{}}},
		{"null count", map[string]any{"values": map[string]any{"spools": nil}}},
		{"private string count", map[string]any{"values": map[string]any{"spools": "private-inventory-id"}}},
		{"negative count", map[string]any{"values": map[string]any{"spools": -1}}},
		{"fractional count", map[string]any{"values": map[string]any{"spools": 0.5}}},
		{"failed refresh", map[string]any{"error": true}},
		{"stale refresh", map[string]any{"stale": true}},
		{"missing timestamp", map[string]any{"updatedAt": nil}},
		{"zero timestamp", map[string]any{"updatedAt": 0}},
		{"retry pending", map[string]any{"retryAt": 12345}},
		{"retry null", map[string]any{"retryAt": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSpoolmanLinkFixture()
			fixture.refreshPatch = tc.patch
			link := fixture.link(t)
			err := link.Apply(t.Context(), fixture.from, fixture.to)
			if err == nil || strings.Contains(err.Error(), "private-inventory-id") || strings.Contains(err.Error(), "private-database-secret") {
				t.Fatalf("invalid data must fail without printing private values: %v", err)
			}
			fixture.requireSamples(t, 1)
		})
	}
	fixture := newSpoolmanLinkFixture()
	fixture.noAdvance = true
	link := fixture.link(t)
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err == nil || !strings.Contains(err.Error(), "did not advance") {
		t.Fatalf("a cached non-stale no-op refresh must not pass: %v", err)
	}
	fixture.requireSamples(t, 1)
}

func TestSpoolmanLinkRequiresTheAdvertisedCountUnit(t *testing.T) {
	fixture := newSpoolmanLinkFixture()
	fixture.metrics = []any{map[string]any{"key": "spools", "unit": "grams"}}
	link := fixture.link(t)
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err == nil {
		t.Fatal("wrong metric unit completed the Spoolman Link")
	}
	fixture.requireSamples(t, 1)
}

func TestSpoolmanLinkRejectsMissingCatalogMetricBeforeRefreshing(t *testing.T) {
	fixture := newSpoolmanLinkFixture()
	fixture.metrics = []any{map[string]any{"key": "remainingWeight", "unit": "grams"}}
	link := fixture.link(t)
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err == nil {
		t.Fatal("a catalog without the selected spool count completed the Link")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.catalogs != 1 || fixture.snapshots != 0 || fixture.refreshes != 0 {
		t.Fatal("missing selected metric must fail at the one central catalog read")
	}
}

func TestSpoolmanLinkRejectsHomarrAuthenticationWithoutMutation(t *testing.T) {
	fixture := newSpoolmanLinkFixture()
	link := fixture.link(t)
	if err := fixture.from.SetSecret("HOMARR_API_KEY", "rejected-private-homarr-key"); err != nil {
		t.Fatal(err)
	}
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err == nil || strings.Contains(err.Error(), "rejected-private-homarr-key") {
		t.Fatalf("rejected Homarr authentication must fail without printing the key: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.writes != 0 || fixture.catalogs != 0 || fixture.refreshes != 0 {
		t.Fatal("rejected Homarr authentication reached mutation or Stats")
	}
}

func TestSpoolmanLinkPreflightPreservesAuthenticationAndStopsBeforeHomarrRequests(t *testing.T) {
	for _, status := range []int{302, 401, 403} {
		fixture := newSpoolmanLinkFixture()
		fixture.protectedPath, fixture.protectedStatus = "/api/v1/spool", status
		link := fixture.link(t)
		before := maps.Clone(fixture.to.Secrets.(fakeSecrets))
		if err := link.Apply(t.Context(), fixture.from, fixture.to); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("protected inventory must stay Manual: %v", err)
		}
		if fixture.homarrCalls != 0 || fixture.refreshes != 0 || fixture.writes != 0 || !maps.Equal(before, fixture.to.Secrets.(fakeSecrets)) {
			t.Fatal("protected inventory caused Homarr requests, credential changes or a fabricated refresh")
		}
		last := fixture.results[len(fixture.results)-1]
		if last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, "authentication") || strings.Contains(last.Hint, "private-inventory") {
			t.Fatal("the original actionable authentication recovery must be retained without response data")
		}
	}
}

func TestSpoolmanLinkRejectsMissingCredentialsOrInvalidNativeIdentityBeforeMutation(t *testing.T) {
	for _, mode := range []string{"missing password", "wrong version", "malformed inventory"} {
		fixture := newSpoolmanLinkFixture()
		switch mode {
		case "missing password":
			fixture.missingPassword = true
		case "wrong version":
			fixture.info = strings.Replace(fixture.info, "0.27.1", "dev", 1)
		case "malformed inventory":
			fixture.inventory = `[{"id":"private-inventory-id"}]`
		}
		link := fixture.link(t)
		if err := link.Apply(t.Context(), fixture.from, fixture.to); err == nil || strings.Contains(err.Error(), "private-inventory-id") {
			t.Fatalf("native prerequisite must fail safely: %v", err)
		}
		if fixture.homarrCalls != 0 || fixture.refreshes != 0 || fixture.writes != 0 {
			t.Fatal("invalid native prerequisites reached Homarr")
		}
	}
}

func TestSpoolmanLinkDoesNotInventOtherCredentialAlternatives(t *testing.T) {
	fixture := newSpoolmanLinkFixture()
	fixture.requiredSecrets = [][]string{{"apiKey"}, {"username", "password"}}
	link := fixture.link(t)
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err == nil || !strings.Contains(err.Error(), "credential shape") {
		t.Fatalf("an unadvertised empty alternative must fail: %v", err)
	}
	if fixture.writes != 0 || fixture.refreshes != 0 {
		t.Fatal("unsupported authentication reached mutation or Stats")
	}
}

func TestSpoolmanLinkPreservesCrossKindSharedEntriesAndUserIntegrationOnRemoval(t *testing.T) {
	fixture := newSpoolmanLinkFixture()
	entries := []any{
		map[string]any{"id": "sonarr-user-count", "integrationId": "other-sonarr", "metric": "shows", "label": "My shows", "hidden": false, "compact": true},
		map[string]any{"id": "caddy-user-count", "integrationId": "other-caddy", "metric": "upstreams", "label": "My routes", "hidden": false, "compact": false},
	}
	fixture.fake.integrations = []map[string]any{
		{"id": "user-spoolman", "kind": "spoolman", "name": "My inventory", "url": "http://spoolman:8000", "secrets": []any{}},
		{"id": "other-sonarr", "kind": "sonarr", "name": "My shows", "url": "http://sonarr:8989"},
		{"id": "other-caddy", "kind": "caddy", "name": "My routes", "url": "http://caddy:2015"},
	}
	fixture.fake.items = []map[string]any{{"id": "shared-counts", "kind": "stats", "integrationIds": []any{"other-sonarr", "other-caddy"}, "options": map[string]any{"entries": entries, "table": true}, "size": map[string]any{"width": 4, "height": 3}}}
	link := fixture.link(t)
	for _, peer := range []string{"sonarr", "caddy"} {
		if err := fixture.from.SetSecret("HOMARR_LINKED_"+strings.ToUpper(peer), "other-"+peer); err != nil {
			t.Fatal(err)
		}
		if err := fixture.from.SetSecret("HOMARR_WIDGETS_OTHER_"+strings.ToUpper(peer), `{"shared-counts":true}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err != nil {
		t.Fatal(err)
	}
	owned := fixture.from.Secret("HOMARR_LINKED_SPOOLMAN")
	if owned == "" || owned == "user-spoolman" {
		t.Fatal("matching user integration must never be adopted")
	}
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err != nil || fixture.from.Secret("HOMARR_LINKED_SPOOLMAN") != owned {
		t.Fatalf("owned integration changed on rerun: %v", err)
	}
	fixture.mu.Lock()
	reads, refreshes := fixture.peerReads, fixture.refreshes
	fixture.protectedPath, fixture.protectedStatus = "/api/v1/health", http.StatusForbidden
	fixture.mu.Unlock()
	if err := link.Remove(t.Context(), fixture.from, fixture.to); err != nil {
		t.Fatal(err)
	}
	if fixture.peerReads != reads || fixture.refreshes != refreshes {
		t.Fatal("owned cleanup must not probe a protected peer or refresh Stats")
	}
	fixture.fake.mu.Lock()
	defer fixture.fake.mu.Unlock()
	if len(fixture.fake.integrations) != 3 {
		t.Fatal("removal changed user or unrelated cross-kind integrations")
	}
	for _, integration := range fixture.fake.integrations {
		if integration["id"] == owned {
			t.Fatal("unused owned Spoolman integration survived removal")
		}
	}
	var shared map[string]any
	for _, item := range fixture.fake.items {
		if item["kind"] == "stats" {
			if shared != nil {
				t.Fatal("removal duplicated the shared survivor widget")
			}
			shared = item
		}
	}
	options, _ := shared["options"].(map[string]any)
	if shared["id"] != "shared-counts" || !reflect.DeepEqual(options["entries"], entries) || options["table"] != true || !reflect.DeepEqual(shared["integrationIds"], []any{"other-sonarr", "other-caddy"}) {
		t.Fatal("removal changed cross-kind bindings, labels, options or shared widget identity")
	}
}

func TestSpoolmanLinkReleasesStillUsedOwnedIntegrationAndNeverReclaimsIt(t *testing.T) {
	fixture := newSpoolmanLinkFixture()
	link := fixture.link(t)
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err != nil {
		t.Fatal(err)
	}
	retainedID := fixture.from.Secret("HOMARR_LINKED_SPOOLMAN")
	fixture.fake.mu.Lock()
	fixture.fake.items = append(fixture.fake.items, map[string]any{
		"id": "person-spool-count", "kind": "stats", "integrationIds": []any{retainedID},
		"options": map[string]any{"table": true, "entries": []any{map[string]any{"id": "person-count", "integrationId": retainedID, "metric": "spools", "label": "My inventory", "compact": true}}},
	})
	fixture.fake.mu.Unlock()
	if err := link.Remove(t.Context(), fixture.from, fixture.to); err != nil {
		t.Fatal(err)
	}
	if fixture.from.Secret("HOMARR_LINKED_SPOOLMAN") != "" {
		t.Fatal("still-used integration ownership was not released")
	}
	fixture.fake.mu.Lock()
	if len(fixture.fake.integrations) != 1 || fixture.fake.integrations[0]["id"] != retainedID || len(fixture.fake.apps) != 1 || fixture.fake.apps[0]["description"] != "self-host retained tile:spoolman" {
		t.Fatal("user-bound integration and retained tile must survive release")
	}
	statistics := 0
	for _, item := range fixture.fake.items {
		if item["kind"] == "stats" {
			statistics++
			if item["id"] != "person-spool-count" {
				t.Fatal("owned removal changed the person's widget")
			}
		}
	}
	if statistics != 1 {
		t.Fatal("owned removal lost or duplicated the person's widget")
	}
	fixture.fake.mu.Unlock()
	if err := link.Apply(t.Context(), fixture.from, fixture.to); err != nil {
		t.Fatal(err)
	}
	newID := fixture.from.Secret("HOMARR_LINKED_SPOOLMAN")
	if newID == "" || newID == retainedID {
		t.Fatal("reconnect must create a new owned integration instead of reclaiming a retained one")
	}
	fixture.fake.mu.Lock()
	defer fixture.fake.mu.Unlock()
	if len(fixture.fake.integrations) != 2 {
		t.Fatal("reconnect lost the retained integration or duplicated its new owned connection")
	}
}
