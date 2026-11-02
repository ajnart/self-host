package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

const externalConnectionRecovery = "Review this connection's owner-only private input with self-host connections update <id> --file PATH --yes, then run self-host setup homarr. Keep its kind and endpoint unchanged; reconnect by removing and adding a new connection. Never bypass TLS or replace credentials automatically."
const externalConnectionRemovalRecovery = "External removal is pending. Restore only Homarr's API and its recorded ownership, then retry self-host setup homarr. No external target credentials, reachability, Setup or lifecycle changes are required. Preserve matching user integrations and widgets; never clear ownership to bypass recovery."

func externalConnectionDescriptor(c helpers.ExternalConnection) (target, error) {
	t, err := externalTarget(c)
	if err != nil {
		return target{}, err
	}
	if t.kind != c.Kind || t.name == "" || t.preflight == nil {
		return target{}, &helpers.ManualError{Hint: "The external adapter does not establish its exact kind and fail-closed native prerequisites. Preserve the connection and retry only after an audited adapter repair."}
	}
	t.id = "external-" + c.ID
	t.integrationOnly = true
	t.internal = fixed(c.Endpoint)
	t.secrets = func(_, _ *helpers.H) []secret {
		credentials := make([]secret, 0, len(c.Credentials))
		for _, credential := range c.Credentials {
			credentials = append(credentials, secret{Kind: credential.Kind, Value: credential.Value})
		}
		return credentials
	}
	return t, nil
}

func (App) ValidateExternalConnection(c helpers.ExternalConnection) error {
	_, err := externalConnectionDescriptor(c)
	return err
}

func (App) SetupExternalConnections(ctx context.Context, h *helpers.H) ([]string, error) {
	if h.ExternalConnections == nil {
		return nil, nil
	}
	connections, err := h.ExternalConnections()
	if err != nil {
		return nil, h.Step(ctx, "link-external-connections", "review external connection ownership", helpers.StepSpec{
			Do: func(context.Context) error { return err }, Hint: externalConnectionRecovery,
		})
	}
	removed := []string{}
	var failures []error
	for _, c := range connections {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if c.Removing {
			t := target{id: "external-" + c.ID, name: c.Kind, kind: c.Kind, internal: fixed(""), integrationOnly: true}
			err := t.link().Remove(ctx, h, nil)
			if err == nil {
				removed = append(removed, c.ID)
			}
			failures = append(failures, err)
			continue
		}
		t, err := externalConnectionDescriptor(c)
		if err != nil {
			manual := &helpers.ManualError{Hint: externalConnectionRecovery + " The saved kind, credential shape or selected-data prerequisites need review."}
			err = h.Step(ctx, "link-external-"+c.ID, "review external connection prerequisites", helpers.StepSpec{
				Do: func(context.Context) error { return manual }, Hint: externalConnectionRecovery,
			})
		} else {
			err = t.link().Apply(ctx, h, nil)
		}
		failures = append(failures, err)
	}
	return removed, errors.Join(failures...)
}

func (t target) releaseExternalConnection(h *helpers.H) error {
	id := h.Secret(h.Key("linked_" + t.id))
	if id != "" {
		if err := h.SetSecret(h.Key("widgets_"+id), ""); err != nil {
			return err
		}
		if err := h.SetSecret(integrationFingerprintKey(h, id), ""); err != nil {
			return err
		}
	}
	return h.SetSecret(h.Key("linked_"+t.id), "")
}

func (t target) externalRemovalReady(ctx context.Context, h *helpers.H) error {
	id := h.Secret(h.Key("linked_" + t.id))
	if id == "" {
		return nil
	}
	if h.Secret(h.Key("widgets_"+id)) == "" {
		return &helpers.ManualError{Hint: "The external integration is recorded but its widget ownership is missing. Review its confirmed owned widget IDs through the Project secret boundary ({} preserves existing widgets), then retry self-host setup homarr. Do not infer ownership from matching endpoints."}
	}
	integrations, err := listIntegrations(ctx, h)
	if err != nil {
		return err
	}
	for _, integration := range integrations {
		if stringField(integration, "id") == id && stringField(integration, "kind") != t.kind {
			return &helpers.ManualError{Hint: "The recorded external integration has a different kind. Preserve its resources and repair explicit ownership through the Project secret boundary before retrying removal."}
		}
	}
	return nil
}
