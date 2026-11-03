package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type externalConnectionFixture struct {
	t            *testing.T
	integrations []map[string]any
	items        []map[string]any
	userItems    []map[string]any
	created      int
	updated      int
	deleted      int
	requests     int
	clearedApps  int
	detail       map[string]any
	detailStatus int
}

func externalConnectionReply(w http.ResponseWriter, value any) {
	json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
}

func (f *externalConnectionFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests++
	if r.Header.Get("ApiKey") != "external-fixture-api-key" {
		f.t.Error("missing Homarr authentication")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var input struct {
		JSON map[string]any `json:"json"`
	}
	if r.Method == http.MethodPost && r.URL.Path != "/api/boards/items" {
		json.NewDecoder(r.Body).Decode(&input)
	}
	switch r.URL.Path {
	case "/api/trpc/info.getInfo":
		externalConnectionReply(w, map[string]any{"version": "2.10.1"})
	case "/api/trpc/integration.getKinds":
		externalConnectionReply(w, []any{map[string]any{"kind": "testPeer", "requiredSecrets": [][]string{{"apiKey"}}}})
	case "/api/trpc/integration.all":
		externalConnectionReply(w, f.integrations)
	case "/api/trpc/integration.byId":
		if f.detailStatus != 0 {
			w.WriteHeader(f.detailStatus)
			return
		}
		externalConnectionReply(w, f.detail)
	case "/api/trpc/integration.create":
		if _, found := input.JSON["app"]; found {
			f.t.Error("external connection fabricated a linked Homarr App")
		}
		f.created++
		record := map[string]any{"id": "external-owned-" + strconv.Itoa(f.created), "kind": input.JSON["kind"], "url": input.JSON["url"]}
		f.integrations = append(f.integrations, record)
		externalConnectionReply(w, map[string]any{"integration": record})
	case "/api/trpc/integration.update":
		if input.JSON["appId"] == nil && f.detail["app"] != nil {
			f.clearedApps++
		}
		if id, ok := input.JSON["appId"].(string); ok {
			f.detail["app"] = map[string]any{"id": id}
		} else {
			f.detail["app"] = nil
		}
		f.updated++
		externalConnectionReply(w, nil)
	case "/api/trpc/integration.delete":
		kept := []map[string]any{}
		for _, item := range f.integrations {
			if item["id"] == input.JSON["id"] {
				f.deleted++
				continue
			}
			kept = append(kept, item)
		}
		f.integrations = kept
		externalConnectionReply(w, nil)
	case "/api/boards":
		json.NewEncoder(w).Encode([]map[string]any{{"id": "home", "name": "Home"}, {"id": "user", "name": "User"}})
	case "/api/trpc/board.getBoardByName":
		json.Unmarshal([]byte(r.URL.Query().Get("input")), &input)
		items := f.items
		if input.JSON["name"] == "User" {
			items = f.userItems
		}
		externalConnectionReply(w, map[string]any{"id": "home", "name": input.JSON["name"], "items": items, "sections": []any{}, "layouts": []any{}})
	case "/api/boards/items":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["id"] = "external-widget-" + strconv.Itoa(len(f.items)+1)
		f.items = append(f.items, body)
		json.NewEncoder(w).Encode(map[string]any{"itemId": body["id"]})
	case "/api/trpc/board.saveBoard":
		f.items = nil
		for _, raw := range input.JSON["items"].([]any) {
			f.items = append(f.items, raw.(map[string]any))
		}
		externalConnectionReply(w, nil)
	default:
		f.t.Errorf("unexpected external reconciliation request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *externalConnectionFixture) helpers(t *testing.T) *helpers.H {
	t.Helper()
	f.t = t
	f.integrations = []map[string]any{{"id": "user-matching", "kind": "testPeer", "url": "https://private-external.example"}}
	f.items = []map[string]any{{"id": "unrelated-widget", "kind": "calendar", "integrationIds": []any{"unrelated-integration"}, "options": map[string]any{"unchanged": true}}}
	f.userItems = []map[string]any{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "external-fixture-api-key"}})
}

func externalConnectionTestTarget(preflight func(context.Context, *helpers.H, *helpers.H) error) target {
	return target{
		id: "external-0123456789abcdef0123456789abcdef", kind: "testPeer", name: "Test peer", integrationOnly: true,
		internal: fixed("https://private-external.example"), preflight: preflight,
		secrets: func(_, _ *helpers.H) []secret { return []secret{{Kind: "apiKey", Value: "external-provider-key"}} },
		widgets: []widget{plain("healthMonitoring")},
	}
}

func TestExternalConnectionReusesReconciliationWithoutAppOrTargetHelpers(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	target := externalConnectionTestTarget(func(context.Context, *helpers.H, *helpers.H) error { return nil })
	link := target.link()
	if link.Target != "" {
		t.Fatal("external connection entered the installed App target contract")
	}
	if err := link.Apply(t.Context(), h, nil); err != nil {
		t.Fatal(err)
	}
	owned := h.Secret(h.Key("linked_" + target.id))
	if owned == "" || owned == "user-matching" || f.created != 1 {
		t.Fatal("matching user integration was adopted")
	}
	f.items[1]["options"] = map[string]any{"userOption": "preserved"}
	before, _ := json.Marshal(f.items)
	if err := link.Apply(t.Context(), h, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(f.items)
	if f.created != 1 || f.updated != 0 || h.Secret(h.Key("linked_"+target.id)) != owned || !reflect.DeepEqual(before, after) {
		t.Fatal("rerun changed owned IDs, user options or layout")
	}
	f.userItems = []map[string]any{{"id": "user-retainer", "kind": "healthMonitoring", "integrationIds": []any{owned}, "options": map[string]any{}}}
	target.preflight = func(context.Context, *helpers.H, *helpers.H) error {
		t.Fatal("removal ran a provider preflight")
		return nil
	}
	target.secrets = func(_, _ *helpers.H) []secret { t.Fatal("removal read provider credentials"); return nil }
	target.internal = func(*helpers.H) string { t.Fatal("removal resolved the external endpoint"); return "" }
	if err := target.link().Remove(t.Context(), h, nil); err != nil {
		t.Fatal(err)
	}
	if f.deleted != 0 || len(f.items) != 1 || f.items[0]["id"] != "unrelated-widget" || h.Secret(h.Key("linked_"+target.id)) != "" || h.Secret(h.Key("widgets_"+owned)) != "" {
		t.Fatal("removal destroyed retained/user resources or failed to release ownership")
	}
	target = externalConnectionTestTarget(func(context.Context, *helpers.H, *helpers.H) error { return nil })
	if err := target.link().Apply(t.Context(), h, nil); err != nil {
		t.Fatal(err)
	}
	if f.created != 2 || h.Secret(h.Key("linked_"+target.id)) == owned {
		t.Fatal("reconnect reclaimed a retained integration")
	}
	if err := target.link().Remove(t.Context(), h, nil); err != nil {
		t.Fatal(err)
	}
	if f.deleted != 1 || len(f.integrations) != 2 || len(f.userItems) != 1 {
		t.Fatal("unreferenced removal affected the matching or retained user integrations")
	}
}

func TestExternalConnectionManualHoldAndMissingOwnershipPrecedeMutation(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	target := externalConnectionTestTarget(func(context.Context, *helpers.H, *helpers.H) error {
		return &helpers.ManualError{Hint: "Native confinement is unproved; keep this connection paused before credential forwarding."}
	})
	var recorded helpers.StepResult
	h.Record = func(r helpers.StepResult) { recorded = r }
	if err := target.link().Apply(t.Context(), h, nil); !errors.Is(err, helpers.ErrManual) || recorded.Hint == externalConnectionRecovery {
		t.Fatal("specific native Manual recovery was lost")
	}
	if f.requests != 0 {
		t.Fatal("Manual hold forwarded data or credentials to Homarr")
	}
	if err := h.SetSecret(h.Key("linked_"+target.id), "external-owned-lost"); err != nil {
		t.Fatal(err)
	}
	if err := target.link().Remove(t.Context(), h, nil); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("missing widget ownership was guessed")
	}
	if f.requests != 0 {
		t.Fatal("missing explicit widget ownership mutated Homarr")
	}
}

func TestExternalConnectionDTOCannotSerializePrivateFields(t *testing.T) {
	c := helpers.ExternalConnection{ID: "opaque", Kind: "testPeer", Endpoint: "private-endpoint", Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "private-key"}}, Selection: map[string]string{"node": "private-node"}}
	raw, err := json.Marshal(c)
	if err != nil || string(raw) != `{"id":"opaque","kind":"testPeer"}` {
		t.Fatal("external connection DTO exposed private fields")
	}
}

func TestExternalConnectionRefreshPreservesUserAppAssociation(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	target := externalConnectionTestTarget(nil)
	f.integrations = append(f.integrations, map[string]any{"id": "owned", "kind": target.kind, "url": target.internal(nil)})
	f.detail = map[string]any{"id": "owned", "kind": target.kind, "app": map[string]any{"id": "person-added-app"}}
	if err := h.SetSecret(h.Key("linked_"+target.id), "owned"); err != nil {
		t.Fatal(err)
	}
	if id, err := ensureIntegration(t.Context(), h, nil, target, ""); err != nil || id != "owned" {
		t.Fatalf("refresh: %v", err)
	}
	if f.updated != 1 || f.clearedApps != 0 || stringField(f.detail["app"].(map[string]any), "id") != "person-added-app" {
		t.Fatal("credential refresh cleared a person-added App association")
	}
	if h.Secret(h.Key("app_ids")) != "" {
		t.Fatal("preserving an association claimed ownership of the person's App")
	}
}

func TestExternalConnectionAmbiguousAppAssociationStopsBeforeRefresh(t *testing.T) {
	for _, scenario := range []string{"unavailable", "missing", "invalid", "wrong-id", "wrong-kind"} {
		t.Run(scenario, func(t *testing.T) {
			f := &externalConnectionFixture{}
			h := f.helpers(t)
			target := externalConnectionTestTarget(nil)
			f.integrations = append(f.integrations, map[string]any{"id": "owned", "kind": target.kind})
			f.detail = map[string]any{"id": "owned", "kind": target.kind, "app": map[string]any{"id": "person-added-app"}}
			switch scenario {
			case "unavailable":
				f.detailStatus = http.StatusServiceUnavailable
			case "missing":
				delete(f.detail, "app")
			case "invalid":
				f.detail["app"] = map[string]any{}
			case "wrong-id":
				f.detail["id"] = "different"
			case "wrong-kind":
				f.detail["kind"] = "different"
			}
			if err := h.SetSecret(h.Key("linked_"+target.id), "owned"); err != nil {
				t.Fatal(err)
			}
			if _, err := ensureIntegration(t.Context(), h, nil, target, ""); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("ambiguous association did not require Manual recovery")
			}
			if f.updated != 0 || f.clearedApps != 0 || h.Secret(integrationFingerprintKey(h, "owned")) != "" {
				t.Fatal("ambiguous association allowed credential or ownership mutation")
			}
		})
	}
}

func TestExternalConnectionSharedWidgetsRemainOwnedAcrossCatalogReconciliation(t *testing.T) {
	for _, kind := range []string{"stats", "calendar"} {
		t.Run(kind, func(t *testing.T) {
			f := &externalConnectionFixture{}
			h := f.helpers(t)
			c := helpers.ExternalConnection{ID: "0123456789abcdef0123456789abcdef", Kind: "unknown"}
			h.ExternalConnections = func() ([]helpers.ExternalConnection, error) { return []helpers.ExternalConnection{c}, nil }
			for key, value := range map[string]string{
				"linked_external-" + c.ID: "external", "widgets_external": `{"shared":true}`,
				"linked_sonarr": "catalog", "widgets_catalog": `{}`,
			} {
				if err := h.SetSecret(h.Key(key), value); err != nil {
					t.Fatal(err)
				}
			}
			w := plain(kind)
			if kind == "stats" {
				w = statWidget("requests")
			}
			f.items = append(f.items, map[string]any{"id": "shared", "kind": kind, "integrationIds": []any{"external"}, "options": widgetOptions(w, "external"), "size": map[string]any{"width": 2, "height": 1}})
			current := map[string]any{"items": []any{f.items[0], f.items[1]}}
			if ready, err := sharedWidgetCurrent(h, current, w, "external"); err != nil || ready {
				t.Fatal("external shared widget bypassed owned reconciliation as an unowned fallback")
			}
			owned := map[string]bool{}
			if err := ensureSharedWidget(t.Context(), h, w, "catalog", owned); err != nil {
				t.Fatal(err)
			}
			if len(f.items) != 2 || f.items[1]["id"] != "shared" || !wiredTo(f.items[1], kind, "external") || !wiredTo(f.items[1], kind, "catalog") {
				t.Fatal("Catalog reconciliation duplicated or lost the external shared widget")
			}
			current = map[string]any{"items": []any{f.items[0], f.items[1]}}
			if ready, err := sharedWidgetCurrent(h, current, w, "external"); err != nil || !ready {
				t.Fatal("external shared widget fell back to unowned detection")
			}
			if err := removeWidgets(t.Context(), h, "external", nil); err != nil {
				t.Fatal(err)
			}
			if len(f.items) != 2 || !wiredTo(f.items[1], kind, "catalog") || wiredTo(f.items[1], kind, "external") || f.items[0]["id"] != "unrelated-widget" {
				t.Fatal("external removal damaged the Catalog or unrelated survivor")
			}
		})
	}
}

func TestExternalConnectionInvalidInventoryPreservesSharedWidgets(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	h.ExternalConnections = func() ([]helpers.ExternalConnection, error) {
		return nil, &helpers.ManualError{Hint: "Repair the private external record without discarding ownership."}
	}
	before, _ := json.Marshal(f.items)
	if err := validateOwnership(h); err != nil {
		t.Fatal("external inventory blocked Homarr's own ownership step")
	}
	if err := ensureSharedWidget(t.Context(), h, plain("calendar"), "catalog", map[string]bool{}); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("unknown external inventory became an empty ownership set")
	}
	after, _ := json.Marshal(f.items)
	if !reflect.DeepEqual(before, after) || f.created != 0 || f.updated != 0 || f.deleted != 0 {
		t.Fatal("invalid external inventory mutated existing resources")
	}
}

func TestExternalConnectionCorruptBoundOwnershipHoldsOnlyRelatedMutations(t *testing.T) {
	for _, scenario := range []string{"stats", "stats-entry-only", "calendar", "healthMonitoring"} {
		t.Run(scenario, func(t *testing.T) {
			f := &externalConnectionFixture{}
			h := f.helpers(t)
			for key, value := range map[string]string{"linked_sonarr": "catalog", "widgets_catalog": `{"shared":true}`, "widgets_external": "null"} {
				if err := h.SetSecret(h.Key(key), value); err != nil {
					t.Fatal(err)
				}
			}
			kind := scenario
			ids := []any{"external", "catalog"}
			if scenario == "stats-entry-only" {
				kind = "stats"
				ids = []any{"catalog"}
			}
			w := plain(kind)
			if kind == "stats" {
				w = statWidget("requests")
			}
			f.items = append(f.items, map[string]any{"id": "shared", "kind": kind, "integrationIds": ids, "options": widgetOptions(w, "external")})
			before, _ := json.Marshal(f.items)
			if sharedWidget(kind) {
				if err := ensureSharedWidget(t.Context(), h, w, "catalog", map[string]bool{"shared": true}); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("corrupt co-bound ownership did not hold shared mutation")
				}
			}
			if err := removeWidgets(t.Context(), h, "catalog", []widget{w}); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("corrupt co-bound ownership allowed related removal")
			}
			after, _ := json.Marshal(f.items)
			if !reflect.DeepEqual(before, after) || h.Secret(h.Key("widgets_external")) != "null" {
				t.Fatal("related mutation rewrote or discarded unknown bindings")
			}
		})
	}
}
