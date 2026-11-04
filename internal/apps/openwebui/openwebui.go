// Package openwebui is Open WebUI, a private chat front end. The first
// administrator is seeded from the Admin email and password on an empty user
// database.
package openwebui

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Open WebUI. Traps: docs/catalog/traps/open-webui.md.
type App struct{ app.Defaults }

func (App) AdminEmail(username string) string { return setup.AdminEmail(username) }

func (App) Info() app.Info {
	return app.Info{
		ID: "open-webui", Name: "Open WebUI", Category: "Local AI",
		Description:   "Chat with AI models running on your own computer, in your browser",
		Image:         "ghcr.io/open-webui/open-webui:latest",
		Docs:          "https://docs.openwebui.com/getting-started/quick-start/",
		Port:          8080,
		ContainerPort: 8080,
		Service:       "open-webui",
	}
}

// Secrets declares the persistent session key that keeps sign-ins valid across
// restarts.
func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "secret_key", Length: 64, Hex: true}}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  open-webui:
    image: ghcr.io/open-webui/open-webui:latest
    restart: unless-stopped
    environment:
      WEBUI_ADMIN_EMAIL: ${ADMIN_USER}@selfhost.local
      WEBUI_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      WEBUI_SECRET_KEY: ${OPEN_WEBUI_SECRET_KEY}
      OLLAMA_BASE_URL: http://ollama:11434
    volumes:
      - ./appdata:/app/backend/data
    ports:
      - ${BIND_IP}:` + port + `:8080
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "Open WebUI",
	Probe: setupspec.Probe{Path: "/health", Timeout: "3m"},
	Email: "{user}@selfhost.local",
	Login: []setupspec.Call{{
		Path:    "/api/v1/auths/signin",
		JSON:    map[string]any{"email": "{email}", "password": "{password}"},
		Ok:      []int{200},
		Lenient: true,
		Assert: []setupspec.Assert{
			{Path: "token", Present: true},
			{Path: "email", Equals: "{email}"},
			{Path: "role", Equals: "admin"},
		},
	}},
	StepID:      "admin-login",
	Title:       "verify the administrator {email}",
	CreateError: "Open WebUI creates the first administrator from WEBUI_ADMIN_EMAIL/WEBUI_ADMIN_PASSWORD only when the user database is empty",
	Hint: "Open WebUI seeds the first administrator only on an empty database. Recreate the container with an empty " +
		"data volume (self-host start open-webui), then open {url} and sign in as {email} with the password from `self-host credentials`.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

// Check verifies the administrator login only; model provider and API-key
// access stay separate acceptance steps.
func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) StandaloneGuide() string {
	return "Sign in with the email made of the ADMIN_USER value and @selfhost.local, and the ADMIN_PASSWORD value."
}
