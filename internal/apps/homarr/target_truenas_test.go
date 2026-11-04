package homarr

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func truenasTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "0123456789abcdef0123456789abcdef", Kind: "truenas", Endpoint: "https://private-truenas.example.invalid",
		Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "private-truenas-key"}},
	}
}

func TestTrueNASExternalInputRequiresTheExactPrivateContract(t *testing.T) {
	c := truenasTestConnection()
	d, err := externalConnectionDescriptor(c)
	if err != nil || d.kind != "truenas" || d.id != "external-"+c.ID || !d.integrationOnly || d.internal(nil) != c.Endpoint {
		t.Fatalf("descriptor: %v", err)
	}
	credentials := d.credentials(nil, nil)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" || len(d.widgets) != 2 || d.widgets[0].kind != "systemResources" || len(d.widgets[1].metrics) != 4 {
		t.Fatal("descriptor did not retain the API key and the read-only CPU/RAM surface")
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://private-truenas.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/api/current" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?key=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "#" },
		func(c *helpers.ExternalConnection) {
			c.Endpoint = "https://user:private@private-truenas.example.invalid"
		},
		func(c *helpers.ExternalConnection) { c.Endpoint = "https://private-truenas.example.invalid:70000" },
		func(c *helpers.ExternalConnection) { c.Credentials = nil },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Value = "" },
		func(c *helpers.ExternalConnection) { c.Credentials[0].Kind = "password" },
		func(c *helpers.ExternalConnection) {
			c.Credentials = append(c.Credentials, helpers.ExternalCredential{Kind: "username", Value: "admin"})
		},
		func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"pool": "tank"} },
		func(c *helpers.ExternalConnection) { c.Kind = "truenas-scale" },
	} {
		input := truenasTestConnection()
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid TrueNAS connection was accepted")
		}
	}
	input := truenasTestConnection()
	input.Endpoint = "https://private-truenas.example.invalid:8443/"
	if err := (App{}).ValidateExternalConnection(input); err != nil {
		t.Fatalf("custom port rejected: %v", err)
	}
	encoded, _ := json.Marshal(truenasTestConnection())
	if strings.Contains(string(encoded), "private-truenas") {
		t.Fatal("TrueNAS private input escaped the connection DTO")
	}
}

func truenasTestHealth(now time.Time) []any {
	return []any{map[string]any{
		"integrationId": "owned-truenas", "updatedAt": float64(now.UnixMilli()),
		"healthInfo": map[string]any{"cpuUtilization": float64(0), "memUsedInBytes": float64(0), "memAvailableInBytes": float64(8192), "uptime": float64(0)},
	}}
}

func TestTrueNASNativeSemanticsRequireOwnedFreshMeasuredData(t *testing.T) {
	now := time.Now()
	if err := truenasValidateNative(truenasTestHealth(now), "owned-truenas", now); err != nil {
		t.Fatalf("legitimate zeros were rejected: %v", err)
	}
	rfc := truenasTestHealth(now)
	rfc[0].(map[string]any)["updatedAt"] = now.Format(time.RFC3339Nano)
	if err := truenasValidateNative(rfc, "owned-truenas", now); err != nil {
		t.Fatalf("RFC3339 timestamp rejected: %v", err)
	}
	for name, change := range map[string]func(map[string]any, map[string]any){
		"error":         func(r, _ map[string]any) { r["error"] = "unavailable" },
		"null info":     func(r, _ map[string]any) { r["healthInfo"] = nil },
		"wrong id":      func(r, _ map[string]any) { r["integrationId"] = "user-truenas" },
		"missing time":  func(r, _ map[string]any) { delete(r, "updatedAt") },
		"stale time":    func(r, _ map[string]any) { r["updatedAt"] = float64(now.Add(-time.Hour).UnixMilli()) },
		"future time":   func(r, _ map[string]any) { r["updatedAt"] = float64(now.Add(time.Hour).UnixMilli()) },
		"null cpu":      func(_, h map[string]any) { h["cpuUtilization"] = nil },
		"cpu over 100":  func(_, h map[string]any) { h["cpuUtilization"] = float64(101) },
		"negative cpu":  func(_, h map[string]any) { h["cpuUtilization"] = float64(-1) },
		"nan cpu":       func(_, h map[string]any) { h["cpuUtilization"] = math.NaN() },
		"fractional":    func(_, h map[string]any) { h["memUsedInBytes"] = 1.5 },
		"missing avail": func(_, h map[string]any) { delete(h, "memAvailableInBytes") },
		"zero total":    func(_, h map[string]any) { h["memAvailableInBytes"] = float64(0) },
		"inf uptime":    func(_, h map[string]any) { h["uptime"] = math.Inf(1) },
		"negative up":   func(_, h map[string]any) { h["uptime"] = float64(-1) },
	} {
		health := truenasTestHealth(now)
		result := health[0].(map[string]any)
		change(result, func() map[string]any { m, _ := result["healthInfo"].(map[string]any); return m }())
		if err := truenasValidateNative(health, "owned-truenas", now); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("%s was accepted", name)
		}
	}
	two := append(truenasTestHealth(now), truenasTestHealth(now)...)
	if truenasValidateNative(two, "owned-truenas", now) == nil || truenasValidateNative([]any{}, "owned-truenas", now) == nil || truenasValidateNative(nil, "owned-truenas", now) == nil {
		t.Fatal("a non-singular result was accepted")
	}
}

func TestTrueNASStatsRetainUnitsClockAndRetrySemantics(t *testing.T) {
	valid := func() statsSample {
		return statsSample{
			values:  map[string]any{"cpu": float64(0), "memoryUsed": float64(0), "memoryAvailable": float64(8192), "uptime": float64(0)},
			units:   map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"},
			retryAt: float64(0), startedAt: 10000, completedAt: 10010, updatedAt: 10005,
		}
	}
	if err := truenasValidateStats(valid()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*statsSample){
		"null cpu":      func(s *statsSample) { s.values["cpu"] = nil },
		"cpu over 100":  func(s *statsSample) { s.values["cpu"] = float64(101) },
		"missing up":    func(s *statsSample) { delete(s.values, "uptime") },
		"fractional":    func(s *statsSample) { s.values["memoryUsed"] = 0.5 },
		"zero total":    func(s *statsSample) { s.values["memoryAvailable"] = float64(0) },
		"wrong unit":    func(s *statsSample) { s.units["memoryUsed"] = "percent" },
		"nil retry":     func(s *statsSample) { s.retryAt = nil },
		"retry hold":    func(s *statsSample) { s.retryAt = float64(12000) },
		"future time":   func(s *statsSample) { s.updatedAt = 20000 },
		"nan time":      func(s *statsSample) { s.updatedAt = math.NaN() },
		"ancient time":  func(s *statsSample) { s.startedAt = 100000 },
		"negative time": func(s *statsSample) { s.updatedAt = -1 },
	} {
		sample := valid()
		change(&sample)
		if err := truenasValidateStats(sample); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestTrueNASVerifyTestsStoredCredentialsBeforeReadingData(t *testing.T) {
	d, err := truenasExternalTarget(truenasTestConnection())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	valid := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, strings.TrimPrefix(r.URL.Path, "/api/trpc/"))
		switch r.URL.Path {
		case "/api/trpc/integration.testConnection":
			var input struct {
				JSON map[string]any `json:"json"`
			}
			json.Unmarshal([]byte(r.URL.Query().Get("input")), &input)
			if r.Method != http.MethodGet || input.JSON["id"] != "owned-truenas" || len(input.JSON) != 1 {
				t.Error("connection test was not a query for only the owned integration ID")
			}
			if !valid {
				externalConnectionReply(w, map[string]any{"success": false, "error": "authorization"})
				return
			}
			externalConnectionReply(w, map[string]any{"success": true})
		case "/api/trpc/widget.healthMonitoring.getSystemHealthStatus":
			externalConnectionReply(w, truenasTestHealth(time.Now()))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	h := helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
	if err := d.verify(t.Context(), h, nil, "owned-truenas"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "integration.testConnection,widget.healthMonitoring.getSystemHealthStatus" {
		t.Fatalf("request order: %v", order)
	}
	order, valid = nil, false
	err = d.verify(t.Context(), h, nil, "owned-truenas")
	if !errors.Is(err, helpers.ErrManual) || len(order) != 1 || strings.Contains(err.Error(), "private-truenas-key") {
		t.Fatal("a revoked key was masked by cached data or leaked")
	}
}
