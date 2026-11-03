package homarr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

func synologyConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "abc", Kind: "synology", Endpoint: "https://nas.example.invalid:5001",
		Credentials: []helpers.ExternalCredential{{Kind: "username", Value: "u"}, {Kind: "password", Value: "p"}},
	}
}

func TestSynologyExternalTargetValidation(t *testing.T) {
	if _, err := externalConnectionDescriptor(synologyConnection()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*helpers.ExternalConnection){
		"http":      func(c *helpers.ExternalConnection) { c.Endpoint = "http://nas.example.invalid:5000" },
		"path":      func(c *helpers.ExternalConnection) { c.Endpoint = "https://nas.example.invalid/webapi" },
		"userinfo":  func(c *helpers.ExternalConnection) { c.Endpoint = "https://a:b@nas.example.invalid" },
		"badport":   func(c *helpers.ExternalConnection) { c.Endpoint = "https://nas.example.invalid:99999" },
		"missing":   func(c *helpers.ExternalConnection) { c.Credentials = c.Credentials[:1] },
		"duplicate": func(c *helpers.ExternalConnection) { c.Credentials[1].Kind = "username" },
		"token":     func(c *helpers.ExternalConnection) { c.Credentials[1].Kind = "apiKey" },
		"empty":     func(c *helpers.ExternalConnection) { c.Credentials[1].Value = "" },
		"selection": func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"node": "x"} },
	} {
		c := synologyConnection()
		c.Credentials = append([]helpers.ExternalCredential(nil), c.Credentials...)
		mutate(&c)
		if _, err := externalConnectionDescriptor(c); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSynologyPreflightHoldsBeforeCredentialForwarding(t *testing.T) {
	d, err := externalConnectionDescriptor(synologyConnection())
	if err != nil {
		t.Fatal(err)
	}
	err = d.preflight(context.Background(), nil, nil)
	var manual *helpers.ManualError
	if !errors.As(err, &manual) || !strings.Contains(manual.Hint, "follows redirects") {
		t.Fatalf("preflight did not hold: %v", err)
	}
}

func synologyHealth() []any {
	return []any{map[string]any{"integrationId": "id1", "healthInfo": map[string]any{
		"cpuUtilization": 10.0, "memUsedInBytes": 268435456.0, "memAvailableInBytes": 805306368.0, "uptime": 3723.0,
	}}}
}

func TestSynologyValidateNative(t *testing.T) {
	if err := synologyValidateNative(synologyHealth(), "id1"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]any){
		"other id": func(r []any) { r[0].(map[string]any)["integrationId"] = "id2" },
		"error":    func(r []any) { r[0].(map[string]any)["error"] = "x" },
		"missing":  func(r []any) { delete(r[0].(map[string]any)["healthInfo"].(map[string]any), "uptime") },
		"null":     func(r []any) { r[0].(map[string]any)["healthInfo"].(map[string]any)["cpuUtilization"] = nil },
		"over":     func(r []any) { r[0].(map[string]any)["healthInfo"].(map[string]any)["cpuUtilization"] = 101.0 },
		"zero mem": func(r []any) {
			h := r[0].(map[string]any)["healthInfo"].(map[string]any)
			h["memUsedInBytes"], h["memAvailableInBytes"] = 0.0, 0.0
		},
	} {
		r := synologyHealth()
		mutate(r)
		if synologyValidateNative(r, "id1") == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if synologyValidateNative(nil, "id1") == nil {
		t.Error("null result was accepted")
	}
}

func synologySample() statsSample {
	return statsSample{
		values:    map[string]any{"cpu": 10.0, "memoryUsed": 268435456.0, "memoryAvailable": 805306368.0, "uptime": 3723.0},
		units:     map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"},
		retryAt:   0.0,
		startedAt: 1000, completedAt: 1100, updatedAt: 1050,
	}
}

func TestSynologyValidateStats(t *testing.T) {
	if err := synologyValidateStats(synologySample()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*statsSample){
		"unit":    func(s *statsSample) { s.units["cpu"] = "bytes" },
		"missing": func(s *statsSample) { delete(s.values, "uptime") },
		"null":    func(s *statsSample) { s.values["cpu"] = nil },
		"cpu":     func(s *statsSample) { s.values["cpu"] = 120.0 },
		"frac":    func(s *statsSample) { s.values["memoryUsed"] = 1.5 },
		"retry":   func(s *statsSample) { s.retryAt = 5.0 },
		"stale":   func(s *statsSample) { s.updatedAt = 0 },
	} {
		s := synologySample()
		s.values = map[string]any{"cpu": 10.0, "memoryUsed": 268435456.0, "memoryAvailable": 805306368.0, "uptime": 3723.0}
		s.units = map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"}
		mutate(&s)
		if synologyValidateStats(s) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
