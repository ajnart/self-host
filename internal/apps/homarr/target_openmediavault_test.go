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

func openmediavaultTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "openmediavault", Endpoint: "https://private-omv.example.invalid",
		Credentials: []helpers.ExternalCredential{{Kind: "username", Value: "private-admin"}, {Kind: "password", Value: "private-password"}},
	}
}

func TestOpenmediavaultExternalInputRequiresTheExactPrivateContract(t *testing.T) {
	c := openmediavaultTestConnection()
	target, err := externalConnectionDescriptor(c)
	if err != nil || target.kind != "openmediavault" || target.id != "external-"+c.ID || !target.integrationOnly {
		t.Fatalf("descriptor: %v", err)
	}
	if len(target.credentials(nil, nil)) != 2 || target.internal(nil) != c.Endpoint || len(target.widgets) != 2 || target.widgets[0].kind != "systemResources" || len(target.widgets[1].metrics) != 4 {
		t.Fatal("descriptor did not retain exact credentials, endpoint and supported surfaces")
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-omv.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/rpc.php" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?token=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://user:private@private-omv.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-omv.example.invalid:70000" },
		func(c *helpers.ExternalConnection) { c.Credentials = c.Credentials[:1] },
		func(c *helpers.ExternalConnection) { c.Credentials = append(c.Credentials, c.Credentials[0]) },
		func(c *helpers.ExternalConnection) { c.Credentials[1].Value = "" },
		func(c *helpers.ExternalConnection) { c.Credentials[1].Kind = "apiKey" },
		func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"node": "x"} },
	} {
		input := openmediavaultTestConnection()
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid OpenMediaVault connection was accepted")
		}
	}
	encoded, _ := json.Marshal(c)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("OpenMediaVault private input escaped the connection DTO")
	}
}

func TestOpenmediavaultTransportHoldPrecedesEveryRequestAndPersistsSpecificRecovery(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	c := openmediavaultTestConnection()
	h.ExternalConnections = func() ([]helpers.ExternalConnection, error) { return []helpers.ExternalConnection{c}, nil }
	var recorded helpers.StepResult
	h.Record = func(r helpers.StepResult) { recorded = r }
	removed, err := (App{}).SetupExternalConnections(t.Context(), h)
	if !errors.Is(err, helpers.ErrManual) || len(removed) != 0 || recorded.Status != stepstatus.StepManual || recorded.Hint != openmediavaultTransportHold {
		t.Fatal("native safety hold was not a specific recorded Manual outcome")
	}
	if f.requests != 0 || f.created != 0 || h.Secret(h.Key("linked_external-"+c.ID)) != "" {
		t.Fatal("held OpenMediaVault forwarded credentials, made native requests or created resources")
	}
	c.Removing, c.Endpoint, c.Credentials = true, "invalid and unavailable", nil
	removed, err = (App{}).SetupExternalConnections(t.Context(), h)
	if err != nil || len(removed) != 1 || removed[0] != c.ID || f.requests != 0 {
		t.Fatal("local disconnect depended on OpenMediaVault inputs or reachability")
	}
}

func openmediavaultTestNative() []any {
	return []any{map[string]any{"integrationId": "owned-omv", "healthInfo": map[string]any{
		"cpuUtilization": float64(0), "memUsedInBytes": float64(0), "memAvailableInBytes": float64(8192), "uptime": 12.5, "loadAverage": map[string]any{"1min": float64(250)},
	}}}
}

func TestOpenmediavaultNativeSemanticsRejectMissingNullErrorAndInvalidMetrics(t *testing.T) {
	if err := openmediavaultValidateNative(openmediavaultTestNative(), "owned-omv"); err != nil {
		t.Fatal("explicit legitimate zeros were rejected")
	}
	for _, change := range []func(map[string]any, map[string]any){
		func(r, _ map[string]any) { r["integrationId"] = "other" },
		func(r, _ map[string]any) { r["error"] = "failed" },
		func(r, _ map[string]any) { r["stale"] = true },
		func(r, _ map[string]any) { r["healthInfo"] = nil },
		func(_, h map[string]any) { delete(h, "uptime") },
		func(_, h map[string]any) { h["cpuUtilization"] = float64(101) },
		func(_, h map[string]any) { h["cpuUtilization"] = nil },
		func(_, h map[string]any) { h["memUsedInBytes"] = 1.5 },
		func(_, h map[string]any) { h["memAvailableInBytes"] = float64(-1) },
		func(_, h map[string]any) { h["uptime"] = math.Inf(1) },
	} {
		native := openmediavaultTestNative()
		result := native[0].(map[string]any)
		change(result, result["healthInfo"].(map[string]any))
		if err := openmediavaultValidateNative(native, "owned-omv"); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("missing, null, errored or invalid native metrics were accepted")
		}
	}
	if err := openmediavaultValidateNative(append(openmediavaultTestNative(), openmediavaultTestNative()...), "owned-omv"); err == nil {
		t.Fatal("duplicate results were accepted")
	}
	if err := openmediavaultValidateNative(map[string]any{}, "owned-omv"); err == nil {
		t.Fatal("non-list native response was accepted")
	}
}

func TestOpenmediavaultStatsRetainUnitsClockAndRetrySemantics(t *testing.T) {
	valid := func() statsSample {
		return statsSample{
			values:  map[string]any{"cpu": float64(0), "memoryUsed": float64(0), "memoryAvailable": float64(8192), "uptime": 12.5},
			units:   map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"},
			retryAt: float64(0), startedAt: 10000, completedAt: 10010, updatedAt: 10005,
		}
	}
	if err := openmediavaultValidateStats(valid()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*statsSample){
		func(s *statsSample) { s.values["cpu"] = float64(101) },
		func(s *statsSample) { s.values["memoryUsed"] = 1.5 },
		func(s *statsSample) { s.values["uptime"] = nil },
		func(s *statsSample) { delete(s.values, "memoryAvailable") },
		func(s *statsSample) { s.units["cpu"] = "bytes" },
		func(s *statsSample) { s.retryAt = nil },
		func(s *statsSample) { s.retryAt = float64(12000) },
		func(s *statsSample) { s.updatedAt = 20000 },
		func(s *statsSample) { s.updatedAt = math.NaN() },
		func(s *statsSample) { s.startedAt = 100000 },
	} {
		sample := valid()
		change(&sample)
		if err := openmediavaultValidateStats(sample); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("invalid OpenMediaVault Stats semantics were accepted")
		}
	}
}
