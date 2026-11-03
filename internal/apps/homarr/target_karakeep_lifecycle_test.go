package homarr_test

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/apps/homarr"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func TestKarakeepManualLinkPreservesSharedOwnershipAcrossRerunRemovalAndReconnect(t *testing.T) {
	link, to, native := homarr.KarakeepLifecycleFixtureForTest(t)
	fake := newFake()
	fake.apiKey = "fixture-key"
	base := fake.handler()
	statRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/trpc/integration.getKinds" || r.URL.Path == "/api/trpc/integration.request" || strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
			if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
				statRequests++
			}
			native.ServeHTTP(w, r)
			return
		}
		base.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	state := fakeSecrets{"HOMARR_API_KEY": "fixture-key"}
	from := homarrH(t, server, state)
	records := []helpers.StepResult{}
	from.Record = func(result helpers.StepResult) { records = append(records, result) }
	from.Now = func() time.Time { return time.UnixMilli(1700000000000) }
	if err := link.Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unproved saved key did not remain Manual: %v", err)
	}
	first := from.Secret(from.Key("linked_karakeep"))
	if first == "" {
		t.Fatal("actual returned integration ID was not recorded")
	}
	before := maps.Clone(state)
	fake.mu.Lock()
	for _, integration := range fake.integrations {
		if integration["id"] == first {
			integration["secrets"] = []any{map[string]any{"kind": "apiKey", "value": "ak2_cccccccccccccccccccc_dddddddddddddddddddddddddddddddd"}}
		}
	}
	fake.mu.Unlock()
	if err := link.Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("rerun accepted unproved saved credential: %v", err)
	}
	if statRequests != 0 {
		t.Fatal("unproved saved credential reached native Stats")
	}
	if !maps.Equal(before, state) || len(records) != 2 || records[1].Status != stepstatus.StepManual || !strings.Contains(records[1].Hint, "keyId") || !strings.Contains(records[1].Hint, "self-host setup homarr") {
		t.Fatal("unchanged local fingerprint bypassed the persisted actionable Manual boundary")
	}
	if from.Secret(from.Key("linked_karakeep")) != first {
		t.Fatal("rerun changed owned integration identity")
	}
	fake.mu.Lock()
	fake.integrations = append(fake.integrations, map[string]any{"id": "karakeep-survivor-sonarr", "kind": "sonarr", "url": "http://sonarr:8989"})
	fake.items = append(fake.items, map[string]any{"id": "karakeep-user-widget", "kind": "stats", "integrationIds": []any{first, "karakeep-survivor-sonarr"}, "options": map[string]any{"entries": []any{
		map[string]any{"id": "user-karakeep-entry", "integrationId": first, "metric": "bookmarks"},
		map[string]any{"id": "user-sonarr-entry", "integrationId": "karakeep-survivor-sonarr", "metric": "shows"},
	}}})
	fake.mu.Unlock()
	key := to.Secret(to.Key("homarr_api_key"))
	if err := to.SetSecret(to.Key("homarr_api_key"), ""); err != nil {
		t.Fatal(err)
	}
	beforeStats := statRequests
	if err := link.Remove(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	if statRequests != beforeStats {
		t.Fatal("removal acquired native Stats")
	}
	if from.Secret(from.Key("linked_karakeep")) != "" {
		t.Fatal("retained integration ownership was not released")
	}
	fake.mu.Lock()
	retained, survivor, userWidget, foreignEntry := false, false, false, false
	for _, integration := range fake.integrations {
		retained = retained || integration["id"] == first
		survivor = survivor || integration["id"] == "karakeep-survivor-sonarr"
	}
	for _, item := range fake.items {
		if item["id"] == "karakeep-user-widget" {
			userWidget = true
			options, _ := item["options"].(map[string]any)
			entries, _ := options["entries"].([]any)
			for _, raw := range entries {
				entry, _ := raw.(map[string]any)
				foreignEntry = foreignEntry || entry["id"] == "user-sonarr-entry"
			}
		}
	}
	fake.mu.Unlock()
	if !retained || !userWidget || !survivor || !foreignEntry {
		t.Fatal("user binding, retained integration or cross-kind survivor was deleted")
	}
	if err := to.SetSecret(to.Key("homarr_api_key"), key); err != nil {
		t.Fatal(err)
	}
	if err := link.Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("reconnect accepted unproved saved credential: %v", err)
	}
	if id := from.Secret(from.Key("linked_karakeep")); id == "" || id == first {
		t.Fatal("reconnect reclaimed a retained user connection")
	}
}
