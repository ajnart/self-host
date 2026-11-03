package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ajnart/self-host/internal/apps/beszel"
	"github.com/ajnart/self-host/internal/helpers"
)

const beszelRecoveryHint = "Run self-host setup beszel to verify the Hub login and this machine's enrolled, online agent, then run self-host setup homarr. Keep the existing read-only socket proxy. A changed System Stats selection needs review of only the owned widget in Homarr: select this machine while preserving its ID, layout, time period and chart choices. Missing prior managed-binding evidence is not permission to reset a person's selection or create a duplicate integration."

func beszelTarget() target {
	return target{
		id: "beszel", name: "Beszel", kind: "beszel", internal: (*helpers.H).InternalURL,
		recovery: beszelRecoveryHint,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "username", Value: beszel.AdminEmail(to)}, {Kind: "password", Value: to.AdminPassword()}}
		},
		widgets: []widget{beszelSystemStats()},
	}
}

func beszelSystemStats() widget {
	return widget{kind: "beszelSystemStats", bind: func(ctx context.Context, to *helpers.H, integrationID string) (map[string]any, error) {
		id, err := beszel.MachineID(ctx, to)
		if err != nil {
			return nil, err
		}
		options := map[string]any{"systemId": integrationID + ":" + id, "timePeriod": "1h"}
		for _, key := range []string{"showCpu", "showMemory", "showDisk", "showDiskIO", "showNetwork", "showGpuUsage", "showGpuMemory", "showGpuPower", "showDockerCpu", "showDockerMemory", "showDockerNetwork"} {
			options[key] = true
		}
		return options, nil
	}}
}

func beszelBindings(h *helpers.H, integrationID string) (map[string]string, error) {
	return readOwnership[string](h, h.Key("beszel_bindings_"+integrationID), "Beszel managed bindings")
}

func rememberBeszelBinding(h *helpers.H, integrationID, widgetID, systemID string) error {
	if systemID == "" || !strings.HasPrefix(systemID, integrationID+":") {
		return errors.New("Homarr did not return a valid managed Beszel system selection; run self-host setup beszel, then self-host setup homarr")
	}
	bindings, err := beszelBindings(h, integrationID)
	if err != nil {
		return err
	}
	bindings[widgetID] = systemID
	raw, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	return h.SetSecret(h.Key("beszel_bindings_"+integrationID), string(raw))
}

func reconcileBeszelSystemStats(ctx context.Context, from, to *helpers.H, current map[string]any, integrationID string, apply bool) (bool, error) {
	if integrationID == "" {
		return true, nil
	}
	owned, err := ownedWidgets(from, integrationID)
	if err != nil {
		return false, err
	}
	bindings, err := beszelBindings(from, integrationID)
	if err != nil {
		return false, err
	}
	updates := []map[string]any{}
	expected := ""
	for _, item := range itemsOf(current) {
		id := stringField(item, "id")
		if !owned[id] || !wiredTo(item, "beszelSystemStats", integrationID) {
			continue
		}
		if expected == "" {
			machineID, err := beszel.MachineID(ctx, to)
			if err != nil {
				return false, err
			}
			expected = integrationID + ":" + machineID
		}
		options, _ := item["options"].(map[string]any)
		selected := stringField(options, "systemId")
		if selected == expected {
			continue
		}
		if selected == "" || bindings[id] != selected || !strings.HasPrefix(selected, integrationID+":") {
			from.Info(beszelRecoveryHint)
			return false, helpers.ErrManual
		}
		updates = append(updates, item)
	}
	if len(updates) == 0 {
		return true, nil
	}
	if !apply {
		return false, nil
	}
	for _, item := range updates {
		options := item["options"].(map[string]any)
		options["systemId"] = expected
		bindings[stringField(item, "id")] = expected
	}
	if err := saveItems(ctx, from, current, itemsOf(current)); err != nil {
		return false, err
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return false, err
	}
	if err := from.SetSecret(from.Key("beszel_bindings_"+integrationID), string(raw)); err != nil {
		return false, err
	}
	return true, nil
}
