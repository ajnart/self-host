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
	case "/api/trpc/integration.create":
		if _, found := input.JSON["app"]; found {
			f.t.Error("external connection fabricated a linked Homarr App")
		}
		f.created++
		record := map[string]any{"id": "external-owned-" + strconv.Itoa(f.created), "kind": input.JSON["kind"], "url": input.JSON["url"]}
		f.integrations = append(f.integrations, record)
		externalConnectionReply(w, map[string]any{"integration": record})
	case "/api/trpc/integration.update":
		if input.JSON["appId"] != nil {
			f.t.Error("external update fabricated a Homarr App association")
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
