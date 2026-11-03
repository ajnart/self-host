package homarr

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const opnsenseRecoveryTail = "Preserve the connection, check the endpoint's trusted HTTPS certificate and the dedicated enabled, unexpired API user's key and secret (Lobby: Dashboard privilege only), then run self-host connections update <id> --file PATH --yes and self-host setup homarr. Do not disable TLS verification, broaden privileges or rotate credentials blindly."
const opnsenseIdentityRecovery = "The OPNsense identity could not be confirmed. " + opnsenseRecoveryTail
const opnsenseRejectedRecovery = "OPNsense rejected the API key and secret. The secret is shown only once and cannot be recovered: create a new pair for the dedicated user. " + opnsenseRecoveryTail
const opnsenseForbiddenRecovery = "OPNsense accepted the API user but denied the Lobby: Dashboard privilege. " + opnsenseRecoveryTail
const opnsenseDataRecovery = "Homarr's native OPNsense CPU reading is missing, failed or stale. Do not substitute another metric. " + opnsenseRecoveryTail

func opnsenseExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "opnsense" {
		return target{}, errors.New("the OPNsense external adapter requires the exact opnsense kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("OPNsense requires an approved trusted-HTTPS origin without embedded credentials, paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, errors.New("the private OPNsense endpoint has an invalid port")
		}
	}
	values := map[string]string{}
	for _, credential := range c.Credentials {
		if _, duplicate := values[credential.Kind]; duplicate || credential.Value == "" {
			return target{}, errors.New("OPNsense requires exactly one nonempty opnsenseApiKey and opnsenseApiSecret credential")
		}
		switch credential.Kind {
		case "opnsenseApiKey", "opnsenseApiSecret":
			values[credential.Kind] = credential.Value
		default:
			return target{}, errors.New("OPNsense supports only the opnsenseApiKey and opnsenseApiSecret credential alternative")
		}
	}
	if len(values) != 2 {
		return target{}, errors.New("OPNsense requires exactly one nonempty opnsenseApiKey and opnsenseApiSecret credential; supply them only through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("OPNsense takes no selection; the CPU Stats surface is fixed")
	}
	key, secretValue := values["opnsenseApiKey"], values["opnsenseApiSecret"]
	return target{
		name: "OPNsense", kind: "opnsense",
		widgets: []widget{statWidget("cpu")},
		preflight: func(ctx context.Context, from, _ *helpers.H) error {
			resp, err := from.HTTP(ctx, helpers.Request{Method: "GET", BaseURL: strings.TrimSuffix(c.Endpoint, "/"), Path: "/api/diagnostics/system/system_information", User: key, Password: secretValue, NoRedirect: true})
			if err != nil {
				return &helpers.ManualError{Hint: opnsenseIdentityRecovery}
			}
			switch {
			case resp.Status == http.StatusUnauthorized:
				return &helpers.ManualError{Hint: opnsenseRejectedRecovery}
			case resp.Status == http.StatusForbidden:
				return &helpers.ManualError{Hint: opnsenseForbiddenRecovery}
			case resp.Status < 200 || resp.Status > 299:
				return &helpers.ManualError{Hint: opnsenseIdentityRecovery}
			}
			var info map[string]any
			if resp.Decode(&info) != nil || stringField(info, "name") == "" {
				return &helpers.ManualError{Hint: opnsenseIdentityRecovery}
			}
			versions, _ := info["versions"].([]any)
			if len(versions) == 0 {
				return &helpers.ManualError{Hint: opnsenseIdentityRecovery}
			}
			if version, _ := versions[0].(string); version == "" {
				return &helpers.ManualError{Hint: opnsenseIdentityRecovery}
			}
			return nil
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.firewall.getFirewallCpuStatus", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: opnsenseDataRecovery}
			}
			return opnsenseValidateNative(value, integrationID)
		},
		stats: opnsenseValidateStats,
	}, nil
}

func opnsensePercent(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && number <= 100
}

func opnsenseValidateNative(value any, integrationID string) error {
	invalid := func() error { return &helpers.ManualError{Hint: opnsenseDataRecovery} }
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return invalid()
	}
	result, ok := results[0].(map[string]any)
	if !ok || result["error"] != nil && result["error"] != false {
		return invalid()
	}
	integration, ok := result["integration"].(map[string]any)
	if !ok || stringField(integration, "id") != integrationID || stringField(integration, "kind") != "opnsense" {
		return invalid()
	}
	if updatedAt, ok := integration["updatedAt"]; ok && updatedAt == nil {
		return invalid()
	}
	summary, ok := result["summary"].(map[string]any)
	if !ok {
		return invalid()
	}
	if _, ok := opnsensePercent(summary["total"]); !ok {
		return invalid()
	}
	return nil
}

func opnsenseValidateStats(sample statsSample) error {
	_, valid := opnsensePercent(sample.values["cpu"])
	retry, retryOK := sample.retryAt.(float64)
	if sample.units["cpu"] != "percent" || !valid || !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt < sample.startedAt || sample.updatedAt > sample.completedAt+30_000 {
		return &helpers.ManualError{Hint: "OPNsense Stats require a measured CPU percent from 0 to 100, percent units and one successful freshly advancing refresh with valid clock and retry metadata. " + opnsenseRecoveryTail}
	}
	return nil
}
