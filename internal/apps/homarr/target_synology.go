package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

const synologyTransportHold = "Synology native transport is held before credential forwarding or integration creation: the audited Homarr Synology client follows redirects and caches API discovery by integration ID, so a successful CLI no-redirect probe cannot establish native confinement. Preserve this connection and its credentials. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not use a proxy, disable TLS verification, weaken MFA, Adaptive MFA or Auto Block, or change DSM to bypass this hold."
const synologyDataRecovery = "Synology's DSM CPU, memory and uptime data are not verified. Preserve its credentials; check that the dedicated account can sign in without MFA prompts, is permitted by DSM and NAS policy to read system utilization, and that DSM presents a trusted certificate, then retry self-host setup homarr after the native transport hold is resolved. Missing or defaulted metrics are not measured zeros."
const synologyStatsRecovery = "Synology Stats require measured CPU percent, memory byte counts and uptime seconds from one successful genuinely advancing refresh. Preserve the existing credentials and retry Homarr Setup after the native transport hold is resolved; do not invent values or use cached/no-op data as acceptance."

func synologyExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "synology" {
		return target{}, errors.New("the Synology external adapter requires the exact synology kind")
	}
	if err := externalHTTPSOrigin(c.Endpoint, "Synology"); err != nil {
		return target{}, err
	}
	values := map[string]string{}
	for _, credential := range c.Credentials {
		if _, duplicate := values[credential.Kind]; duplicate || credential.Value == "" {
			return target{}, errors.New("Synology requires exactly one nonempty username and password credential")
		}
		switch credential.Kind {
		case "username", "password":
			values[credential.Kind] = credential.Value
		default:
			return target{}, errors.New("Synology supports only the username and password credential alternative")
		}
	}
	if len(values) != 2 {
		return target{}, errors.New("Synology requires a dedicated DSM username and password; supply them only through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("Synology accepts no selection; it reports the one DiskStation at its endpoint")
	}
	return target{
		name: "Synology", kind: "synology",
		widgets: []widget{
			{kind: "systemResources", options: map[string]any{"visibleCharts": []string{"cpu", "memory"}}},
			{kind: "healthMonitoring", options: map[string]any{"cpu": true, "memory": true, "showUptime": true, "gpu": false, "fileSystem": false}},
			statWidget("cpu", "memoryUsed", "memoryAvailable", "uptime"),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: synologyTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.healthMonitoring.getSystemHealthStatus", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: synologyDataRecovery}
			}
			return synologyValidateNative(value, integrationID)
		},
		stats: synologyValidateStats,
	}, nil
}

func synologyValidateNative(value any, integrationID string) error {
	invalid := func() error { return &helpers.ManualError{Hint: synologyDataRecovery} }
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return invalid()
	}
	result, ok := results[0].(map[string]any)
	if !ok || stringField(result, "integrationId") != integrationID || result["error"] != nil {
		return invalid()
	}
	health, ok := result["healthInfo"].(map[string]any)
	if !ok {
		return invalid()
	}
	cpu, cpuOK := measuredNumber(health["cpuUtilization"], false)
	used, usedOK := measuredNumber(health["memUsedInBytes"], true)
	available, availableOK := measuredNumber(health["memAvailableInBytes"], true)
	_, uptimeOK := measuredNumber(health["uptime"], true)
	if !cpuOK || cpu > 100 || !usedOK || !availableOK || used+available == 0 || !uptimeOK {
		return invalid()
	}
	return nil
}

func synologyValidateStats(sample statsSample) error {
	invalid := func() error { return &helpers.ManualError{Hint: synologyStatsRecovery} }
	for key, unit := range map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"} {
		number, valid := measuredNumber(sample.values[key], key != "cpu")
		if sample.units[key] != unit || !valid || key == "cpu" && number > 100 {
			return invalid()
		}
	}
	retry, retryOK := sample.retryAt.(float64)
	if !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return invalid()
	}
	return nil
}
