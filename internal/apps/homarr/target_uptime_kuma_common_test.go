package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func uptimeKumaMissingProcedure(proc string) map[string]any {
	return map[string]any{"error": map[string]any{"json": map[string]any{
		"code": -32004, "message": `No procedure found on path "` + proc + `"`,
		"data": map[string]any{"code": "NOT_FOUND", "httpStatus": 404, "path": proc},
	}}}
}

func uptimeKumaReply(w http.ResponseWriter, value any) {
	json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
}

func TestUptimeKumaCapabilityOnlyFallsBackOnExactNumericAbsence(t *testing.T) {
	proc := uptimeKumaStatusPages.procedure()
	for _, scenario := range []struct {
		name   string
		status int
		body   any
		raw    string
		family uptimeKumaNativeFamily
		calls  int
	}{
		{"modern", 200, nil, `{"result":{"data":{"json":[]}}}`, uptimeKumaStatusPages, 1},
		{"legacy", 404, uptimeKumaMissingProcedure(proc), "", uptimeKumaLegacyDashboard, 2},
		{"unauthorized", 401, uptimeKumaMissingProcedure(proc), "", 0, 1},
		{"forbidden", 403, uptimeKumaMissingProcedure(proc), "", 0, 1},
		{"server failure", 500, uptimeKumaMissingProcedure(proc), "", 0, 1},
		{"rate limit", 429, uptimeKumaMissingProcedure(proc), "", 0, 1},
		{"redirect", 302, nil, "private upstream details", 0, 1},
		{"unknown not found", 404, nil, `{"error":{"json":{"code":-32004,"message":"private upstream details"}}}`, 0, 1},
		{"different procedure", 404, uptimeKumaMissingProcedure("different.procedure"), "", 0, 1},
		{"string code", 404, nil, `{"error":{"json":{"code":"NOT_FOUND"}}}`, 0, 1},
		{"malformed", 404, nil, "private upstream details", 0, 1},
		{"null result", 200, nil, `{"result":{"data":{"json":null}}}`, 0, 1},
		{"nonempty probe", 200, nil, `{"result":{"data":{"json":[{}]}}}`, 0, 1},
		{"wrong result shape", 200, nil, `{"result":{"data":{"json":{}}}}`, 0, 1},
		{"both result and error", 404, nil, `{"result":{"data":{"json":[]}},"error":{"json":{"code":-32004}}}`, 0, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("ApiKey") != "fixture-key" {
					t.Error("capability probe mutated or lost authentication")
				}
				if r.URL.Path == "/api/trpc/info.getInfo" {
					uptimeKumaReply(w, map[string]any{"version": "2.10.1"})
					return
				}
				calls++
				var input struct {
					JSON map[string]any `json:"json"`
				}
				if json.Unmarshal([]byte(r.URL.Query().Get("input")), &input) != nil {
					t.Fatal("missing SuperJSON input")
				}
				ids, ok := input.JSON["integrationIds"].([]any)
				if !ok || len(ids) != 0 {
					t.Error("probe selected a provider integration")
				}
				if calls == 2 {
					if r.URL.Path != "/api/trpc/"+uptimeKumaLegacyDashboard.procedure() {
						t.Error("unexpected fallback")
					}
					uptimeKumaReply(w, []any{})
					return
				}
				if input.JSON["statusPageSlug"] != "chosen-page" {
					t.Error("modern probe lost selected slug")
				}
				w.WriteHeader(scenario.status)
				if scenario.body != nil {
					json.NewEncoder(w).Encode(scenario.body)
					return
				}
				w.Write([]byte(scenario.raw))
			}))
			t.Cleanup(server.Close)
			h := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			family, err := uptimeKumaCapability(t.Context(), h, "chosen-page")
			if family != scenario.family || calls != scenario.calls {
				t.Fatalf("family=%v calls=%d err=%v", family, calls, err)
			}
			if scenario.family == 0 && (!errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private upstream details")) {
				t.Fatalf("incompatibility must be sanitized Manual: %v", err)
			}
			if scenario.family != 0 && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUptimeKumaCapabilityAuthenticatesBeforePublicProbe(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/trpc/info.getInfo" {
			t.Error("public native probe ran after authentication failed")
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("private upstream details"))
	}))
	t.Cleanup(server.Close)
	h := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{}})
	_, err := uptimeKumaCapability(t.Context(), h, "chosen-page")
	if !errors.Is(err, helpers.ErrManual) || calls != 1 || strings.Contains(err.Error(), "private upstream details") {
		t.Fatalf("authentication failure=%v calls=%d", err, calls)
	}
}

func TestUptimeKumaNullableStatsOptInKeepsOneSampleAndMissingStrictness(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		nullable bool
		missing  bool
		advance  bool
		valid    bool
	}{
		{"strict null", false, false, true, false},
		{"opted in null", true, false, true, true},
		{"opted in missing", true, true, true, false},
		{"opted in no-op", true, false, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values["requests"] = nil
			fixture.advance = scenario.advance
			if scenario.missing {
				delete(fixture.values, "requests")
			}
			validated := false
			descriptor := statsTarget(func(sample statsSample) error {
				validated = true
				value, present := sample.values["requests"]
				if !present || value != nil || sample.units["requests"] != "count" || sample.updatedAt != fixture.timestamp {
					return errors.New("nullable sample changed")
				}
				return nil
			})
			if scenario.nullable {
				descriptor.nullableStats = []string{"requests"}
			}
			h := fixture.helpers(t)
			err := descriptor.verifyData(t.Context(), h, h, "owned")
			if (err == nil) != scenario.valid || validated != scenario.valid || fixture.catalogs != 1 || fixture.snapshots != 1 || fixture.refreshes != 1 || !fixture.force {
				t.Fatalf("err=%v validated=%v catalogs=%d snapshots=%d refreshes=%d", err, validated, fixture.catalogs, fixture.snapshots, fixture.refreshes)
			}
		})
	}
}

func TestUptimeKumaOwnedOptionsPreserveIDsUserOptionsAndCrossKindBindings(t *testing.T) {
	var saved map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/trpc/board.saveBoard" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var input struct {
			JSON map[string]any `json:"json"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		saved = input.JSON
		uptimeKumaReply(w, nil)
	}))
	t.Cleanup(server.Close)
	h := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_WIDGETS_OWNED": `{"owned-widget":true}`, "HOMARR_API_KEY": "fixture-key"}})
	descriptor := target{name: "Uptime Kuma", widgets: []widget{{kind: "uptimeKuma", options: map[string]any{"showSummary": true}, managedOptions: []string{"showSummary", "showUpCount"}}}}
	owned := map[string]any{"id": "owned-widget", "kind": "uptimeKuma", "integrationIds": []any{"owned"}, "options": map[string]any{"showUpCount": true, "custom": "keep"}}
	user := map[string]any{"id": "user-widget", "kind": "uptimeKuma", "integrationIds": []any{"owned"}, "options": map[string]any{"custom": "user"}}
	survivor := map[string]any{"id": "shared-stats", "kind": "stats", "integrationIds": []any{"owned", "other-kind"}, "options": map[string]any{"entries": []any{map[string]any{"integrationId": "other-kind", "metric": "requests"}}}}
	current := map[string]any{"id": "home", "sections": []any{}, "items": []any{owned, user, survivor}}
	if ready, err := descriptor.reconcileOwnedOptions(t.Context(), h, current, "owned", false); err != nil || ready || saved != nil {
		t.Fatalf("read-only reconciliation ready=%v err=%v", ready, err)
	}
	if _, err := descriptor.reconcileOwnedOptions(t.Context(), h, current, "owned", true); err != nil {
		t.Fatal(err)
	}
	items := itemsOf(saved)
	if len(items) != 3 || items[0]["id"] != "owned-widget" || !reflect.DeepEqual(items[0]["options"], map[string]any{"showSummary": true, "custom": "keep"}) || !reflect.DeepEqual(items[1], user) || !reflect.DeepEqual(items[2], survivor) {
		t.Fatalf("reconciliation changed identities, user options or survivor: %v", saved)
	}
	if !reflect.DeepEqual(owned["options"], map[string]any{"showUpCount": true, "custom": "keep"}) {
		t.Fatal("readback was mutated in memory before saving")
	}
	owned["integrationIds"] = []any{"owned", "other-kind"}
	saved = nil
	if _, err := descriptor.reconcileOwnedOptions(t.Context(), h, current, "owned", true); !errors.Is(err, helpers.ErrManual) || saved != nil {
		t.Fatalf("cross-source native options must remain Manual without writes: %v", err)
	}
}

func TestUptimeKumaPreparationFailurePrecedesAllLinkWrites(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	h := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard()})
	descriptor := target{prepare: func(context.Context, *helpers.H, *helpers.H) (target, error) {
		return target{}, uptimeKumaCapabilityManual()
	}}
	if _, err := descriptor.linked(t.Context(), h, h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal(err)
	}
	if err := descriptor.add(t.Context(), h, h); !errors.Is(err, helpers.ErrManual) || requests != 0 {
		t.Fatalf("preparation failed after requests: %v requests=%d", err, requests)
	}
}
