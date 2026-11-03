package homarr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

func netalertxConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "n1", Kind: "netalertx", Endpoint: "http://192.0.2.10:20212",
		Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "fixture-token"}},
	}
}

func TestNetalertxExternalTargetValidation(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*helpers.ExternalConnection)
		wantOK bool
	}{
		{"http origin", func(*helpers.ExternalConnection) {}, true},
		{"https origin", func(c *helpers.ExternalConnection) { c.Endpoint = "https://alerts.example.invalid" }, true},
		{"wrong kind", func(c *helpers.ExternalConnection) { c.Kind = "netalertx2" }, false},
		{"embedded credentials", func(c *helpers.ExternalConnection) { c.Endpoint = "http://u:p@192.0.2.10:20212" }, false},
		{"path", func(c *helpers.ExternalConnection) { c.Endpoint = "http://192.0.2.10:20212/api" }, false},
		{"query", func(c *helpers.ExternalConnection) { c.Endpoint = "http://192.0.2.10:20212?token=x" }, false},
		{"bad port", func(c *helpers.ExternalConnection) { c.Endpoint = "http://192.0.2.10:70000" }, false},
		{"other scheme", func(c *helpers.ExternalConnection) { c.Endpoint = "ftp://192.0.2.10" }, false},
		{"no credential", func(c *helpers.ExternalConnection) { c.Credentials = nil }, false},
		{"empty credential", func(c *helpers.ExternalConnection) { c.Credentials[0].Value = "" }, false},
		{"wrong credential kind", func(c *helpers.ExternalConnection) { c.Credentials[0].Kind = "token" }, false},
		{"extra credential", func(c *helpers.ExternalConnection) {
			c.Credentials = append(c.Credentials, helpers.ExternalCredential{Kind: "username", Value: "x"})
		}, false},
		{"selection", func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"mac": "aa"} }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c := netalertxConnection()
			scenario.change(&c)
			descriptor, err := externalConnectionDescriptor(c)
			if (err == nil) != scenario.wantOK {
				t.Fatalf("descriptor: %v, want success %v", err, scenario.wantOK)
			}
			if scenario.wantOK && (!slices.Equal(descriptor.statsMetrics(), netalertxStatsKeys) || !descriptor.integrationOnly) {
				t.Fatal("descriptor does not bind the four Stats keys integration-only")
			}
		})
	}
}

func TestNetalertxTotalsAndStatsVerification(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		totals any
		change func(*statsFixture)
		status float64
		wantOK bool
	}{
		{name: "seeded totals", totals: []any{4, 2, 0, 1, 1, 1}, wantOK: true},
		{name: "explicit zeros", totals: []any{0, 0, 0, 0, 0, 0}, change: func(f *statsFixture) {
			f.values = map[string]any{"total": 0, "connected": 0, "newDevices": 0, "downAlerts": 0}
		}, wantOK: true},
		{name: "connected exceeds total", totals: []any{1, 2, 0, 1, 1, 1}, wantOK: true},
		{name: "rejected token", totals: []any{4, 2, 0, 1, 1, 1}, status: 401},
		{name: "not an array", totals: map[string]any{"total": 4}},
		{name: "short array", totals: []any{4, 2, 0}},
		{name: "null total", totals: []any{nil, 2, 0, 1, 1, 1}},
		{name: "negative down alerts", totals: []any{4, 2, 0, 1, -1, 1}},
		{name: "fractional connected", totals: []any{4, 0.5, 0, 1, 1, 1}},
		{name: "text new devices", totals: []any{4, 2, 0, "1", 1, 1}},
		{name: "missing stat", totals: []any{4, 2, 0, 1, 1, 1}, change: func(f *statsFixture) { delete(f.values, "downAlerts") }},
		{name: "null stat", totals: []any{4, 2, 0, 1, 1, 1}, change: func(f *statsFixture) { f.values["total"] = nil }},
		{name: "error stat", totals: []any{4, 2, 0, 1, 1, 1}, change: func(f *statsFixture) { f.values["newDevices"] = -1 }},
		{name: "wrong unit", totals: []any{4, 2, 0, 1, 1, 1}, change: func(f *statsFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{name: "no-op refresh", totals: []any{4, 2, 0, 1, 1, 1}, change: func(f *statsFixture) { f.advance = false }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newStatsFixture()
			fixture.values = map[string]any{"total": 4, "connected": 2, "newDevices": 1, "downAlerts": 1}
			fixture.metrics = []any{}
			for _, key := range netalertxStatsKeys {
				fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
			}
			if scenario.change != nil {
				scenario.change(fixture)
			}
			from := fixture.helpers(t)
			statsURL, err := url.Parse(from.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(statsURL)
			requests := 0
			homarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/trpc/integration.request" {
					proxy.ServeHTTP(w, r)
					return
				}
				var input struct {
					JSON struct{ IntegrationID, Method, Path string } `json:"json"`
				}
				if r.Method != "POST" || r.Header.Get("ApiKey") != "fixture-key" || json.NewDecoder(r.Body).Decode(&input) != nil || input.JSON.IntegrationID != "netalertx-owned" || input.JSON.Method != "GET" || input.JSON.Path != "/devices/totals" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests++
				status := scenario.status
				if status == 0 {
					status = 200
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
					"ok": status == 200, "status": status, "integration": map[string]any{"id": "netalertx-owned", "kind": "netalertx"},
					"data": map[string]any{"response": scenario.totals},
				}}}})
			}))
			t.Cleanup(homarr.Close)
			from.BaseURL, from.Config.HTTP = homarr.URL, homarr.Client()
			descriptor, err := netalertxExternalTarget(netalertxConnection())
			if err != nil {
				t.Fatal(err)
			}
			err = descriptor.verifyData(t.Context(), from, nil, "netalertx-owned")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("verification: %v, want success %v", err, scenario.wantOK)
			}
			if requests != 1 {
				t.Fatalf("expected exactly one totals request, got %d", requests)
			}
		})
	}
}
