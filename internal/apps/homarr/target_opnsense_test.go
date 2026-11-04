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

func opnsenseConnection(endpoint string) helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "opn1", Kind: "opnsense", Endpoint: endpoint,
		Credentials: []helpers.ExternalCredential{
			{Kind: "opnsenseApiKey", Value: "private-key-169"},
			{Kind: "opnsenseApiSecret", Value: "private-secret-169"},
		},
	}
}

func TestOpnsenseTargetAcceptsOnlyTheExactPairAndOrigin(t *testing.T) {
	good := opnsenseConnection("https://fw.example.invalid:8443")
	target, err := opnsenseExternalTarget(good)
	if err != nil || target.kind != "opnsense" || len(target.widgets) != 1 || target.widgets[0].kind != "stats" || target.widgets[0].metrics[0] != "cpu" {
		t.Fatalf("exact target: %v", err)
	}
	for name, change := range map[string]func(*helpers.ExternalConnection){
		"http":      func(c *helpers.ExternalConnection) { c.Endpoint = "http://fw.example.invalid" },
		"userinfo":  func(c *helpers.ExternalConnection) { c.Endpoint = "https://u:p@fw.example.invalid" },
		"path":      func(c *helpers.ExternalConnection) { c.Endpoint = "https://fw.example.invalid/ui" },
		"query":     func(c *helpers.ExternalConnection) { c.Endpoint = "https://fw.example.invalid?x=1" },
		"port":      func(c *helpers.ExternalConnection) { c.Endpoint = "https://fw.example.invalid:70000" },
		"kind":      func(c *helpers.ExternalConnection) { c.Kind = "proxmox" },
		"missing":   func(c *helpers.ExternalConnection) { c.Credentials = c.Credentials[:1] },
		"empty":     func(c *helpers.ExternalConnection) { c.Credentials[1].Value = "" },
		"duplicate": func(c *helpers.ExternalConnection) { c.Credentials[1].Kind = "opnsenseApiKey" },
		"extra": func(c *helpers.ExternalConnection) {
			c.Credentials = append(c.Credentials, helpers.ExternalCredential{Kind: "apiKey", Value: "x"})
		},
		"selection": func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"scope": "all"} },
	} {
		c := opnsenseConnection("https://fw.example.invalid")
		c.Credentials = append([]helpers.ExternalCredential(nil), c.Credentials...)
		change(&c)
		if _, err := opnsenseExternalTarget(c); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestOpnsensePreflightReadsOnlyIdentityWithoutLeakingIt(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		body   string
		hint   string
	}{
		{"valid", 200, `{"name":"private-host-169","versions":["OPNsense 26.7.8"]}`, ""},
		{"rejected", 401, `{}`, opnsenseRejectedRecovery},
		{"forbidden", 403, `{}`, opnsenseForbiddenRecovery},
		{"redirect", 302, ``, opnsenseIdentityRecovery},
		{"html", 200, `<html>private-host-169</html>`, opnsenseIdentityRecovery},
		{"empty", 200, `{}`, opnsenseIdentityRecovery},
		{"no name", 200, `{"versions":["26.7"]}`, opnsenseIdentityRecovery},
		{"no versions", 200, `{"name":"x","versions":[]}`, opnsenseIdentityRecovery},
		{"blank version", 200, `{"name":"x","versions":[""]}`, opnsenseIdentityRecovery},
		{"server error", 500, `private-host-169`, opnsenseIdentityRecovery},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var requests []string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				user, password, _ := r.BasicAuth()
				if user != "private-key-169" || password != "private-secret-169" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if scenario.status == 302 {
					http.Redirect(w, r, "/elsewhere", http.StatusFound)
					return
				}
				w.WriteHeader(scenario.status)
				w.Write([]byte(scenario.body))
			}))
			t.Cleanup(srv.Close)
			target, err := opnsenseExternalTarget(opnsenseConnection(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: "http://127.0.0.1:1", HTTP: srv.Client(), Log: events.Discard()})
			err = target.preflight(t.Context(), from, nil)
			if scenario.hint == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var manual *helpers.ManualError
				if !errors.As(err, &manual) || manual.Hint != scenario.hint {
					t.Fatalf("got %v", err)
				}
				for _, private := range []string{"private-key-169", "private-secret-169", "private-host-169"} {
					if strings.Contains(err.Error(), private) {
						t.Fatal("private value reached the error")
					}
				}
			}
			if len(requests) != 1 || requests[0] != "GET /api/diagnostics/system/system_information" {
				t.Fatalf("unexpected remote requests: %v", requests)
			}
		})
	}
}

func opnsenseNative(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestOpnsenseNativeCPURequiresOneOwnedMeasuredResult(t *testing.T) {
	for name, scenario := range map[string]struct {
		raw   string
		valid bool
	}{
		"zero":          {`[{"integration":{"id":"own","kind":"opnsense","updatedAt":"2026-01-01"},"summary":{"total":0},"error":null}]`, true},
		"full":          {`[{"integration":{"id":"own","kind":"opnsense","updatedAt":"2026-01-01"},"summary":{"total":100},"error":false}]`, true},
		"fraction":      {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":12.5}}]`, true},
		"unowned":       {`[{"integration":{"id":"user","kind":"opnsense"},"summary":{"total":1}}]`, false},
		"wrong kind":    {`[{"integration":{"id":"own","kind":"proxmox"},"summary":{"total":1}}]`, false},
		"error":         {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":0},"error":"boom"}]`, false},
		"null updated":  {`[{"integration":{"id":"own","kind":"opnsense","updatedAt":null},"summary":{"total":1}}]`, false},
		"missing total": {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{}}]`, false},
		"null total":    {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":null}}]`, false},
		"over range":    {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":101}}]`, false},
		"negative":      {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":-1}}]`, false},
		"two results":   {`[{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":1}},{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":1}}]`, false},
		"not a list":    {`{"integration":{"id":"own","kind":"opnsense"},"summary":{"total":1}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := opnsenseValidateNative(opnsenseNative(t, scenario.raw), "own")
			if (err == nil) != scenario.valid {
				t.Fatalf("valid=%v, got %v", scenario.valid, err)
			}
		})
	}
}

func TestOpnsenseStatsAcceptLegitimateZeroAndRejectUnmeasuredOrStale(t *testing.T) {
	sample := func(change func(*statsSample)) statsSample {
		s := statsSample{
			values: map[string]any{"cpu": float64(0)}, units: map[string]string{"cpu": "percent"},
			retryAt: float64(0), startedAt: 1000, completedAt: 1100, updatedAt: 1050,
		}
		change(&s)
		return s
	}
	for name, scenario := range map[string]struct {
		change func(*statsSample)
		valid  bool
	}{
		"zero":         {func(*statsSample) {}, true},
		"full":         {func(s *statsSample) { s.values["cpu"] = float64(100) }, true},
		"fraction":     {func(s *statsSample) { s.values["cpu"] = 33.3 }, true},
		"missing":      {func(s *statsSample) { delete(s.values, "cpu") }, false},
		"null":         {func(s *statsSample) { s.values["cpu"] = nil }, false},
		"over":         {func(s *statsSample) { s.values["cpu"] = float64(100.5) }, false},
		"negative":     {func(s *statsSample) { s.values["cpu"] = float64(-1) }, false},
		"wrong unit":   {func(s *statsSample) { s.units["cpu"] = "count" }, false},
		"retry set":    {func(s *statsSample) { s.retryAt = float64(5) }, false},
		"retry null":   {func(s *statsSample) { s.retryAt = nil }, false},
		"epoch":        {func(s *statsSample) { s.updatedAt = 0 }, false},
		"stale":        {func(s *statsSample) { s.updatedAt = 999 }, false},
		"future":       {func(s *statsSample) { s.updatedAt = 1100 + 30_001 }, false},
		"future bound": {func(s *statsSample) { s.updatedAt = 1100 + 30_000 }, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := opnsenseValidateStats(sample(scenario.change))
			if (err == nil) != scenario.valid {
				t.Fatalf("valid=%v, got %v", scenario.valid, err)
			}
		})
	}
}
