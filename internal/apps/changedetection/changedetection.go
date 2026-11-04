package changedetection

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "changedetection", Name: "Changedetection.io", Category: "Monitoring",
		Description:   "Watch web pages for changes and receive notifications",
		Image:         "ghcr.io/dgtlmoon/changedetection.io:latest",
		Docs:          "https://github.com/dgtlmoon/changedetection.io#installation",
		Port:          5050,
		ContainerPort: 5000,
		Service:       "changedetection",
		WebSockets:    true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{
		Key: "api_key", Prompt: "Existing Changedetection.io API key", Private: true, RecoveryOnly: true,
		Help: "Restore the existing instance key through the Project secret boundary. It is write-capable, not a read-only scoped key. Setup never regenerates it. This input does not release the startup hold.",
	}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  changedetection:
    image: ghcr.io/dgtlmoon/changedetection.io:latest
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      DISABLE_VERSION_CHECK: "true"
    volumes:
      - ./data:/datastore
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:5000
`
}

const startupHint = "Changedetection.io startup is held because the supported image creates external demo watches on a fresh datastore. Do not start it or delete matching watches. An approved upstream-supported no-demo bootstrap is required before managed installation, start, restart, update or restore; existing files or a supplied key do not release this hold."

const keyHint = "Inspect Changedetection.io Settings > API privately and restore only the existing instance key as CHANGEDETECTION_API_KEY through the Project secret boundary, then run self-host setup changedetection. Setup does not generate, reset or replace API keys."

const passwordHint = "Verify privately that Changedetection.io already requires and accepts the shared Admin password, then run self-host setup changedetection. An unknown existing password needs a separately approved recovery plan; do not reset it, change the shared Admin login or disable protection to bypass verification."

func (App) Preflight(ctx context.Context, _ *helpers.H) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &helpers.ManualError{Hint: startupHint}
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	return h.Step(ctx, "credentials", "verify the existing Changedetection.io login and API data", helpers.StepSpec{
		Do:         func(ctx context.Context) error { return verify(ctx, h) },
		Hint:       keyHint + " " + passwordHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return verify(ctx, h)
}

func (App) StandaloneGuide() string {
	return "Do not start this Compose on a fresh datastore: the official image creates and fetches external demo watches. Managed startup remains held until an approved upstream-supported no-demo bootstrap exists. Do not delete matching watches or edit the datastore to bypass this hold. Keep the complete /datastore private and persistent. Existing-instance verification requires password protection and the existing write-capable API key from Settings > API; Setup does not create or reset either credential. A browser backend is optional for JavaScript watches, not required for API statistics."
}
