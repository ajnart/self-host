package homarr

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const unifiTransportHold = "UniFi Controller native transport is held before credential forwarding or integration creation: the audited Homarr UniFi client does not confine login or request redirects and falls back between controller flavors and ports, so a successful CLI no-redirect probe cannot establish native confinement. Preserve this connection and its credentials. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect, approved-destination and trusted-TLS behavior. Do not use a proxy, disable TLS verification, bypass MFA, enroll certificates, rotate credentials or change the controller to bypass this hold."
const unifiDataRecovery = "The UniFi Controller's selected site and read-only health data are not verified. Preserve its credentials and selection; check that the dedicated local, non-MFA account sees only the selected site with read-only access and trusted TLS, then retry self-host setup homarr after the native transport hold is resolved. Missing or defaulted metrics are not measured zeros."

var unifiComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func unifiControllerExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "unifiController" {
		return target{}, errors.New("the UniFi Controller external adapter requires the exact unifiController kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("UniFi Controller requires an approved trusted-HTTPS origin without embedded credentials, API/proxy paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, errors.New("the private UniFi Controller endpoint has an invalid port")
		}
	}
	values := map[string]string{}
	for _, credential := range c.Credentials {
		if _, duplicate := values[credential.Kind]; duplicate || credential.Value == "" {
			return target{}, errors.New("UniFi Controller requires exactly one nonempty username and password credential")
		}
		switch credential.Kind {
		case "username", "password":
			values[credential.Kind] = credential.Value
		default:
			return target{}, errors.New("UniFi Controller supports only the username and password credential alternative")
		}
	}
	if len(values) != 2 {
		return target{}, errors.New("UniFi Controller requires a dedicated local, non-MFA read-only account; supply username and password only through the owner-only private input")
	}
	flavor := c.Selection["flavor"]
	if len(c.Selection) != 3 || flavor != "unifiOS" && flavor != "classic" || !unifiComponent.MatchString(c.Selection["siteId"]) || !unifiComponent.MatchString(c.Selection["siteName"]) {
		return target{}, errors.New("UniFi Controller requires an explicit flavor (unifiOS or classic) and one selected site ID and name; the native widget aggregates every site the account can see")
	}
	siteID, siteName := c.Selection["siteId"], c.Selection["siteName"]
	return target{
		name: "UniFi Controller", kind: "unifiController",
		widgets: []widget{
			{kind: "networkControllerStatus", options: map[string]any{"content": "wifi"}},
			statWidget("wifi", "lan", "latency"),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: unifiTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.networkController.summary", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: unifiDataRecovery}
			}
			return unifiValidateNative(value, integrationID, from.Now().UnixMilli(), siteID, siteName)
		},
		stats: unifiValidateStats,
	}, nil
}

func unifiValidateNative(value any, integrationID string, nowMillis int64, siteID, siteName string) error {
	invalid := func() error { return &helpers.ManualError{Hint: unifiDataRecovery} }
	entries, ok := value.([]any)
	if !ok || len(entries) != 1 {
		return invalid()
	}
	entry, ok := entries[0].(map[string]any)
	if !ok || stringField(entry, "integrationId") != integrationID || entry["error"] != nil && entry["error"] != false {
		return invalid()
	}
	integration, _ := entry["integration"].(map[string]any)
	if stringField(integration, "id") != integrationID || stringField(integration, "kind") != "unifiController" {
		return invalid()
	}
	updated, ok := entry["updatedAt"].(float64)
	if !ok || !validStatsTimestamp(updated) || updated > float64(nowMillis)+5000 || updated < float64(nowMillis)-60000 {
		return invalid()
	}
	summary, ok := entry["summary"].(map[string]any)
	if !ok {
		return invalid()
	}
	if reported, present := summary["site"]; present {
		site, _ := reported.(map[string]any)
		if stringField(site, "id") != siteID || stringField(site, "name") != siteName {
			return invalid()
		}
	}
	for _, key := range []string{"wifi", "lan"} {
		subsystem, ok := summary[key].(map[string]any)
		if !ok {
			return invalid()
		}
		for _, field := range []string{"users", "guests"} {
			if _, valid := proxmoxMetric(subsystem[field], true); !valid {
				return invalid()
			}
		}
	}
	www, ok := summary["www"].(map[string]any)
	if !ok {
		return invalid()
	}
	if _, valid := proxmoxMetric(www["latency"], false); !valid {
		return invalid()
	}
	if vpn, ok := summary["vpn"].(map[string]any); !ok {
		return invalid()
	} else if users := vpn["users"]; users != nil {
		if _, valid := proxmoxMetric(users, true); !valid {
			return invalid()
		}
	}
	return nil
}

func unifiValidateStats(sample statsSample) error {
	retry, retryOK := sample.retryAt.(float64)
	valid := sample.units["wifi"] == "count" && sample.units["lan"] == "count" && sample.units["latency"] == "ms"
	for _, metric := range []string{"wifi", "lan", "latency"} {
		_, ok := proxmoxMetric(sample.values[metric], metric != "latency")
		valid = valid && ok
	}
	if !valid || !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return &helpers.ManualError{Hint: "UniFi Controller Stats require finite non-negative measured wifi and lan client counts, a latency in milliseconds and one successful genuinely advancing refresh with valid clock/retry metadata. Preserve the existing credentials and retry Homarr Setup after the native transport hold is resolved; do not invent values or use cached/no-op data as acceptance."}
	}
	return nil
}
