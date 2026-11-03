package homarr

import (
	"context"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const proxmoxTransportHold = "Proxmox native transport is held before credential forwarding or integration creation: the audited Homarr Proxmox client follows redirects, so a successful CLI no-redirect probe cannot establish native confinement. Preserve this connection and its token. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not use a proxy, disable TLS verification, enroll certificates, rotate credentials or change Proxmox to bypass this hold."
const proxmoxDataRecovery = "Proxmox's selected existing node and read-only cluster data are not verified. Preserve its token and selection; check the dedicated pve user's and token's effective audit permissions, trusted TLS and the selected node, then retry self-host setup homarr after the native transport hold is resolved. Missing or defaulted metrics are not measured zeros."

var proxmoxComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var proxmoxTokenSecret = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func proxmoxExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "proxmox" {
		return target{}, errors.New("the Proxmox external adapter requires the exact proxmox kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("Proxmox requires an approved trusted-HTTPS origin without embedded credentials, API/proxy paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, errors.New("the private Proxmox endpoint has an invalid port")
		}
	}
	values := map[string]string{}
	for _, credential := range c.Credentials {
		if _, duplicate := values[credential.Kind]; duplicate || credential.Value == "" {
			return target{}, errors.New("Proxmox requires exactly one nonempty username, tokenId, apiKey and realm credential")
		}
		switch credential.Kind {
		case "username", "tokenId", "apiKey", "realm":
			values[credential.Kind] = credential.Value
		default:
			return target{}, errors.New("Proxmox supports only the username, tokenId, apiKey and realm credential alternative")
		}
	}
	if len(values) != 4 || !proxmoxComponent.MatchString(values["username"]) || !proxmoxComponent.MatchString(values["tokenId"]) || values["realm"] != "pve" || !proxmoxTokenSecret.MatchString(values["apiKey"]) {
		return target{}, errors.New("Proxmox requires a dedicated pve-realm user, separate token ID and lowercase UUID token secret; supply them only through the owner-only private input")
	}
	if len(c.Selection) != 2 || c.Selection["scope"] != "cluster" || !proxmoxComponent.MatchString(c.Selection["node"]) {
		return target{}, errors.New("Proxmox requires an explicit cluster scope and existing node identity anchor; the native widget fetches token-visible cluster data, not an isolated single-node view")
	}
	return target{
		name: "Proxmox", kind: "proxmox",
		widgets: []widget{
			{kind: "healthMonitoring", options: map[string]any{
				"defaultTab": "cluster", "visibleClusterSections": []string{"node"},
				"cpu": true, "memory": true, "showUptime": true, "gpu": false, "fileSystem": false,
			}},
			statWidget("nodes"),
		},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: proxmoxTransportHold}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			value, err := apiKeyProcedure(ctx, from, "widget.healthMonitoring.getClusterHealthStatus", map[string]any{"integrationId": integrationID})
			if err != nil {
				return &helpers.ManualError{Hint: proxmoxDataRecovery}
			}
			return proxmoxValidateNative(value, c.Selection["node"])
		},
		stats: proxmoxValidateStats,
	}, nil
}

func proxmoxMetric(value any, integral bool) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && (!integral || number == math.Trunc(number) && number <= 1<<53-1)
}

func proxmoxValidateNative(value any, selectedNode string) error {
	invalid := func() error { return &helpers.ManualError{Hint: proxmoxDataRecovery} }
	cluster, ok := value.(map[string]any)
	if !ok || cluster["error"] != nil && cluster["error"] != false || cluster["stale"] != nil && cluster["stale"] != false {
		return invalid()
	}
	for _, key := range []string{"nodes", "vms", "lxcs", "storages"} {
		if _, ok := cluster[key].([]any); !ok {
			return invalid()
		}
	}
	nodes := cluster["nodes"].([]any)
	if len(nodes) == 0 {
		return invalid()
	}
	selected := false
	seen := map[string]bool{}
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		name := stringField(node, "name")
		if !ok || name == "" || seen[name] || stringField(node, "node") != name || stringField(node, "id") != "node/"+name || stringField(node, "type") != "node" {
			return invalid()
		}
		seen[name] = true
		if stringField(node, "status") == "offline" && node["isRunning"] == false {
			continue
		}
		if stringField(node, "status") != "online" || node["isRunning"] != true {
			return invalid()
		}
		cpu, _ := node["cpu"].(map[string]any)
		memory, _ := node["memory"].(map[string]any)
		usage, usageOK := proxmoxMetric(cpu["utilization"], false)
		cores, coresOK := proxmoxMetric(cpu["cores"], true)
		used, usedOK := proxmoxMetric(memory["used"], true)
		total, totalOK := proxmoxMetric(memory["total"], true)
		_, uptimeOK := proxmoxMetric(node["uptime"], true)
		if !usageOK || usage > 1 || !coresOK || cores == 0 || !usedOK || !totalOK || total == 0 || used > total || !uptimeOK {
			return invalid()
		}
		if name == selectedNode {
			selected = true
		}
	}
	if !selected {
		return invalid()
	}
	return nil
}

func proxmoxValidateStats(sample statsSample) error {
	count, valid := proxmoxMetric(sample.values["nodes"], true)
	retry, retryOK := sample.retryAt.(float64)
	if sample.units["nodes"] != "count" || !valid || count == 0 || !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return &helpers.ManualError{Hint: "Proxmox Stats require a positive measured node count, count units and one successful genuinely advancing refresh with valid clock/retry metadata. Preserve the existing token and retry Homarr Setup after the native transport hold is resolved; do not invent nodes or use cached/no-op data as acceptance."}
	}
	return nil
}
