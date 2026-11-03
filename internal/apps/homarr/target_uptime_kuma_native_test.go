package homarr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type uptimeKumaContractFixture struct {
	family       uptimeKumaNativeFamily
	results      []any
	pageMonitors []any
	pageGroups   []any
	heartbeat    map[string]any
	stats        *statsFixture
	items        []any
	apps         []any
	integrations []any
	probes       []string
	nativeReads  []string
	mutations    int
	peerReads    int
	nativeStatus int
	infoStatus   int
	peerStatus   int
}

func uptimeKumaModernPage() map[string]any {
	now := time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	return map[string]any{
		"title": "Chosen page", "url": "https://browser.example/status/chosen-page",
		"incidents": []any{}, "maintenances": []any{},
		"groups": []any{map[string]any{"id": 7, "name": "Chosen group", "monitors": []any{
			map[string]any{"id": 1, "name": "Chosen first", "type": "http", "url": nil, "status": "up", "uptime24h": 99,
				"ping": 10, "lastCheckAt": now - 60_000, "statusSince": nil, "certificateDaysRemaining": nil, "tags": []any{},
				"heartbeats": []any{map[string]any{"status": "up", "time": now - 120_000, "ping": 20}, map[string]any{"status": "up", "time": now - 60_000, "ping": 10}}},
			map[string]any{"id": 2, "name": "Chosen second", "type": nil, "url": nil, "status": "unknown", "uptime24h": nil,
				"ping": nil, "lastCheckAt": nil, "statusSince": nil, "certificateDaysRemaining": nil, "tags": []any{}, "heartbeats": []any{}},
		}}},
	}
}

func uptimeKumaModernMonitors(page map[string]any) []any {
	return page["groups"].([]any)[0].(map[string]any)["monitors"].([]any)
}

func uptimeKumaNewContractFixture(t *testing.T, family uptimeKumaNativeFamily) (*uptimeKumaContractFixture, *helpers.H, *helpers.H) {
	t.Helper()
	f := &uptimeKumaContractFixture{
		family: family, stats: uptimeKumaStatsFixture(),
		pageMonitors: []any{map[string]any{"id": 1, "name": "Chosen first"}, map[string]any{"id": 2, "name": "Chosen second"}},
		integrations: []any{map[string]any{"id": "kuma-owned", "kind": "uptimeKuma", "url": "http://uptime-kuma:3001"}},
		heartbeat:    map[string]any{"heartbeatList": map[string]any{"1": []any{map[string]any{"status": 1, "time": "2026-10-13 12:00:00"}}, "2": []any{}}, "uptimeList": map[string]any{"1_24": 0.99}},
	}
	f.stats.keepValues = true
	result := map[string]any{"integrationId": "kuma-owned", "integrationUrl": "http://uptime-kuma:3001", "error": nil, "updatedAt": "2026-11-02T12:00:00.000Z"}
	if family == uptimeKumaStatusPages {
		result["statusPage"] = uptimeKumaModernPage()
	} else {
		result["dashboard"] = uptimeKumaDashboard()
	}
	f.results = []any{result}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.peerReads++
		if r.Method != http.MethodGet {
			t.Error("peer probe mutated selected page")
		}
		if f.peerStatus != 0 {
			w.WriteHeader(f.peerStatus)
			return
		}
		switch r.URL.Path {
		case "/api/status-page/chosen-page":
			groups := f.pageGroups
			if groups == nil {
				groups = []any{map[string]any{"id": 7, "name": "Chosen group", "monitorList": f.pageMonitors}}
			}
			json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"slug": "chosen-page"}, "publicGroupList": groups})
		case "/api/status-page/heartbeat/chosen-page":
			json.NewEncoder(w).Encode(f.heartbeat)
		default:
			t.Errorf("unselected peer path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)
	to := helpers.New(helpers.Config{App: "uptime-kuma", BaseURL: peer.URL, URL: peer.URL, HTTP: peer.Client(), Log: events.Discard(), Inputs: map[string]string{"homarr_status_page": "chosen-page"}, Secrets: statsSecrets{}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiKey") != "fixture-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			f.mutations++
		}
		switch r.URL.Path {
		case "/api/trpc/info.getInfo":
			if f.infoStatus != 0 {
				w.WriteHeader(f.infoStatus)
				w.Write([]byte("private upstream details"))
				return
			}
			uptimeKumaReply(w, map[string]any{"version": "2.10.1"})
		case "/api/trpc/integration.getKinds":
			uptimeKumaReply(w, []any{map[string]any{"kind": "uptimeKuma", "requiredSecrets": []any{[]any{"slug"}}}})
		case "/api/trpc/integration.all":
			uptimeKumaReply(w, f.integrations)
		case "/api/trpc/widget.uptimeKuma.getStatusPages", "/api/trpc/widget.uptimeKuma.getDashboard":
			var input struct {
				JSON struct {
					IntegrationIDs []string `json:"integrationIds"`
					Slug           string   `json:"statusPageSlug"`
				} `json:"json"`
			}
			if json.Unmarshal([]byte(r.URL.Query().Get("input")), &input) != nil {
				t.Fatal("invalid native input envelope")
			}
			proc := strings.TrimPrefix(r.URL.Path, "/api/trpc/")
			if proc == uptimeKumaStatusPages.procedure() && input.JSON.Slug != "chosen-page" {
				t.Error("modern native request lost selected slug")
			}
			if len(input.JSON.IntegrationIDs) == 0 {
				f.probes = append(f.probes, proc)
				if proc != family.procedure() {
					w.WriteHeader(http.StatusNotFound)
					json.NewEncoder(w).Encode(uptimeKumaMissingProcedure(proc))
					return
				}
				uptimeKumaReply(w, []any{})
				return
			}
			f.nativeReads = append(f.nativeReads, proc)
			if !reflect.DeepEqual(input.JSON.IntegrationIDs, []string{"kuma-owned"}) || proc != family.procedure() {
				t.Error("native query changed owned identity or admitted family")
			}
			if f.nativeStatus != 0 {
				w.WriteHeader(f.nativeStatus)
				json.NewEncoder(w).Encode(uptimeKumaMissingProcedure(proc))
				return
			}
			uptimeKumaReply(w, f.results)
		case "/api/trpc/widget.stats.catalog", "/api/trpc/widget.stats.snapshot", "/api/trpc/widget.stats.refresh":
			f.stats.ServeHTTP(w, r)
		case "/api/boards":
			json.NewEncoder(w).Encode([]any{map[string]any{"id": "home", "name": "Home"}})
		case "/api/trpc/board.getBoardByName":
			uptimeKumaReply(w, map[string]any{"id": "home", "name": "Home", "sections": []any{}, "items": f.items})
		case "/api/trpc/board.saveBoard":
			var input struct {
				JSON struct {
					Items []any `json:"items"`
				} `json:"json"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			f.items = input.JSON.Items
			uptimeKumaReply(w, nil)
		case "/api/boards/items":
			var item map[string]any
			json.NewDecoder(r.Body).Decode(&item)
			item["id"] = "kuma-new-native"
			f.items = append(f.items, item)
			json.NewEncoder(w).Encode(map[string]any{"itemId": "kuma-new-native"})
		case "/api/apps":
			json.NewEncoder(w).Encode(f.apps)
		case "/api/apps/kuma-app":
			if r.Method != http.MethodPatch {
				t.Errorf("unexpected App mutation %s", r.Method)
			}
			var item map[string]any
			json.NewDecoder(r.Body).Decode(&item)
			item["id"] = "kuma-app"
			f.apps = []any{item}
			json.NewEncoder(w).Encode(item)
		default:
			t.Errorf("unexpected Homarr operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	secrets := statsSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_LINKED_UPTIME_KUMA": "kuma-owned", "HOMARR_WIDGETS_KUMA_OWNED": `{"kuma-native":true,"shared-stats":true}`, "HOMARR_APP_IDS": `{"uptime-kuma":"kuma-app"}`}
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: secrets})
	descriptor := uptimeKumaTargetForFamily(family)
	secrets[integrationFingerprintKey(from, "kuma-owned")] = integrationFingerprint(descriptor, from, to)
	f.apps = []any{map[string]any{"id": "kuma-app", "name": "Uptime Kuma", "description": installedTileMarker + "uptime-kuma", "href": peer.URL, "pingUrl": "http://uptime-kuma:3001", "iconUrl": "keep-icon"}}
	entries := widgetOptions(descriptor.widgets[1], "kuma-owned")["entries"].([]any)
	entries = append(entries, map[string]any{"id": "survivor-entry", "integrationId": "caddy-owned", "metric": "requests", "label": "Keep survivor", "hidden": false, "compact": false})
	f.items = []any{
		map[string]any{"id": "kuma-native", "kind": "uptimeKuma", "options": map[string]any{"showUpCount": true, "custom": "keep"}, "integrationIds": []any{"kuma-owned"}},
		map[string]any{"id": "shared-stats", "kind": "stats", "size": map[string]any{"width": 4, "height": 3}, "options": map[string]any{"entries": entries}, "integrationIds": []any{"kuma-owned", "caddy-owned"}},
		map[string]any{"id": "user-native", "kind": "uptimeKuma", "options": map[string]any{"statusPageSlug": "user-page"}, "integrationIds": []any{"kuma-owned"}},
		map[string]any{"id": "home-tile", "kind": "app", "options": map[string]any{"appId": "kuma-app"}},
		map[string]any{"id": "assistant", "kind": "assistant", "options": map[string]any{}},
		map[string]any{"id": "notebook", "kind": "notebook", "size": map[string]any{"width": 3, "height": 5}, "options": map[string]any{"content": notebookContent([]notebookService{{name: "Homarr", href: server.URL}, {name: "Uptime Kuma", href: peer.URL}})}},
	}
	return f, from, to
}

func TestUptimeKumaNativePreparationRejectsBeforeMutationWithoutFallback(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*uptimeKumaContractFixture, *helpers.H, *helpers.H)
	}{
		{"first install", func(_ *uptimeKumaContractFixture, from, _ *helpers.H) {
			from.SetSecret(from.Key("linked_uptime-kuma"), "")
		}},
		{"missing widget ownership", func(_ *uptimeKumaContractFixture, from, _ *helpers.H) {
			from.SetSecret(from.Key("widgets_kuma-owned"), "")
		}},
		{"credential drift", func(_ *uptimeKumaContractFixture, from, _ *helpers.H) {
			from.SetSecret(integrationFingerprintKey(from, "kuma-owned"), "different")
		}},
		{"unselected page", func(_ *uptimeKumaContractFixture, _, to *helpers.H) { to.Inputs["homarr_status_page"] = "" }},
		{"private selected page", func(f *uptimeKumaContractFixture, _, _ *helpers.H) { f.peerStatus = 401 }},
		{"Homarr authentication", func(f *uptimeKumaContractFixture, _, _ *helpers.H) { f.infoStatus = 401 }},
		{"native authentication", func(f *uptimeKumaContractFixture, _, _ *helpers.H) { f.nativeStatus = 403 }},
		{"native procedure disappearance", func(f *uptimeKumaContractFixture, _, _ *helpers.H) { f.nativeStatus = 404 }},
		{"native internal failure", func(f *uptimeKumaContractFixture, _, _ *helpers.H) { f.nativeStatus = 500 }},
		{"different integration", func(f *uptimeKumaContractFixture, _, _ *helpers.H) {
			f.results[0].(map[string]any)["integrationId"] = "unowned"
		}},
		{"different kind", func(f *uptimeKumaContractFixture, _, _ *helpers.H) {
			f.integrations[0].(map[string]any)["kind"] = "caddy"
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
			scenario.change(f, from, to)
			for _, apply := range []bool{false, true} {
				var err error
				if apply {
					err = uptimeKumaTarget().add(t.Context(), from, to)
				} else {
					_, err = uptimeKumaTarget().linked(t.Context(), from, to)
				}
				if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private upstream details") || f.mutations != 0 || f.stats.catalogs != 0 || f.stats.refreshes != 0 || slices.Contains(f.probes, uptimeKumaLegacyDashboard.procedure()) {
					t.Fatalf("unsafe admission err=%v writes=%d probes=%v Stats=%d/%d", err, f.mutations, f.probes, f.stats.catalogs, f.stats.refreshes)
				}
			}
		})
	}
}

func TestUptimeKumaModernNativeSemantics(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(map[string]any, *uptimeKumaContractFixture)
		valid  bool
	}{
		{"nullable unknown", func(map[string]any, *uptimeKumaContractFixture) {}, true},
		{"stale still has 24h uptime", func(p map[string]any, _ *uptimeKumaContractFixture) {
			monitor := uptimeKumaModernMonitors(p)[0].(map[string]any)
			monitor["status"] = "stale"
			monitor["lastCheckAt"] = monitor["lastCheckAt"].(int64) - 1_800_000
			for _, raw := range monitor["heartbeats"].([]any) {
				beat := raw.(map[string]any)
				beat["time"] = beat["time"].(int64) - 1_800_000
			}
		}, true},
		{"zero uptime", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[0].(map[string]any)["uptime24h"] = 0
		}, true},
		{"missing nullable uptime", func(p map[string]any, _ *uptimeKumaContractFixture) {
			delete(uptimeKumaModernMonitors(p)[1].(map[string]any), "uptime24h")
		}, false},
		{"fractional id", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[0].(map[string]any)["id"] = 1.5
		}, false},
		{"wrong name", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[0].(map[string]any)["name"] = "Unselected"
		}, false},
		{"missing monitor", func(p map[string]any, _ *uptimeKumaContractFixture) {
			p["groups"].([]any)[0].(map[string]any)["monitors"] = uptimeKumaModernMonitors(p)[:1]
		}, false},
		{"null groups", func(p map[string]any, _ *uptimeKumaContractFixture) { p["groups"] = nil }, false},
		{"wrong slug", func(p map[string]any, _ *uptimeKumaContractFixture) {
			p["url"] = "https://browser.example/status/default"
		}, false},
		{"old paused label", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[1].(map[string]any)["status"] = "paused"
		}, false},
		{"uptime outside range", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[0].(map[string]any)["uptime24h"] = 101
		}, false},
		{"heartbeat disagreement", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[0].(map[string]any)["status"] = "down"
		}, false},
		{"missing heartbeat field", func(p map[string]any, _ *uptimeKumaContractFixture) {
			delete(uptimeKumaModernMonitors(p)[1].(map[string]any), "heartbeats")
		}, false},
		{"known without heartbeat", func(p map[string]any, _ *uptimeKumaContractFixture) {
			uptimeKumaModernMonitors(p)[1].(map[string]any)["status"] = "up"
		}, false},
		{"unexpected duplicate", func(p map[string]any, _ *uptimeKumaContractFixture) {
			p["groups"].([]any)[0].(map[string]any)["monitors"] = append(uptimeKumaModernMonitors(p), uptimeKumaModernMonitors(p)[0])
		}, false},
		{"expected duplicate", func(p map[string]any, f *uptimeKumaContractFixture) {
			p["groups"].([]any)[0].(map[string]any)["monitors"] = append(uptimeKumaModernMonitors(p), uptimeKumaModernMonitors(p)[0])
			f.pageMonitors = append(f.pageMonitors, f.pageMonitors[0])
		}, true},
		{"conflicting duplicate", func(p map[string]any, f *uptimeKumaContractFixture) {
			duplicate := map[string]any{}
			raw, _ := json.Marshal(uptimeKumaModernMonitors(p)[0])
			json.Unmarshal(raw, &duplicate)
			duplicate["status"] = "down"
			for _, beat := range duplicate["heartbeats"].([]any) {
				beat.(map[string]any)["status"] = "down"
			}
			p["groups"].([]any)[0].(map[string]any)["monitors"] = append(uptimeKumaModernMonitors(p), duplicate)
			f.pageMonitors = append(f.pageMonitors, f.pageMonitors[0])
		}, false},
		{"empty selected page", func(p map[string]any, f *uptimeKumaContractFixture) {
			p["groups"] = []any{}
			f.pageMonitors = []any{}
			f.pageGroups = []any{}
		}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
			scenario.change(f.results[0].(map[string]any)["statusPage"].(map[string]any), f)
			_, err := uptimeKumaTarget().prepare(t.Context(), from, to)
			if (err == nil) != scenario.valid || f.mutations != 0 || f.stats.refreshes != 0 {
				t.Fatalf("native contract valid=%v err=%v writes=%d refreshes=%d", scenario.valid, err, f.mutations, f.stats.refreshes)
			}
		})
	}
	for _, status := range []string{"down", "pending", "maintenance", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
			monitor := uptimeKumaModernMonitors(f.results[0].(map[string]any)["statusPage"].(map[string]any))[0].(map[string]any)
			monitor["status"] = status
			for _, raw := range monitor["heartbeats"].([]any) {
				raw.(map[string]any)["status"] = status
			}
			if _, err := uptimeKumaTarget().prepare(t.Context(), from, to); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUptimeKumaModernNullableStatsKeepLegacyStrict(t *testing.T) {
	for _, family := range []uptimeKumaNativeFamily{uptimeKumaStatusPages, uptimeKumaLegacyDashboard} {
		for _, missing := range []bool{false, true} {
			t.Run(family.procedure()+"/missing="+map[bool]string{true: "true", false: "false"}[missing], func(t *testing.T) {
				f, from, to := uptimeKumaNewContractFixture(t, family)
				f.stats.values["averageUptimePercent"] = nil
				if missing {
					delete(f.stats.values, "averageUptimePercent")
				}
				descriptor, err := uptimeKumaTarget().prepare(t.Context(), from, to)
				if err != nil {
					t.Fatal(err)
				}
				err = descriptor.verifyData(t.Context(), from, to, "kuma-owned")
				want := family == uptimeKumaStatusPages && !missing
				if (err == nil) != want || f.stats.catalogs != 1 || f.stats.snapshots != 1 || f.stats.refreshes != 1 || !f.stats.force {
					t.Fatalf("err=%v want=%v catalogs=%d snapshots=%d refreshes=%d", err, want, f.stats.catalogs, f.stats.snapshots, f.stats.refreshes)
				}
			})
		}
	}
}

func TestUptimeKumaMigrationRerunAndUnavailablePeerRemoval(t *testing.T) {
	f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	if err := uptimeKumaTarget().add(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	if ready, err := uptimeKumaTarget().linked(t.Context(), from, to); err != nil || !ready {
		t.Fatalf("rerun ready=%v err=%v", ready, err)
	}
	items := itemsOf(map[string]any{"items": f.items})
	if len(items) != 6 || items[0]["id"] != "kuma-native" || items[0]["options"].(map[string]any)["statusPageSlug"] != "chosen-page" || items[0]["options"].(map[string]any)["custom"] != "keep" || items[2]["options"].(map[string]any)["statusPageSlug"] != "user-page" || from.Secret(from.Key("linked_uptime-kuma")) != "kuma-owned" {
		t.Fatal("rerun changed owned IDs or user options")
	}
	if f.stats.catalogs != 2 || f.stats.snapshots != 2 || f.stats.refreshes != 2 {
		t.Fatal("each separate verification must share exactly one central sample")
	}
	probes, native, peerReads := len(f.probes), len(f.nativeReads), f.peerReads
	refreshes := f.stats.refreshes
	f.peerStatus, f.nativeStatus = 503, 503
	to.BaseURL = "http://127.0.0.1:1"
	if err := uptimeKumaTarget().remove(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	if len(f.probes) != probes || len(f.nativeReads) != native || f.peerReads != peerReads || f.stats.refreshes != refreshes {
		t.Fatal("removal queried peer/native capabilities or Stats")
	}
	items = itemsOf(map[string]any{"items": f.items})
	if slices.ContainsFunc(items, func(item map[string]any) bool { return item["id"] == "kuma-native" || item["id"] == "home-tile" }) || !hasWidget(items, "uptimeKuma", "kuma-owned") || from.Secret(from.Key("linked_uptime-kuma")) != "" || len(f.integrations) != 1 {
		t.Fatal("removal deleted user bindings or retained ownership")
	}
	for _, item := range items {
		if item["id"] == "shared-stats" {
			entries := item["options"].(map[string]any)["entries"].([]any)
			if !reflect.DeepEqual(item["integrationIds"], []any{"caddy-owned"}) || len(entries) != 1 || entries[0].(map[string]any)["id"] != "survivor-entry" {
				t.Fatal("removal changed the cross-kind survivor partition")
			}
		}
	}
	if !strings.HasPrefix(f.apps[0].(map[string]any)["description"].(string), retainedTileMarker) || f.apps[0].(map[string]any)["iconUrl"] != "keep-icon" {
		t.Fatal("retained App fields or ownership release changed")
	}
}

func TestUptimeKumaModernStatsEmptyAndIndependentSamples(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		empty   bool
		average any
		valid   bool
	}{
		{"empty unknown average", true, nil, true},
		{"empty fabricated zero", true, float64(0), false},
		{"zero observed uptime", false, float64(0), true},
		{"independent changed sample", false, float64(98), true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
			if scenario.empty {
				f.pageMonitors = []any{}
				f.pageGroups = []any{}
				f.results[0].(map[string]any)["statusPage"].(map[string]any)["groups"] = []any{}
				f.stats.values = map[string]any{"totalMonitors": float64(0), "upCount": float64(0), "downCount": float64(0)}
			} else {
				f.stats.values = map[string]any{"totalMonitors": float64(1), "upCount": float64(0), "downCount": float64(1)}
			}
			f.stats.values["averageUptimePercent"] = scenario.average
			descriptor, err := uptimeKumaTarget().prepare(t.Context(), from, to)
			if err != nil {
				t.Fatal(err)
			}
			err = descriptor.verifyData(t.Context(), from, to, "kuma-owned")
			if (err == nil) != scenario.valid || f.stats.catalogs != 1 || f.stats.snapshots != 1 || f.stats.refreshes != 1 || !f.stats.force {
				t.Fatalf("valid=%v err=%v catalog/snapshot/refresh=%d/%d/%d", scenario.valid, err, f.stats.catalogs, f.stats.snapshots, f.stats.refreshes)
			}
		})
	}
}

func TestUptimeKumaDoesNotAdoptMatchingUserWidget(t *testing.T) {
	f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	f.items = f.items[1:]
	if err := from.SetSecret(from.Key("widgets_kuma-owned"), `{"shared-stats":true}`); err != nil {
		t.Fatal(err)
	}
	if ready, err := uptimeKumaTarget().linked(t.Context(), from, to); err != nil || ready {
		t.Fatalf("matching user widget satisfied owned option selection: ready=%v err=%v", ready, err)
	}
	if err := uptimeKumaTarget().add(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	owned, err := ownedWidgets(from, "kuma-owned")
	if err != nil || !owned["kuma-new-native"] || owned["user-native"] {
		t.Fatalf("new ownership adopted a user widget: %v err=%v", owned, err)
	}
	for _, item := range itemsOf(map[string]any{"items": f.items}) {
		if item["id"] == "user-native" && item["options"].(map[string]any)["statusPageSlug"] != "user-page" {
			t.Fatal("user page selection was overwritten")
		}
	}
}

func TestUptimeKumaPreparedFamiliesAreInvocationLocal(t *testing.T) {
	descriptor := uptimeKumaTarget()
	modernFixture, modernFrom, modernTo := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	_, legacyFrom, legacyTo := uptimeKumaNewContractFixture(t, uptimeKumaLegacyDashboard)
	modern, err := descriptor.prepare(t.Context(), modernFrom, modernTo)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := descriptor.prepare(t.Context(), legacyFrom, legacyTo)
	if err != nil {
		t.Fatal(err)
	}
	modern.widgets[0].options["statusPageSlug"] = "changed-local-copy"
	again, err := descriptor.prepare(t.Context(), modernFrom, modernTo)
	if err != nil {
		t.Fatal(err)
	}
	if again.widgets[0].options["statusPageSlug"] != "chosen-page" || legacy.widgets[0].options["showUpCount"] != true || legacy.widgets[0].options["statusPageSlug"] != nil || len(legacy.nullableStats) != 0 || len(again.nullableStats) != 1 || descriptor.widgets[0].options["statusPageSlug"] != nil || len(modernFixture.probes) != 2 {
		t.Fatal("native family/options/null policy leaked between invocations or capability results were cached")
	}
}
