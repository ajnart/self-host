package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const wazuhTransportHold = "Wazuh native transport is held before integration creation: the Wazuh API source and a self-host no-redirect probe were checked, but the audited Homarr Wazuh adapter's redirect and trusted-TLS behavior is not established, so a successful probe cannot confine native credential forwarding. Preserve this connection and its credentials. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not use a proxy, disable TLS verification, enroll certificates, rotate credentials or change Wazuh to bypass this hold."
const wazuhSourceRecovery = "Wazuh's server API agent summary could not be verified. Preserve the connection; confirm the HTTPS server API endpoint (default port 55000, not the indexer or dashboard), the dedicated account's agent:read access to agent:id:* and trusted TLS, then retry self-host setup homarr. Missing or defaulted counters are not measured zeros."

func wazuhExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "wazuh" {
		return target{}, errors.New("the Wazuh external adapter requires the exact wazuh kind")
	}
	if err := externalHTTPSOrigin(c.Endpoint, "Wazuh"); err != nil {
		return target{}, err
	}
	values := map[string]string{}
	for _, credential := range c.Credentials {
		if _, duplicate := values[credential.Kind]; duplicate || credential.Value == "" {
			return target{}, errors.New("Wazuh requires exactly one nonempty username and password credential")
		}
		switch credential.Kind {
		case "username", "password":
			values[credential.Kind] = credential.Value
		default:
			return target{}, errors.New("Wazuh supports only the server API username and password alternative")
		}
	}
	if len(values) != 2 || len(c.Selection) != 0 {
		return target{}, errors.New("Wazuh requires exactly the dedicated agent:read username and password supplied through the owner-only private input, with no selection")
	}
	username, password := values["username"], values["password"]
	return target{
		name: "Wazuh", kind: "wazuh",
		widgets: []widget{statWidget("agents", "activeAgents", "disconnectedAgents")},
		preflight: func(ctx context.Context, from, _ *helpers.H) error {
			if err := wazuhProbe(ctx, from, c.Endpoint, username, password); err != nil {
				return err
			}
			return &helpers.ManualError{Hint: wazuhTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, _ string) error {
			return wazuhProbe(ctx, from, c.Endpoint, username, password)
		},
		stats: wazuhValidateStats,
	}, nil
}

func wazuhProbe(ctx context.Context, h *helpers.H, endpoint, username, password string) error {
	manual := &helpers.ManualError{Hint: wazuhSourceRecovery}
	auth, err := h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/security/user/authenticate?raw=true", BaseURL: endpoint, User: username, Password: password, NoRedirect: true})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	token := strings.TrimSpace(string(auth.Body))
	if err != nil || !auth.OK() || token == "" || strings.ContainsAny(token, "{}\r\n ") {
		return manual
	}
	summary, err := h.HTTP(ctx, helpers.Request{Path: "/agents/summary/status", BaseURL: endpoint, Header: map[string]string{"Authorization": "Bearer " + token}, NoRedirect: true})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil || !summary.OK() || wazuhValidateSummary(summary.Body) != nil {
		return manual
	}
	return nil
}

func wazuhValidateSummary(body []byte) error {
	var raw struct {
		Data struct {
			Connection map[string]any `json:"connection"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return errors.New("invalid Wazuh summary")
	}
	if code, ok := raw.Error.(float64); !ok || code != 0 {
		return errors.New("Wazuh reported an error")
	}
	counts := map[string]float64{}
	for _, key := range []string{"total", "active", "disconnected", "never_connected", "pending"} {
		count, ok := countValue(raw.Data.Connection[key])
		if !ok {
			return errors.New("Wazuh summary counters are missing or invalid")
		}
		counts[key] = count
	}
	if counts["active"]+counts["disconnected"]+counts["never_connected"]+counts["pending"] != counts["total"] {
		return errors.New("Wazuh summary counters are inconsistent")
	}
	return nil
}

func wazuhValidateStats(sample statsSample) error {
	retry, retryOK := sample.retryAt.(float64)
	agents, agentsOK := countValue(sample.values["agents"])
	active, activeOK := countValue(sample.values["activeAgents"])
	disconnected, disconnectedOK := countValue(sample.values["disconnectedAgents"])
	unitsOK := true
	for _, metric := range []string{"agents", "activeAgents", "disconnectedAgents"} {
		unitsOK = unitsOK && sample.units[metric] == "count"
	}
	if !unitsOK || !agentsOK || !activeOK || !disconnectedOK || active+disconnected > agents || !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return &helpers.ManualError{Hint: "Wazuh Stats require measured integral agent counts with count units, active plus disconnected agents no greater than the total, and one successful genuinely advancing refresh with valid clock/retry metadata. Zero is valid only when explicitly measured. Preserve the connection and retry Homarr Setup; do not invent agents or use cached/no-op data as acceptance."}
	}
	return nil
}
