package homarr

import (
	"errors"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

func coolifyTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "coolify", Endpoint: "https://private-coolify.example.invalid",
		Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "1|private-token"}},
	}
}

func TestCoolifyExternalInputRequiresTheExactPrivateContract(t *testing.T) {
	c := coolifyTestConnection()
	target, err := externalConnectionDescriptor(c)
	if err != nil || target.kind != "coolify" || target.id != "external-"+c.ID || !target.integrationOnly {
		t.Fatalf("descriptor: %v", err)
	}
	credentials := target.credentials(nil, nil)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" || target.internal(nil) != c.Endpoint || len(target.widgets) != 2 || target.widgets[0].kind != "coolify" || len(target.widgets[1].metrics) != 3 {
		t.Fatal("descriptor did not retain the exact credential, endpoint and surfaces")
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-coolify.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/api/v1" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?token=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "#" },
		func(c *helpers.ExternalConnection) {
			c.Endpoint = "https://user:private@private-coolify.example.invalid"
		},
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-coolify.example.invalid:70000" },
		func(c *helpers.ExternalConnection) { c.Credentials = nil },
		func(c *helpers.ExternalConnection) { c.Credentials = append(c.Credentials, c.Credentials[0]) },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Kind = "personalAccessToken" },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Value = "" },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Value = " 1|private-token" },
		func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"team": "1"} },
	} {
		input := coolifyTestConnection()
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid Coolify connection was accepted")
		}
	}
}

func TestCoolifyPreflightHoldsBeforeCredentialForwarding(t *testing.T) {
	target, err := externalConnectionDescriptor(coolifyTestConnection())
	if err != nil {
		t.Fatal(err)
	}
	var manual *helpers.ManualError
	if err := target.preflight(t.Context(), nil, nil); !errors.As(err, &manual) || manual.Hint != coolifyTransportHold {
		t.Fatal("Coolify preflight did not hold")
	}
}

func coolifyNativeEntry(now time.Time) map[string]any {
	return map[string]any{
		"integrationId": "integration-1", "integrationUrl": "https://private-coolify.example.invalid",
		"updatedAt": now.Add(-time.Second).UTC().Format(time.RFC3339Nano),
		"instanceInfo": map[string]any{
			"version":      "4.0.0-beta.474",
			"applications": []any{},
			"services":     []any{},
			"servers":      []any{map[string]any{"uuid": "server-1", "name": "fixture-server"}},
		},
	}
}

func TestCoolifyNativeValidation(t *testing.T) {
	now := time.Now()
	valid := func(mutate func(map[string]any)) any {
		entry := coolifyNativeEntry(now)
		mutate(entry)
		return []any{entry}
	}
	info := func(entry map[string]any) map[string]any { return entry["instanceInfo"].(map[string]any) }
	if err := coolifyValidateNative(valid(func(map[string]any) {}), "integration-1", "https://private-coolify.example.invalid", now); err != nil {
		t.Fatalf("valid empty-workload inventory rejected: %v", err)
	}
	for name, value := range map[string]any{
		"not a list":     map[string]any{},
		"two entries":    []any{coolifyNativeEntry(now), coolifyNativeEntry(now)},
		"wrong id":       valid(func(e map[string]any) { e["integrationId"] = "other" }),
		"wrong url":      valid(func(e map[string]any) { e["integrationUrl"] = "https://other.example.invalid" }),
		"error":          valid(func(e map[string]any) { e["error"] = "boom" }),
		"null info":      valid(func(e map[string]any) { e["instanceInfo"] = nil }),
		"missing ver":    valid(func(e map[string]any) { delete(info(e), "version") }),
		"missing array":  valid(func(e map[string]any) { delete(info(e), "services") }),
		"null array":     valid(func(e map[string]any) { info(e)["applications"] = nil }),
		"stale":          valid(func(e map[string]any) { e["updatedAt"] = now.Add(-time.Minute).UTC().Format(time.RFC3339Nano) }),
		"future":         valid(func(e map[string]any) { e["updatedAt"] = now.Add(time.Minute).UTC().Format(time.RFC3339Nano) }),
		"malformed time": valid(func(e map[string]any) { e["updatedAt"] = "yesterday" }),
		"epoch":          valid(func(e map[string]any) { e["updatedAt"] = float64(0) }),
		"no uuid":        valid(func(e map[string]any) { info(e)["servers"] = []any{map[string]any{"name": "x"}} }),
		"duplicate uuid": valid(func(e map[string]any) {
			item := map[string]any{"uuid": "a", "name": "x"}
			info(e)["applications"] = []any{item, item}
		}),
	} {
		var manual *helpers.ManualError
		if err := coolifyValidateNative(value, "integration-1", "https://private-coolify.example.invalid", now); !errors.As(err, &manual) {
			t.Fatalf("%s: invalid native data accepted", name)
		}
	}
}

func TestCoolifyStatsValidation(t *testing.T) {
	good := func() statsSample {
		return statsSample{
			values:  map[string]any{"applications": float64(0), "services": float64(0), "servers": float64(1)},
			units:   map[string]string{"applications": "count", "services": "count", "servers": "count"},
			retryAt: float64(0), startedAt: 1000000, completedAt: 1000100, updatedAt: 1000050,
		}
	}
	if err := coolifyValidateStats(good()); err != nil {
		t.Fatalf("valid stats rejected: %v", err)
	}
	for name, mutate := range map[string]func(*statsSample){
		"fraction":   func(s *statsSample) { s.values["servers"] = 1.5 },
		"negative":   func(s *statsSample) { s.values["servers"] = float64(-1) },
		"missing":    func(s *statsSample) { delete(s.values, "services") },
		"string":     func(s *statsSample) { s.values["servers"] = "1" },
		"unit":       func(s *statsSample) { s.units["servers"] = "percent" },
		"retry":      func(s *statsSample) { s.retryAt = float64(5) },
		"no retry":   func(s *statsSample) { s.retryAt = nil },
		"future":     func(s *statsSample) { s.updatedAt = s.completedAt + 6000 },
		"old":        func(s *statsSample) { s.updatedAt = s.startedAt - 61000 },
		"fractional": func(s *statsSample) { s.updatedAt = 1000050.5 },
		"epoch":      func(s *statsSample) { s.updatedAt = 0 },
	} {
		s := good()
		mutate(&s)
		var manual *helpers.ManualError
		if err := coolifyValidateStats(s); !errors.As(err, &manual) {
			t.Fatalf("%s: invalid stats accepted", name)
		}
	}
}
