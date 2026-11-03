package homarr

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func proxmoxTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "proxmox", Endpoint: "https://private-proxmox.example.invalid:8006",
		Credentials: []helpers.ExternalCredential{
			{Kind: "username", Value: "homarr"}, {Kind: "realm", Value: "pve"}, {Kind: "tokenId", Value: "monitoring"},
			{Kind: "apiKey", Value: "12345678-1234-1234-1234-1234567890ab"},
		},
		Selection: map[string]string{"scope": "cluster", "node": "fixture-node"},
	}
}

func TestProxmoxExternalInputRequiresTheExactPrivateContract(t *testing.T) {
	c := proxmoxTestConnection()
	target, err := externalConnectionDescriptor(c)
	if err != nil || target.kind != "proxmox" || target.id != "external-"+c.ID || !target.integrationOnly {
		t.Fatalf("descriptor: %v", err)
	}
	credentials := target.credentials(nil, nil)
	if len(credentials) != 4 || target.internal(nil) != c.Endpoint || len(target.widgets) != 2 || target.widgets[0].kind != "healthMonitoring" || target.widgets[1].metrics[0] != "nodes" {
		t.Fatal("descriptor did not retain exact credentials, endpoint and supported surfaces")
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-proxmox.example.invalid:8006" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/api2/json" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?token=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "#" },
		func(c *helpers.ExternalConnection) {
			c.Endpoint = "https://user:private@private-proxmox.example.invalid:8006"
		},
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-proxmox.example.invalid:70000" },
		func(c *helpers.ExternalConnection) { c.Credentials = c.Credentials[:3] },
		func(c *helpers.ExternalConnection) { c.Credentials = append(c.Credentials, c.Credentials[0]) },
		func(c *helpers.ExternalConnection) { c.Credentials[1].Value = "pam" },
		func(c *helpers.ExternalConnection) { c.Credentials[2].Value = "other@pve!token" },
		func(c *helpers.ExternalConnection) { c.Credentials[3].Kind = "personalAccessToken" },
		func(c *helpers.ExternalConnection) { c.Credentials[3].Value = "not-a-lowercase-uuid" },
		func(c *helpers.ExternalConnection) { c.Selection["scope"] = "single-node" },
		func(c *helpers.ExternalConnection) { delete(c.Selection, "node") },
		func(c *helpers.ExternalConnection) { c.Selection["unknown"] = "private" },
	} {
		input := proxmoxTestConnection()
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid Proxmox connection was accepted")
		}
	}
}

func TestProxmoxNativeTransportHoldPrecedesEveryRequestAndPersistsSpecificRecovery(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	c := proxmoxTestConnection()
	h.ExternalConnections = func() ([]helpers.ExternalConnection, error) { return []helpers.ExternalConnection{c}, nil }
	var recorded helpers.StepResult
	h.Record = func(r helpers.StepResult) { recorded = r }
	removed, err := (App{}).SetupExternalConnections(t.Context(), h)
	if !errors.Is(err, helpers.ErrManual) || len(removed) != 0 || recorded.Status != stepstatus.StepManual || recorded.Hint != proxmoxTransportHold {
		t.Fatal("native safety hold was not a specific recorded Manual outcome")
	}
	if f.requests != 0 || h.Secret(h.Key("linked_external-"+c.ID)) != "" || f.created != 0 {
		t.Fatal("held Proxmox forwarded credentials, made native requests or created resources")
	}
	c.Removing = true
	c.Endpoint = "invalid and unavailable"
	c.Credentials = nil
	c.Selection = nil
	removed, err = (App{}).SetupExternalConnections(t.Context(), h)
	if err != nil || len(removed) != 1 || removed[0] != c.ID || f.requests != 0 {
		t.Fatal("local disconnect depended on Proxmox inputs or reachability")
	}
}

func proxmoxTestNative() map[string]any {
	return map[string]any{
		"nodes": []any{map[string]any{
			"id": "node/fixture-node", "node": "fixture-node", "name": "fixture-node", "type": "node", "status": "online", "isRunning": true,
			"cpu":    map[string]any{"utilization": float64(0), "cores": float64(4)},
			"memory": map[string]any{"used": float64(0), "total": float64(16384)}, "uptime": float64(0),
		}},
		"vms": []any{}, "lxcs": []any{}, "storages": []any{},
	}
}

func TestProxmoxNativeSemanticsRequireTheSelectedNodeAndExplicitMetrics(t *testing.T) {
	if err := proxmoxValidateNative(proxmoxTestNative(), "fixture-node"); err != nil {
		t.Fatal("explicit legitimate metric zeros were rejected")
	}
	for _, change := range []func(map[string]any, map[string]any){
		func(cluster, _ map[string]any) { cluster["error"] = "failed" },
		func(cluster, _ map[string]any) { cluster["stale"] = true },
		func(cluster, _ map[string]any) { cluster["nodes"] = nil },
		func(cluster, _ map[string]any) { delete(cluster, "storages") },
		func(_, node map[string]any) { node["id"] = "node/not-the-selected-node" },
		func(_, node map[string]any) { node["status"] = "offline"; node["isRunning"] = false },
		func(_, node map[string]any) { node["cpu"].(map[string]any)["utilization"] = nil },
		func(_, node map[string]any) { node["cpu"].(map[string]any)["utilization"] = float64(101) },
		func(_, node map[string]any) { node["cpu"].(map[string]any)["cores"] = float64(0) },
		func(_, node map[string]any) { node["memory"].(map[string]any)["total"] = float64(0) },
		func(_, node map[string]any) { node["memory"].(map[string]any)["used"] = float64(20000) },
		func(_, node map[string]any) { node["uptime"] = math.Inf(1) },
	} {
		cluster := proxmoxTestNative()
		node := cluster["nodes"].([]any)[0].(map[string]any)
		change(cluster, node)
		if err := proxmoxValidateNative(cluster, "fixture-node"); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("missing, wrong-identity or invalid native metrics were accepted")
		}
	}
	if err := proxmoxValidateNative(proxmoxTestNative(), "missing-node"); err == nil {
		t.Fatal("a fabricated node selection was accepted")
	}
}

func TestProxmoxStatsRetainCountUnitsClockAndRetrySemantics(t *testing.T) {
	valid := func() statsSample {
		return statsSample{values: map[string]any{"nodes": float64(1)}, units: map[string]string{"nodes": "count"}, retryAt: float64(0), startedAt: 10000, completedAt: 10010, updatedAt: 10005}
	}
	if err := proxmoxValidateStats(valid()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*statsSample){
		func(s *statsSample) { s.values["nodes"] = float64(0) },
		func(s *statsSample) { s.values["nodes"] = float64(1.5) },
		func(s *statsSample) { s.values["nodes"] = nil },
		func(s *statsSample) { s.values["nodes"] = math.NaN() },
		func(s *statsSample) { s.values["nodes"] = float64(1 << 54) },
		func(s *statsSample) { s.units["nodes"] = "bytes" },
		func(s *statsSample) { s.retryAt = nil },
		func(s *statsSample) { s.retryAt = float64(12000) },
		func(s *statsSample) { s.updatedAt = 20000 },
		func(s *statsSample) { s.updatedAt = math.NaN() },
		func(s *statsSample) { s.startedAt = 100000 },
	} {
		sample := valid()
		change(&sample)
		if err := proxmoxValidateStats(sample); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("invalid Proxmox Stats semantics were accepted")
		}
	}
	encoded, _ := json.Marshal(proxmoxTestConnection())
	if strings.Contains(string(encoded), "private-proxmox") {
		t.Fatal("Proxmox private input escaped the connection DTO")
	}
}
