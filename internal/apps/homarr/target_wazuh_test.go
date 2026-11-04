package homarr

import (
	"errors"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

func wazuhTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "wazuh", Endpoint: "https://private-wazuh.example.invalid:55000",
		Credentials: []helpers.ExternalCredential{{Kind: "username", Value: "homarr-ro"}, {Kind: "password", Value: "private-password-188"}},
	}
}

func TestWazuhExternalInputRequiresTheExactServerOnlyPair(t *testing.T) {
	c := wazuhTestConnection()
	d, err := externalConnectionDescriptor(c)
	if err != nil || d.kind != "wazuh" || d.id != "external-"+c.ID || !d.integrationOnly || len(d.widgets) != 1 || strings.Join(d.widgets[0].metrics, ",") != "agents,activeAgents,disconnectedAgents" {
		t.Fatalf("descriptor: %v", err)
	}
	mutations := map[string]func(*helpers.ExternalConnection){
		"kind":      func(c *helpers.ExternalConnection) { c.Kind = "proxmox" },
		"http":      func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-wazuh.example.invalid:55000" },
		"path":      func(c *helpers.ExternalConnection) { c.Endpoint += "/security" },
		"query":     func(c *helpers.ExternalConnection) { c.Endpoint += "?x=1" },
		"userinfo":  func(c *helpers.ExternalConnection) { c.Endpoint = "https://u:p@private-wazuh.example.invalid" },
		"port":      func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-wazuh.example.invalid:0" },
		"missing":   func(c *helpers.ExternalConnection) { c.Credentials = c.Credentials[:1] },
		"empty":     func(c *helpers.ExternalConnection) { c.Credentials[1].Value = "" },
		"duplicate": func(c *helpers.ExternalConnection) { c.Credentials[1].Kind = "username" },
		"indexer": func(c *helpers.ExternalConnection) {
			c.Credentials = append(c.Credentials, helpers.ExternalCredential{Kind: "wazuhIndexerUsername", Value: "x"})
		},
		"selection": func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"scope": "cluster"} },
		"indexer-only": func(c *helpers.ExternalConnection) {
			c.Credentials[0].Kind, c.Credentials[1].Kind = "wazuhIndexerUsername", "wazuhIndexerPassword"
		},
	}
	for name, mutate := range mutations {
		c := wazuhTestConnection()
		c.Credentials = append([]helpers.ExternalCredential(nil), c.Credentials...)
		mutate(&c)
		_, err := externalConnectionDescriptor(c)
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		for _, private := range []string{"private-password-188", "homarr-ro"} {
			if strings.Contains(err.Error(), private) {
				t.Fatalf("%s error leaked a credential", name)
			}
		}
	}
}

func TestWazuhSummaryRejectsErrorsMissingAndInconsistentCounters(t *testing.T) {
	valid := `{"data":{"connection":{"total":3,"active":2,"disconnected":1,"never_connected":0,"pending":0}},"error":0}`
	empty := `{"data":{"connection":{"total":0,"active":0,"disconnected":0,"never_connected":0,"pending":0}},"error":0}`
	for _, body := range []string{valid, empty} {
		if err := wazuhValidateSummary([]byte(body)); err != nil {
			t.Fatalf("valid summary: %v", err)
		}
	}
	for name, body := range map[string]string{
		"error":        `{"data":{"connection":{"total":0,"active":0,"disconnected":0,"never_connected":0,"pending":0}},"error":1}`,
		"no-error":     `{"data":{"connection":{"total":0,"active":0,"disconnected":0,"never_connected":0,"pending":0}}}`,
		"missing":      `{"data":{"connection":{"total":3,"active":2,"disconnected":1}},"error":0}`,
		"null":         `{"data":{"connection":{"total":null,"active":0,"disconnected":0,"never_connected":0,"pending":0}},"error":0}`,
		"negative":     `{"data":{"connection":{"total":-1,"active":0,"disconnected":0,"never_connected":0,"pending":0}},"error":0}`,
		"fractional":   `{"data":{"connection":{"total":1.5,"active":0,"disconnected":0,"never_connected":0,"pending":0}},"error":0}`,
		"overflow":     `{"data":{"connection":{"total":1e300,"active":0,"disconnected":0,"never_connected":0,"pending":0}},"error":0}`,
		"inconsistent": `{"data":{"connection":{"total":1,"active":2,"disconnected":0,"never_connected":0,"pending":0}},"error":0}`,
		"no-data":      `{"error":0}`,
		"malformed":    `not json`,
	} {
		if wazuhValidateSummary([]byte(body)) == nil {
			t.Fatalf("%s summary was accepted", name)
		}
	}
}

func TestWazuhStatsRetainCountUnitsClockAndRetrySemantics(t *testing.T) {
	sample := func() statsSample {
		return statsSample{
			values:    map[string]any{"agents": float64(3), "activeAgents": float64(2), "disconnectedAgents": float64(1)},
			units:     map[string]string{"agents": "count", "activeAgents": "count", "disconnectedAgents": "count"},
			retryAt:   float64(0),
			startedAt: 1000, completedAt: 2000, updatedAt: 1500,
		}
	}
	if err := wazuhValidateStats(sample()); err != nil {
		t.Fatal(err)
	}
	zero := sample()
	zero.values = map[string]any{"agents": float64(0), "activeAgents": float64(0), "disconnectedAgents": float64(0)}
	if err := wazuhValidateStats(zero); err != nil {
		t.Fatalf("explicit zeros: %v", err)
	}
	mutations := map[string]func(*statsSample){
		"missing":   func(s *statsSample) { delete(s.values, "agents") },
		"null":      func(s *statsSample) { s.values["activeAgents"] = nil },
		"negative":  func(s *statsSample) { s.values["agents"] = float64(-1) },
		"fraction":  func(s *statsSample) { s.values["agents"] = 2.5 },
		"unit":      func(s *statsSample) { s.units["agents"] = "percent" },
		"sum":       func(s *statsSample) { s.values["agents"] = float64(2) },
		"retry":     func(s *statsSample) { s.retryAt = float64(5) },
		"retry-nil": func(s *statsSample) { s.retryAt = nil },
		"stale":     func(s *statsSample) { s.updatedAt = 0 },
		"old":       func(s *statsSample) { s.startedAt, s.completedAt, s.updatedAt = 200000, 201000, 1 },
		"future":    func(s *statsSample) { s.updatedAt = 100000 },
	}
	for name, mutate := range mutations {
		s := sample()
		mutate(&s)
		err := wazuhValidateStats(s)
		var manual *helpers.ManualError
		if !errors.As(err, &manual) {
			t.Fatalf("%s stats were accepted", name)
		}
	}
}
