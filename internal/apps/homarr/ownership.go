package homarr

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

func readOwnership[T bool | string](h *helpers.H, key, label string) (map[string]T, error) {
	ids := map[string]T{}
	raw := h.Secret(key)
	if raw == "" {
		return ids, nil
	}
	invalid := func() error {
		hint := fmt.Sprintf("invalid Homarr %s; review %s through the Project secret boundary as a JSON object of confirmed ownership, preserving existing resources, then run self-host setup homarr", label, key)
		h.Info(hint)
		return fmt.Errorf("%s: %w", hint, helpers.ErrManual)
	}
	var recorded map[string]*T
	if err := json.Unmarshal([]byte(raw), &recorded); err != nil || recorded == nil {
		return nil, invalid()
	}
	for id, value := range recorded {
		if strings.TrimSpace(id) == "" || value == nil || any(*value) == "" {
			return nil, invalid()
		}
		ids[id] = *value
	}
	return ids, nil
}

func ownedWidgets(h *helpers.H, integrationID string) (map[string]bool, error) {
	return readOwnership[bool](h, h.Key("widgets_"+integrationID), "widget ownership")
}

func managedAppIDs(h *helpers.H) (map[string]string, error) {
	return readOwnership[string](h, h.Key("app_ids"), "App ownership")
}

func ownedIntegrationIDs(h *helpers.H) ([]string, error) {
	ids := []string{}
	seen := map[string]bool{}
	add := func(key string) {
		if id := h.Secret(h.Key(key)); id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for _, t := range targets() {
		add("linked_" + t.id)
	}
	if h.ExternalConnections != nil {
		connections, err := h.ExternalConnections()
		if err != nil {
			return nil, err
		}
		for _, c := range connections {
			add("linked_external-" + c.ID)
		}
	}
	return ids, nil
}

func validateOwnership(h *helpers.H) error {
	if _, err := managedAppIDs(h); err != nil {
		return err
	}
	for _, t := range targets() {
		id := h.Secret(h.Key("linked_" + t.id))
		if id == "" {
			continue
		}
		if _, err := ownedWidgets(h, id); err != nil {
			return err
		}
		if t.id == "beszel" {
			if _, err := beszelBindings(h, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBoardOwnership(h *helpers.H, current map[string]any) error {
	for _, item := range itemsOf(current) {
		ids, _ := item["integrationIds"].([]any)
		for _, raw := range ids {
			id, _ := raw.(string)
			if id == "" {
				continue
			}
			if _, err := ownedWidgets(h, id); err != nil {
				return err
			}
		}
	}
	return nil
}
