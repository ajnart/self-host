package homarr

import (
	"context"
	"errors"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const truenasDataRecovery = "TrueNAS CPU and memory data are not verified. Preserve this connection's API key; check that the endpoint is the intended trusted-HTTPS TrueNAS origin, that the user-linked key is read-only and unrevoked, that the system serves reporting data and that Homarr can reach it, then run self-host setup homarr. Password or two-factor login is unsupported. Do not disable TLS verification, use HTTP, rotate credentials or broaden TrueNAS access to bypass this."
const truenasStatsRecovery = "TrueNAS Stats require measured CPU, memory and uptime from one successful genuinely advancing refresh with valid clock and retry metadata. Preserve the API key and retry self-host setup homarr; do not use cached or no-op data as acceptance."
const truenasMaxAge = 2 * time.Minute
const truenasMaxSkew = 5 * time.Second

func truenasExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "truenas" {
		return target{}, errors.New("the TrueNAS external adapter requires the exact truenas kind")
	}
	if err := externalHTTPSOrigin(c.Endpoint, "TrueNAS"); err != nil {
		return target{}, err
	}
	if len(c.Credentials) != 1 || c.Credentials[0].Kind != "apiKey" || c.Credentials[0].Value == "" {
		return target{}, errors.New("TrueNAS requires exactly one nonempty apiKey credential from a dedicated read-only user; supply it only through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("TrueNAS takes no selection; it reports the one system behind the endpoint")
	}
	return target{
		name: "TrueNAS", kind: "truenas",
		widgets: []widget{
			{kind: "systemResources", options: map[string]any{"visibleCharts": []string{"cpu", "memory"}}},
			statWidget("cpu", "memoryUsed", "memoryAvailable", "uptime"),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error { return nil },
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			if _, err := apiKeyWrite(ctx, from, "integration.testConnection", map[string]any{"id": integrationID}); err != nil {
				return &helpers.ManualError{Hint: truenasDataRecovery}
			}
			value, err := apiKeyProcedure(ctx, from, "widget.healthMonitoring.getSystemHealthStatus", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: truenasDataRecovery}
			}
			return truenasValidateNative(value, integrationID, from.Now())
		},
		stats: truenasValidateStats,
	}, nil
}

func truenasFresh(value any, now time.Time) bool {
	var at time.Time
	switch stamp := value.(type) {
	case float64:
		if !validStatsTimestamp(stamp) {
			return false
		}
		at = time.UnixMilli(int64(stamp))
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return false
		}
		at = parsed
	default:
		return false
	}
	return !at.After(now.Add(truenasMaxSkew)) && now.Sub(at) <= truenasMaxAge
}

func truenasValidateNative(value any, integrationID string, now time.Time) error {
	invalid := func() error { return &helpers.ManualError{Hint: truenasDataRecovery} }
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return invalid()
	}
	result, ok := results[0].(map[string]any)
	if !ok || stringField(result, "integrationId") != integrationID || result["error"] != nil && result["error"] != false || !truenasFresh(result["updatedAt"], now) {
		return invalid()
	}
	health, ok := result["healthInfo"].(map[string]any)
	if !ok {
		return invalid()
	}
	cpu, cpuOK := measuredNumber(health["cpuUtilization"], false)
	used, usedOK := measuredNumber(health["memUsedInBytes"], true)
	available, availableOK := measuredNumber(health["memAvailableInBytes"], true)
	_, uptimeOK := measuredNumber(health["uptime"], false)
	if !cpuOK || cpu > 100 || !usedOK || !availableOK || used+available == 0 || !uptimeOK {
		return invalid()
	}
	return nil
}

func truenasValidateStats(sample statsSample) error {
	invalid := func() error { return &helpers.ManualError{Hint: truenasStatsRecovery} }
	units := map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"}
	numbers := map[string]float64{}
	for key, unit := range units {
		number, ok := measuredNumber(sample.values[key], key == "memoryUsed" || key == "memoryAvailable")
		if !ok || sample.units[key] != unit {
			return invalid()
		}
		numbers[key] = number
	}
	retry, retryOK := sample.retryAt.(float64)
	if numbers["cpu"] > 100 || numbers["memoryUsed"]+numbers["memoryAvailable"] == 0 || !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return invalid()
	}
	return nil
}
