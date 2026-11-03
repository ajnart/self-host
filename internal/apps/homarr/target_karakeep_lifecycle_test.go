package homarr_test

import (
	"bytes"
	"encoding/json"
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

type karakeepTrackedSecrets struct {
	helpers.Secrets
	writes int
}

func (s *karakeepTrackedSecrets) Set(key, value string) error {
	s.writes++
	return s.Secrets.Set(key, value)
}

func TestKarakeepLinkCheckAndApplyHoldBeforeReconciliation(t *testing.T) {
	for _, savedKey := range []struct{ name, value string }{
		{name: "owned A", value: "ak2_aaaaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{name: "same account B", value: "ak2_cccccccccccccccccccc_dddddddddddddddddddddddddddddddd"},
	} {
		for _, address := range []string{"http://karakeep:3000", "https://edited-karakeep.example.test"} {
			for _, fingerprint := range []string{"current", "stale", "missing"} {
				for _, operation := range []string{"Check", "Apply"} {
					t.Run(savedKey.name+"/"+address+"/"+fingerprint+"/"+operation, func(t *testing.T) {
						link, check, to, native, expectedFingerprint := homarr.KarakeepLifecycleFixtureForTest(t)
						fake := newFake()
						fake.apiKey = "fixture-key"
						fake.apps = []map[string]any{{"id": "karakeep-app", "name": "Edited name", "description": "self-host tile:karakeep", "href": to.URL, "pingUrl": to.InternalURL()}}
						fake.integrations = []map[string]any{{"id": "karakeep-owned", "kind": "karakeep", "name": "Edited connection", "url": address, "secrets": []any{map[string]any{"kind": "apiKey", "value": savedKey.value}}}}
						fake.items = []map[string]any{
							{"id": "karakeep-tile", "kind": "app", "options": map[string]any{"appId": "karakeep-app"}},
							{"id": "user-stats", "kind": "stats", "integrationIds": []any{"karakeep-owned"}, "options": map[string]any{"entries": []any{map[string]any{"id": "user-entry", "integrationId": "karakeep-owned", "metric": "bookmarks", "label": "User label"}}}},
						}
						base := fake.handler()
						requests, integrationWrites, widgetWrites, statsReads := 0, 0, 0, 0
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests++
							if r.Method != http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/trpc/integration.") && r.URL.Path != "/api/trpc/integration.request" {
								integrationWrites++
							}
							if r.Method != http.MethodGet && (strings.HasPrefix(r.URL.Path, "/api/boards") || r.URL.Path == "/api/trpc/board.saveBoard") {
								widgetWrites++
							}
							if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
								statsReads++
							}
							if r.URL.Path == "/api/trpc/integration.getKinds" || r.URL.Path == "/api/trpc/integration.request" || strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
								native.ServeHTTP(w, r)
								return
							}
							base.ServeHTTP(w, r)
						}))
						t.Cleanup(server.Close)
						state := fakeSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_LINKED_KARAKEEP": "karakeep-owned", "HOMARR_APP_IDS": `{"karakeep":"karakeep-app"}`, "HOMARR_WIDGETS_KARAKEEP_OWNED": "{}"}
						if fingerprint == "current" {
							state["HOMARR_INTEGRATION_KARAKEEP_OWNED"] = expectedFingerprint
						}
						if fingerprint == "stale" {
							state["HOMARR_INTEGRATION_KARAKEEP_OWNED"] = "old-fingerprint"
						}
						from := homarrH(t, server, state)
						fromSecrets := &karakeepTrackedSecrets{Secrets: from.Secrets}
						toSecrets := &karakeepTrackedSecrets{Secrets: to.Secrets}
						from.Secrets, to.Secrets = fromSecrets, toSecrets
						records := []helpers.StepResult{}
						from.Record = func(result helpers.StepResult) { records = append(records, result) }
						beforeState := maps.Clone(state)
						before, err := json.Marshal([]any{fake.apps, fake.integrations, fake.items, toSecrets.Secrets})
						if err != nil {
							t.Fatal(err)
						}
						if operation == "Check" {
							var ready bool
							ready, err = check(t.Context(), from, to)
							if ready {
								t.Fatal("unproved saved key completed LinkCheck")
							}
						} else {
							err = link.Apply(t.Context(), from, to)
						}
						var manual *helpers.ManualError
						if !errors.As(err, &manual) || !strings.Contains(manual.Hint, "keyId") {
							t.Fatalf("missing exact-key-proof Manual: %v", err)
						}
						fake.mu.Lock()
						after, snapshotErr := json.Marshal([]any{fake.apps, fake.integrations, fake.items, toSecrets.Secrets})
						fake.mu.Unlock()
						if snapshotErr != nil {
							t.Fatal(snapshotErr)
						}
						if requests != 0 || integrationWrites != 0 || widgetWrites != 0 || statsReads != 0 || fromSecrets.writes != 0 || toSecrets.writes != 0 || !maps.Equal(beforeState, state) || !bytes.Equal(before, after) {
							t.Fatalf("hold was after reconciliation: requests=%d integrations=%d widgets=%d Stats=%d local writes=%d", requests, integrationWrites, widgetWrites, statsReads, fromSecrets.writes+toSecrets.writes)
						}
						if operation == "Apply" && (len(records) != 1 || records[0].Status != stepstatus.StepManual || records[0].Hint != manual.Hint) {
							t.Fatal("Apply did not persist the exact-key-proof Manual hint")
						}
					})
				}
			}
		}
	}
}

func TestKarakeepManualLinkPreservesSharedOwnershipAcrossRerunRemovalAndReconnect(t *testing.T) {
	link, _, to, native, expectedFingerprint := homarr.KarakeepLifecycleFixtureForTest(t)
	fake := newFake()
	fake.apiKey = "fixture-key"
	first := "karakeep-owned"
	fake.apps = []map[string]any{{"id": "karakeep-app", "name": "Karakeep", "description": "self-host tile:karakeep", "href": to.URL, "pingUrl": to.InternalURL()}}
	fake.integrations = []map[string]any{{"id": first, "kind": "karakeep", "name": "Karakeep", "url": to.InternalURL(), "secrets": []any{map[string]any{"kind": "apiKey", "value": to.Secret(to.Key("homarr_api_key"))}}}}
	fake.items = []map[string]any{{"id": "karakeep-tile", "kind": "app", "options": map[string]any{"appId": "karakeep-app"}}}
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
	state := fakeSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_LINKED_KARAKEEP": first, "HOMARR_APP_IDS": `{"karakeep":"karakeep-app"}`, "HOMARR_WIDGETS_KARAKEEP_OWNED": "{}", "HOMARR_INTEGRATION_KARAKEEP_OWNED": expectedFingerprint}
	from := homarrH(t, server, state)
	records := []helpers.StepResult{}
	from.Record = func(result helpers.StepResult) { records = append(records, result) }
	from.Now = func() time.Time { return time.UnixMilli(1700000000000) }
	if err := link.Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unproved saved key did not remain Manual: %v", err)
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
	fake.mu.Lock()
	beforeReconnect, err := json.Marshal([]any{fake.apps, fake.integrations, fake.items})
	fake.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	beforeState := maps.Clone(state)
	if err := link.Apply(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("reconnect accepted unproved saved credential: %v", err)
	}
	fake.mu.Lock()
	afterReconnect, err := json.Marshal([]any{fake.apps, fake.integrations, fake.items})
	fake.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if from.Secret(from.Key("linked_karakeep")) != "" || !maps.Equal(beforeState, state) || !bytes.Equal(beforeReconnect, afterReconnect) || statRequests != beforeStats {
		t.Fatal("Manual reconnect mutated or reclaimed retained user resources")
	}
}
