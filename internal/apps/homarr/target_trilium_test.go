package homarr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func triliumStatsSample() statsSample {
	return statsSample{
		values: map[string]any{"version": "0.106.1", "notesCount": float64(0), "dbSize": float64(0)},
		units:  map[string]string{"version": "text", "notesCount": "count", "dbSize": "bytes"},
	}
}

func TestTriliumStatsSampleValidation(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*statsSample)
		wantOK bool
	}{
		{"populated", func(s *statsSample) { s.values["notesCount"], s.values["dbSize"] = float64(12), float64(4096) }, true},
		{"explicit zeros", func(*statsSample) {}, true},
		{"blank version", func(s *statsSample) { s.values["version"] = " " }, false},
		{"numeric version", func(s *statsSample) { s.values["version"] = float64(1) }, false},
		{"wrong version unit", func(s *statsSample) { s.units["version"] = "count" }, false},
		{"null notes", func(s *statsSample) { s.values["notesCount"] = nil }, false},
		{"fractional notes", func(s *statsSample) { s.values["notesCount"] = 0.5 }, false},
		{"negative notes", func(s *statsSample) { s.values["notesCount"] = float64(-1) }, false},
		{"text size", func(s *statsSample) { s.values["dbSize"] = "1" }, false},
		{"null size", func(s *statsSample) { s.values["dbSize"] = nil }, false},
		{"wrong size unit", func(s *statsSample) { s.units["dbSize"] = "count" }, false},
		{"missing size unit", func(s *statsSample) { delete(s.units, "dbSize") }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sample := triliumStatsSample()
			scenario.change(&sample)
			if err := verifyTriliumStats(sample); (err == nil) != scenario.wantOK {
				t.Fatalf("stats validation: %v, want success %v", err, scenario.wantOK)
			}
		})
	}
}

func TestTriliumTargetDescriptor(t *testing.T) {
	target := triliumTarget()
	if target.kind != "trilium" || len(target.widgets) != 1 || target.widgets[0].kind != "stats" || strings.Join(target.widgets[0].metrics, ",") != "version,notesCount,dbSize" {
		t.Fatalf("unexpected descriptor: %+v", target)
	}
}

func TestTriliumPreflightStaysManualWithoutMutation(t *testing.T) {
	log := events.Discard()
	from := helpers.New(helpers.Config{App: "homarr", Log: log})
	err := triliumTarget().preflight(t.Context(), from, helpers.New(helpers.Config{App: "trilium", Log: events.Discard()}))
	if !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("preflight must stay Manual: %v", err)
	}
}

func TestTriliumNativeIdentityVerification(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		wantOK bool
	}{
		{"app info", func(map[string]any, map[string]any) {}, true},
		{"wrong integration", func(r, _ map[string]any) { r["integration"] = map[string]any{"id": "other", "kind": "trilium"} }, false},
		{"wrong kind", func(r, _ map[string]any) {
			r["integration"] = map[string]any{"id": "trilium-owned", "kind": "linkwarden"}
		}, false},
		{"upstream error", func(r, _ map[string]any) { r["ok"], r["status"] = false, float64(401) }, false},
		{"old version", func(_, i map[string]any) { i["appVersion"] = "0.90.0" }, false},
		{"missing revision", func(_, i map[string]any) { delete(i, "buildRevision") }, false},
		{"fractional db version", func(_, i map[string]any) { i["dbVersion"] = 1.5 }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			info := map[string]any{"appVersion": "0.106.1", "dbVersion": float64(240), "syncVersion": float64(36), "buildRevision": "2be0c6ed"}
			result := map[string]any{"ok": true, "status": float64(200), "integration": map[string]any{"id": "trilium-owned", "kind": "trilium"}, "data": map[string]any{"response": info}}
			scenario.change(result, info)
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.URL.Path != "/api/trpc/integration.request" || r.Method != "POST" || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil ||
					input.JSON.IntegrationID != "trilium-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/etapi/app-info" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests++
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": result}}})
			}))
			t.Cleanup(srv.Close)
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			err := verifyTrilium(t.Context(), from, nil, "trilium-owned")
			if (err == nil) != scenario.wantOK || requests != 1 {
				t.Fatalf("verification: %v, want success %v, requests=%d", err, scenario.wantOK, requests)
			}
		})
	}
}
