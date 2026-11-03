package homarr

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const unraidTransportHold = "Unraid native transport is held before credential forwarding or integration creation: Homarr's Unraid client sends the API key to POST /graphql and its redirect and TLS confinement is not audited, so a successful CLI probe cannot establish native confinement. Preserve this connection and its key. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not install the Unraid API plugin, use a proxy, disable TLS verification, rotate keys or change Unraid to bypass this hold."
const unraidDataRecovery = "Unraid's read-only CPU and memory data is not verified. Preserve the key; confirm Unraid 7.2 or newer already exposes the API (an older server needs its plugin installed by you, never by self-host), that the dedicated key has READ_ANY on INFO and ARRAY, and that TLS is trusted, then retry self-host setup homarr. Missing or defaulted metrics are not measured zeros."

func unraidExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "unraid" {
		return target{}, errors.New("the Unraid external adapter requires the exact unraid kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("Unraid requires an approved trusted-HTTPS origin without embedded credentials, API paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, errors.New("the private Unraid endpoint has an invalid port")
		}
	}
	if len(c.Credentials) != 1 || c.Credentials[0].Kind != "apiKey" || c.Credentials[0].Value == "" {
		return target{}, errors.New("Unraid requires exactly one nonempty apiKey credential; supply it only through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("Unraid has no selection; remove it from the private input")
	}
	return target{
		name: "Unraid", kind: "unraid",
		widgets: []widget{
			{kind: "healthMonitoring", options: map[string]any{"cpu": true, "memory": true, "gpu": false, "fileSystem": false}},
			{kind: "systemResources", options: map[string]any{"visibleCharts": []string{"cpu", "memory"}}},
			statWidget("cpu", "memoryUsed", "memoryAvailable", "uptime"),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: unraidTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.healthMonitoring.getSystemHealthStatus", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: unraidDataRecovery}
			}
			return unraidValidateNative(value, integrationID)
		},
		stats: unraidValidateStats,
	}, nil
}

func unraidNumber(value any, integral bool, max float64) (float64, bool) {
	number, ok := measuredNumber(value, integral)
	return number, ok && number <= max
}

func unraidValidateNative(value any, integrationID string) error {
	invalid := func() error { return &helpers.ManualError{Hint: unraidDataRecovery} }
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return invalid()
	}
	result, ok := results[0].(map[string]any)
	if !ok || stringField(result, "integrationId") != integrationID || result["error"] != nil && result["error"] != false {
		return invalid()
	}
	health, ok := result["healthInfo"].(map[string]any)
	if !ok {
		return invalid()
	}
	_, cpuOK := unraidNumber(health["cpuUtilization"], false, 100)
	used, usedOK := unraidNumber(health["memUsedInBytes"], true, 1<<53-1)
	available, availableOK := unraidNumber(health["memAvailableInBytes"], true, 1<<53-1)
	_, uptimeOK := unraidNumber(health["uptime"], false, math.MaxFloat64)
	if !cpuOK || !usedOK || !availableOK || used+available == 0 || !uptimeOK {
		return invalid()
	}
	return nil
}

func unraidValidateStats(sample statsSample) error {
	retry, retryOK := sample.retryAt.(float64)
	valid := retryOK && retry == 0 && validStatsTimestamp(sample.updatedAt) && sample.updatedAt <= sample.completedAt+5000 && sample.updatedAt >= sample.startedAt-60000
	for key, unit := range map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"} {
		limit := math.MaxFloat64
		if key == "cpu" {
			limit = 100
		}
		_, ok := unraidNumber(sample.values[key], key == "memoryUsed" || key == "memoryAvailable", limit)
		valid = valid && ok && sample.units[key] == unit
	}
	if !valid {
		return &helpers.ManualError{Hint: "Unraid Stats require measured CPU percent, memory bytes and uptime seconds from one successful genuinely advancing refresh with valid clock/retry metadata. Preserve the existing key and retry Homarr Setup after the native transport hold is resolved; do not invent values or use cached/no-op data as acceptance."}
	}
	return nil
}
