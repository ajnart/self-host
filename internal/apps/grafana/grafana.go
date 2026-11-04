// Package grafana is the Grafana dashboarding and alerting server. The
// administrator is seeded by the image on first start from the Admin login.
package grafana

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Grafana. Traps: docs/catalog/traps/grafana.md.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "grafana", Name: "Grafana", Category: "Observability",
		Description:   "Turn numbers about your computers and apps into charts and alerts",
		Image:         "grafana/grafana:latest",
		Docs:          "https://grafana.com/docs/grafana/latest/setup-grafana/installation/docker/",
		Port:          3000,
		ContainerPort: 3000,
		Service:       "grafana",
	}
}

func (App) Inputs() []app.Input { return nil }

// Compose runs Grafana as the configured owner of its persisted data directory.
// The Admin login is seeded through environment references on first start.
func (App) Files(app.RenderContext, func(string) string) []app.File {
	return []app.File{{Path: "data/.keep", Mode: 0o644}}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  grafana:
    image: grafana/grafana:latest
    restart: unless-stopped
    user: ${PUID}:${PGID}
    environment:
      GF_SECURITY_ADMIN_USER: ${ADMIN_USER}
      GF_SECURITY_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ./data:/var/lib/grafana
    ports:
      - ${BIND_IP}:` + port + `:3000
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "Grafana",
	Probe: setupspec.Probe{Path: "/api/health", Timeout: "3m"},
	Login: []setupspec.Call{{
		Path:  "/api/user",
		Basic: true,
		Assert: []setupspec.Assert{
			{Path: "login", Equals: "{user}"},
			{Path: "isGrafanaAdmin", Equals: "true"},
			{Path: "isDisabled", Equals: "false"},
		},
	}},
	StepID:      "admin-login",
	Title:       "verify the administrator {user}",
	CreateError: "Grafana reads GF_SECURITY_ADMIN_USER and GF_SECURITY_ADMIN_PASSWORD only on first start",
	Hint: "Grafana applies the Admin login on first start. Recreate the container so it reads .env: " +
		"self-host start grafana, then sign in at {url} with the password from `self-host credentials`.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) Links() []app.Link { return nil }
