package homarr

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const coolifyTransportHold = "Coolify native transport is held before credential forwarding or integration creation: the audited Homarr Coolify client's redirect and trusted-TLS behavior is not established, so a successful CLI no-redirect probe cannot establish native confinement. Preserve this connection and its token. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not use a proxy, disable TLS verification, enroll certificates, rotate credentials or change Coolify to bypass this hold."
const coolifyDataRecovery = "Coolify's read-only team inventory is not verified. Preserve its token; confirm API access is enabled, this host's IP is allowed, TLS is trusted and the read token covers the whole intended team (servers and team routes are not available to project-scoped tokens), then retry self-host setup homarr. Missing or defaulted counts are not measured zeros."
const coolifyStatsRecovery = "Coolify Stats require explicit applications, services and servers counts (zeros allowed), count units and one successful genuinely advancing refresh with valid clock/retry metadata. Preserve the existing token and retry Homarr Setup; do not invent counts or use cached/no-op data as acceptance."

const coolifyMaxAge = 30 * time.Second
const coolifyFutureSkew = 5 * time.Second

var coolifyInventory = []string{"applications", "services", "servers"}

func coolifyExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "coolify" {
		return target{}, errors.New("the Coolify external adapter requires the exact coolify kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("Coolify requires an approved trusted-HTTPS root origin without embedded credentials, /api paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, errors.New("the private Coolify endpoint has an invalid port")
		}
	}
	if len(c.Credentials) != 1 || c.Credentials[0].Kind != "apiKey" || strings.TrimSpace(c.Credentials[0].Value) == "" || c.Credentials[0].Value != strings.TrimSpace(c.Credentials[0].Value) {
		return target{}, errors.New("Coolify requires exactly one nonempty apiKey credential: a dedicated read-only team token supplied only through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("Coolify takes no selection; the native widget reads the whole token-visible team")
	}
	return target{
		name: "Coolify", kind: "coolify",
		widgets: []widget{
			{kind: "coolify", options: map[string]any{"showServers": true, "showApplications": true, "showServices": true}},
			statWidget(coolifyInventory...),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: coolifyTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.coolify.getInstancesInfo", map[string]any{"integrationIds": []string{integrationID}})
			if err != nil {
				return &helpers.ManualError{Hint: coolifyDataRecovery}
			}
			return coolifyValidateNative(value, integrationID, c.Endpoint, time.Now())
		},
		stats: coolifyValidateStats,
	}, nil
}

func coolifyTimestamp(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		return t, err == nil
	case float64:
		if !validStatsTimestamp(v) || v != float64(int64(v)) {
			return time.Time{}, false
		}
		return time.UnixMilli(int64(v)), true
	}
	return time.Time{}, false
}

func coolifyValidateNative(value any, integrationID, endpoint string, now time.Time) error {
	invalid := func() error { return &helpers.ManualError{Hint: coolifyDataRecovery} }
	list, ok := value.([]any)
	if !ok || len(list) != 1 {
		return invalid()
	}
	entry, ok := list[0].(map[string]any)
	if !ok || stringField(entry, "integrationId") != integrationID || strings.TrimRight(stringField(entry, "integrationUrl"), "/") != strings.TrimRight(endpoint, "/") || entry["error"] != nil && entry["error"] != false {
		return invalid()
	}
	updated, ok := coolifyTimestamp(entry["updatedAt"])
	if !ok || now.Sub(updated) > coolifyMaxAge || updated.Sub(now) > coolifyFutureSkew {
		return invalid()
	}
	info, ok := entry["instanceInfo"].(map[string]any)
	if !ok || strings.TrimSpace(stringField(info, "version")) == "" {
		return invalid()
	}
	for _, key := range coolifyInventory {
		items, ok := info[key].([]any)
		if !ok {
			return invalid()
		}
		seen := map[string]bool{}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			uuid := stringField(item, "uuid")
			if !ok || uuid == "" || stringField(item, "name") == "" || seen[uuid] {
				return invalid()
			}
			seen[uuid] = true
		}
	}
	return nil
}

func coolifyValidateStats(sample statsSample) error {
	retry, retryOK := sample.retryAt.(float64)
	valid := retryOK && retry == 0 && validStatsTimestamp(sample.updatedAt) && sample.updatedAt == float64(int64(sample.updatedAt)) && sample.updatedAt <= sample.completedAt+5000 && sample.updatedAt >= sample.startedAt-60000
	for _, key := range coolifyInventory {
		_, countOK := measuredNumber(sample.values[key], true)
		valid = valid && countOK && sample.units[key] == "count"
	}
	if !valid {
		return &helpers.ManualError{Hint: coolifyStatsRecovery}
	}
	return nil
}
