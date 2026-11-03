package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

const openmediavaultTransportHold = "OpenMediaVault native transport is held before credential forwarding or integration creation: the audited Homarr OpenMediaVault adapter uses an ordinary trusted-certificate fetch without explicit manual redirects, so redirect confinement is unproved. Preserve this connection and its credentials. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not use a proxy, disable TLS verification, enroll certificates, rotate credentials, install plugins or change OpenMediaVault to bypass this hold."
const openmediavaultDataRecovery = "OpenMediaVault resource data is not verified. Preserve the connection; check that the account is an administrator without MFA or challenges (monitoring has no read-only scope), that the endpoint uses trusted TLS and that OpenMediaVault answers on /rpc.php, then retry self-host setup homarr after the native transport hold is resolved. Do not install the CPU-temperature plugin. Missing or defaulted metrics are not measured zeros."
const openmediavaultStatsRecovery = "OpenMediaVault Stats require measured percent CPU, byte memory and second uptime values with genuinely advancing refresh and valid clock/retry metadata. Preserve the connection and retry Homarr Setup after the native transport hold is resolved; do not use cached or no-op data as acceptance."

func openmediavaultExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "openmediavault" {
		return target{}, errors.New("the OpenMediaVault external adapter requires the exact openmediavault kind")
	}
	if err := externalHTTPSOrigin(c.Endpoint, "OpenMediaVault"); err != nil {
		return target{}, err
	}
	values := map[string]string{}
	for _, credential := range c.Credentials {
		if _, duplicate := values[credential.Kind]; duplicate || credential.Value == "" {
			return target{}, errors.New("OpenMediaVault requires exactly one nonempty username and password credential")
		}
		switch credential.Kind {
		case "username", "password":
			values[credential.Kind] = credential.Value
		default:
			return target{}, errors.New("OpenMediaVault supports only the username and password credential alternative")
		}
	}
	if len(values) != 2 {
		return target{}, errors.New("OpenMediaVault requires an administrator username and password supplied only through the owner-only private input; monitoring has no read-only scope")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("OpenMediaVault accepts no selection")
	}
	return target{
		name: "OpenMediaVault", kind: "openmediavault",
		widgets: []widget{
			{kind: "systemResources", options: map[string]any{"visibleCharts": []string{"cpu", "memory"}, "hasShadow": true, "labelDisplayMode": "textWithIcon"}},
			statWidget("cpu", "memoryUsed", "memoryAvailable", "uptime"),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: openmediavaultTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.healthMonitoring.getSystemHealthStatus", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: openmediavaultDataRecovery}
			}
			return openmediavaultValidateNative(value, integrationID)
		},
		stats: openmediavaultValidateStats,
	}, nil
}

func openmediavaultNumber(value any, percent, integral bool) bool {
	number, ok := measuredNumber(value, integral)
	return ok && (!percent || number <= 100)
}

func openmediavaultValidateNative(value any, integrationID string) error {
	invalid := &helpers.ManualError{Hint: openmediavaultDataRecovery}
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return invalid
	}
	result, ok := results[0].(map[string]any)
	if !ok || stringField(result, "integrationId") != integrationID || result["error"] != nil && result["error"] != false || result["stale"] != nil && result["stale"] != false {
		return invalid
	}
	health, ok := result["healthInfo"].(map[string]any)
	if !ok || !openmediavaultNumber(health["cpuUtilization"], true, false) || !openmediavaultNumber(health["memUsedInBytes"], false, true) || !openmediavaultNumber(health["memAvailableInBytes"], false, true) || !openmediavaultNumber(health["uptime"], false, false) {
		return invalid
	}
	return nil
}

func openmediavaultValidateStats(sample statsSample) error {
	invalid := &helpers.ManualError{Hint: openmediavaultStatsRecovery}
	for key, unit := range map[string]string{"cpu": "percent", "memoryUsed": "bytes", "memoryAvailable": "bytes", "uptime": "seconds"} {
		if sample.units[key] != unit || !openmediavaultNumber(sample.values[key], key == "cpu", key == "memoryUsed" || key == "memoryAvailable") {
			return invalid
		}
	}
	retry, retryOK := sample.retryAt.(float64)
	if !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return invalid
	}
	return nil
}
