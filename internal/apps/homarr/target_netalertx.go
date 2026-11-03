package homarr

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const netalertxRecovery = "NetAlertX's device totals are not verified. Preserve this connection and its API token; check that the endpoint is the NetAlertX backend API (default port 20212, not the web UI on 20211) and that its API_TOKEN is current, then run self-host connections update <id> --file PATH --yes and retry self-host setup homarr. NetAlertX has one global API_TOKEN with no read-only scope; no scan, discovery or device action is performed. An invalid token may raise an alert in NetAlertX."

var netalertxStatsKeys = []string{"total", "connected", "newDevices", "downAlerts"}

func netalertxExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "netalertx" {
		return target{}, errors.New("the NetAlertX external adapter requires the exact netalertx kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("NetAlertX requires an approved backend API origin without embedded credentials, path, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, errors.New("the private NetAlertX endpoint has an invalid port")
		}
	}
	if len(c.Credentials) != 1 || c.Credentials[0].Kind != "apiKey" || c.Credentials[0].Value == "" {
		return target{}, errors.New("NetAlertX requires exactly one nonempty apiKey credential; supply it only through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("NetAlertX takes no selection; its Stats are instance-wide totals")
	}
	return target{
		name: "NetAlertX", kind: "netalertx",
		widgets:   []widget{statWidget(netalertxStatsKeys...)},
		preflight: func(context.Context, *helpers.H, *helpers.H) error { return nil },
		verify:    verifyNetalertx,
		stats:     verifyNetalertxStats,
	}, nil
}

func verifyNetalertx(ctx context.Context, from, _ *helpers.H, integrationID string) error {
	value, err := apiKeyWrite(ctx, from, "integration.request", map[string]any{
		"integrationId": integrationID, "method": "GET", "path": "/devices/totals",
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &helpers.ManualError{Hint: netalertxRecovery}
	}
	result, _ := value.(map[string]any)
	integration, _ := result["integration"].(map[string]any)
	data, _ := result["data"].(map[string]any)
	totals, _ := data["response"].([]any)
	if result["ok"] != true || result["status"] != float64(200) || integration["id"] != integrationID || integration["kind"] != "netalertx" || len(totals) < 5 {
		return &helpers.ManualError{Hint: netalertxRecovery}
	}
	for _, index := range []int{0, 1, 3, 4} {
		if !isCount(totals[index]) {
			return &helpers.ManualError{Hint: netalertxRecovery}
		}
	}
	return nil
}

func verifyNetalertxStats(sample statsSample) error {
	if !validStatsTimestamp(sample.updatedAt) {
		return &helpers.ManualError{Hint: netalertxRecovery}
	}
	for _, key := range netalertxStatsKeys {
		if sample.units[key] != "count" || !isCount(sample.values[key]) {
			return &helpers.ManualError{Hint: netalertxRecovery}
		}
	}
	return nil
}
