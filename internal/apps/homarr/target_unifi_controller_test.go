package homarr

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

func unifiTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "unifiController", Endpoint: "https://private-unifi.example.invalid:8443",
		Credentials: []helpers.ExternalCredential{{Kind: "username", Value: "readonly"}, {Kind: "password", Value: "fixture-password"}},
		Selection:   map[string]string{"flavor": "classic", "siteId": "fixture-site", "siteName": "fixture-name"},
	}
}

func TestUniFiExternalInputRequiresTheExactPrivateContract(t *testing.T) {
	c := unifiTestConnection()
	d, err := externalConnectionDescriptor(c)
	if err != nil || d.kind != "unifiController" || d.id != "external-"+c.ID || !d.integrationOnly {
		t.Fatalf("descriptor: %v", err)
	}
	if len(d.credentials(nil, nil)) != 2 || d.internal(nil) != c.Endpoint || len(d.widgets) != 2 || d.widgets[0].kind != "networkControllerStatus" || d.widgets[0].options["content"] != "wifi" || strings.Join(d.widgets[1].metrics, ",") != "wifi,lan,latency" {
		t.Fatal("descriptor did not retain exact credentials, endpoint and read-only surfaces")
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-unifi.example.invalid:8443" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/proxy/network" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?x=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://user:private@private-unifi.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-unifi.example.invalid:70000" },
		func(c *helpers.ExternalConnection) { c.Credentials = c.Credentials[:1] },
		func(c *helpers.ExternalConnection) { c.Credentials = append(c.Credentials, c.Credentials[0]) },
		func(c *helpers.ExternalConnection) { c.Credentials[1].Value = "" },
		func(c *helpers.ExternalConnection) { c.Credentials[1].Kind = "apiKey" },
		func(c *helpers.ExternalConnection) { c.Selection["flavor"] = "auto" },
		func(c *helpers.ExternalConnection) { delete(c.Selection, "siteId") },
		func(c *helpers.ExternalConnection) { c.Selection["siteName"] = "all sites" },
		func(c *helpers.ExternalConnection) { c.Selection["unknown"] = "private" },
		func(c *helpers.ExternalConnection) { c.Kind = "unifi" },
	} {
		input := unifiTestConnection()
		input.Selection = map[string]string{"flavor": "classic", "siteId": "fixture-site", "siteName": "fixture-name"}
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid UniFi Controller connection was accepted")
		}
	}
}

func TestUniFiPreflightHoldsBeforeAnyRequest(t *testing.T) {
	d, err := externalConnectionDescriptor(unifiTestConnection())
	if err != nil {
		t.Fatal(err)
	}
	var manual *helpers.ManualError
	if err := d.preflight(t.Context(), nil, nil); !errors.As(err, &manual) || !strings.Contains(manual.Hint, "redirect") {
		t.Fatalf("preflight did not hold: %v", err)
	}
}

func unifiSummary() map[string]any {
	return map[string]any{
		"integrationId": "int-1", "error": false, "updatedAt": float64(1_000_000),
		"integration": map[string]any{"id": "int-1", "kind": "unifiController"},
		"summary": map[string]any{
			"wifi": map[string]any{"users": float64(0), "guests": float64(0)},
			"lan":  map[string]any{"users": float64(3), "guests": float64(1)},
			"www":  map[string]any{"latency": float64(12.5)},
			"vpn":  map[string]any{"users": nil},
		},
	}
}

func TestUniFiValidateNativeAcceptsZerosAndNullableVPNButRejectsUnsafeData(t *testing.T) {
	if err := unifiValidateNative([]any{unifiSummary()}, "int-1", 1_000_000, "s", "n"); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	for name, change := range map[string]func(map[string]any){
		"error":     func(e map[string]any) { e["error"] = true },
		"nullData":  func(e map[string]any) { e["summary"] = nil },
		"owner":     func(e map[string]any) { e["integrationId"] = "other" },
		"kind":      func(e map[string]any) { e["integration"].(map[string]any)["kind"] = "proxmox" },
		"stale":     func(e map[string]any) { e["updatedAt"] = float64(1) },
		"negative":  func(e map[string]any) { e["summary"].(map[string]any)["lan"].(map[string]any)["users"] = float64(-1) },
		"fraction":  func(e map[string]any) { e["summary"].(map[string]any)["wifi"].(map[string]any)["guests"] = 1.5 },
		"latency":   func(e map[string]any) { e["summary"].(map[string]any)["www"].(map[string]any)["latency"] = math.NaN() },
		"vpn":       func(e map[string]any) { e["summary"].(map[string]any)["vpn"].(map[string]any)["users"] = "2" },
		"otherSite": func(e map[string]any) { e["summary"].(map[string]any)["site"] = map[string]any{"id": "x", "name": "n"} },
	} {
		entry := unifiSummary()
		change(entry)
		if err := unifiValidateNative([]any{entry}, "int-1", 1_000_000, "s", "n"); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if err := unifiValidateNative([]any{unifiSummary(), unifiSummary()}, "int-1", 1_000_000, "s", "n"); err == nil {
		t.Fatal("multiple summaries were accepted")
	}
}

func TestUniFiValidateStatsRequiresMeasuredCountsLatencyAndAdvancingRefresh(t *testing.T) {
	good := statsSample{
		values: map[string]any{"wifi": float64(0), "lan": float64(4), "latency": float64(20.5)},
		units:  map[string]string{"wifi": "count", "lan": "count", "latency": "ms"}, retryAt: float64(0),
		startedAt: 1_000_000, completedAt: 1_001_000, updatedAt: 1_000_500,
	}
	if err := unifiValidateStats(good); err != nil {
		t.Fatalf("valid sample rejected: %v", err)
	}
	for name, change := range map[string]func(*statsSample){
		"missing":  func(s *statsSample) { delete(s.values, "latency") },
		"fraction": func(s *statsSample) { s.values["lan"] = 1.5 },
		"unit":     func(s *statsSample) { s.units["latency"] = "s" },
		"retry":    func(s *statsSample) { s.retryAt = float64(5) },
		"stale":    func(s *statsSample) { s.updatedAt = 1 },
		"inf":      func(s *statsSample) { s.values["latency"] = math.Inf(1) },
	} {
		sample := good
		sample.values = map[string]any{"wifi": float64(0), "lan": float64(4), "latency": float64(20.5)}
		sample.units = map[string]string{"wifi": "count", "lan": "count", "latency": "ms"}
		change(&sample)
		if err := unifiValidateStats(sample); err == nil {
			t.Fatalf("%s sample was accepted", name)
		}
	}
}
