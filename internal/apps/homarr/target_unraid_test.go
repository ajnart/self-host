package homarr

import (
	"errors"
	"math"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

func unraidTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "unraid", Endpoint: "https://private-unraid.example.invalid",
		Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "private-unraid-key"}},
	}
}

func unraidNativeResult(id string, mutate func(map[string]any)) any {
	health := map[string]any{"cpuUtilization": 25.0, "memUsedInBytes": 2147483648.0, "memAvailableInBytes": 6442450944.0, "uptime": 3600.0}
	if mutate != nil {
		mutate(health)
	}
	return []any{map[string]any{"integrationId": id, "error": nil, "healthInfo": health}}
}

func TestUnraidExternalInputRequiresTheExactPrivateContract(t *testing.T) {
	c := unraidTestConnection()
	target, err := externalConnectionDescriptor(c)
	if err != nil || target.kind != "unraid" || target.id != "external-"+c.ID || !target.integrationOnly || target.internal(nil) != c.Endpoint {
		t.Fatalf("descriptor: %v", err)
	}
	if len(target.widgets) != 3 || target.widgets[0].kind != "healthMonitoring" || target.widgets[1].kind != "systemResources" || len(target.widgets[2].metrics) != 4 {
		t.Fatal("descriptor did not retain exactly the CPU and memory surfaces")
	}
	credentials := target.secrets(nil, nil)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" {
		t.Fatal("descriptor did not retain the apiKey alternative")
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Kind = "proxmox" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-unraid.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/graphql" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?key=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "#" },
		func(c *helpers.ExternalConnection) {
			c.Endpoint = "https://user:private@private-unraid.example.invalid"
		},
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-unraid.example.invalid:70000" },
		func(c *helpers.ExternalConnection) { c.Credentials = nil },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Value = "" },
		func(c *helpers.ExternalConnection) { c.Credentials = append(c.Credentials, c.Credentials[0]) },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Kind = "password" },
		func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"node": "private"} },
	} {
		input := unraidTestConnection()
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid Unraid connection was accepted")
		}
	}
}

func TestUnraidPreflightIsManualBeforeAnyRequest(t *testing.T) {
	target, err := unraidExternalTarget(unraidTestConnection())
	if err != nil {
		t.Fatal(err)
	}
	err = target.preflight(t.Context(), nil, nil)
	var manual *helpers.ManualError
	if !errors.As(err, &manual) || manual.Hint != unraidTransportHold {
		t.Fatal("preflight did not hold native transport")
	}
}

func TestUnraidNativeValidationAcceptsMeasuredDataAndGenuineZeros(t *testing.T) {
	id := "owned-id"
	for _, mutate := range []func(map[string]any){
		nil,
		func(h map[string]any) { h["cpuUtilization"], h["memUsedInBytes"], h["uptime"] = 0.0, 0.0, 0.0 },
		func(h map[string]any) { h["cpuUtilization"], h["memAvailableInBytes"] = 100.0, 0.0 },
	} {
		if err := unraidValidateNative(unraidNativeResult(id, mutate), id); err != nil {
			t.Fatalf("valid data rejected: %v", err)
		}
	}
}

func TestUnraidNativeValidationRejectsIncompleteOrImpossibleData(t *testing.T) {
	id := "owned-id"
	cases := map[string]any{
		"not an array":   map[string]any{},
		"empty":          []any{},
		"multiple":       append(unraidNativeResult(id, nil).([]any), unraidNativeResult(id, nil).([]any)...),
		"wrong id":       unraidNativeResult("other", nil),
		"error set":      []any{map[string]any{"integrationId": id, "error": "boom", "healthInfo": map[string]any{}}},
		"null health":    []any{map[string]any{"integrationId": id, "error": nil, "healthInfo": nil}},
		"missing cpu":    unraidNativeResult(id, func(h map[string]any) { delete(h, "cpuUtilization") }),
		"cpu over":       unraidNativeResult(id, func(h map[string]any) { h["cpuUtilization"] = 100.5 }),
		"cpu negative":   unraidNativeResult(id, func(h map[string]any) { h["cpuUtilization"] = -1.0 }),
		"cpu nan":        unraidNativeResult(id, func(h map[string]any) { h["cpuUtilization"] = math.NaN() }),
		"fractional mem": unraidNativeResult(id, func(h map[string]any) { h["memUsedInBytes"] = 1.5 }),
		"negative mem":   unraidNativeResult(id, func(h map[string]any) { h["memAvailableInBytes"] = -1.0 }),
		"zero total":     unraidNativeResult(id, func(h map[string]any) { h["memUsedInBytes"], h["memAvailableInBytes"] = 0.0, 0.0 }),
		"uptime string":  unraidNativeResult(id, func(h map[string]any) { h["uptime"] = "1" }),
		"uptime inf":     unraidNativeResult(id, func(h map[string]any) { h["uptime"] = math.Inf(1) }),
	}
	for name, value := range cases {
		if err := unraidValidateNative(value, id); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}

func unraidStatsSample(mutate func(*statsSample)) statsSample {
	sample := statsSample{
		values:    map[string]any{"cpu": 25.0, "memoryUsed": 2147483648.0, "memoryAvailable": 6442450944.0, "uptime": 3600.0},
		units:     map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"},
		retryAt:   0.0,
		startedAt: 1000000, completedAt: 1000100, updatedAt: 1000050,
	}
	if mutate != nil {
		mutate(&sample)
	}
	return sample
}

func TestUnraidStatsValidation(t *testing.T) {
	if err := unraidValidateStats(unraidStatsSample(nil)); err != nil {
		t.Fatal(err)
	}
	if err := unraidValidateStats(unraidStatsSample(func(s *statsSample) {
		s.values = map[string]any{"cpu": 0.0, "memoryUsed": 0.0, "memoryAvailable": 0.0, "uptime": 0.0}
	})); err != nil {
		t.Fatalf("genuine zeros rejected: %v", err)
	}
	for name, mutate := range map[string]func(*statsSample){
		"wrong unit":     func(s *statsSample) { s.units["cpu"] = "bytes" },
		"missing metric": func(s *statsSample) { delete(s.values, "uptime") },
		"null metric":    func(s *statsSample) { s.values["cpu"] = nil },
		"cpu over":       func(s *statsSample) { s.values["cpu"] = 101.0 },
		"fractional mem": func(s *statsSample) { s.values["memoryUsed"] = 0.5 },
		"retry pending":  func(s *statsSample) { s.retryAt = 5.0 },
		"retry missing":  func(s *statsSample) { s.retryAt = nil },
		"no timestamp":   func(s *statsSample) { s.updatedAt = 0 },
		"future":         func(s *statsSample) { s.updatedAt = s.completedAt + 5001 },
		"stale":          func(s *statsSample) { s.updatedAt = s.startedAt - 60001 },
	} {
		if err := unraidValidateStats(unraidStatsSample(mutate)); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}
