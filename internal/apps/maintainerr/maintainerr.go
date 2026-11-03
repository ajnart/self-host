package maintainerr

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "maintainerr", Name: "Maintainerr", Category: "Media",
		Description:   "Review and manage how long movies and shows stay on your media server",
		Image:         "ghcr.io/maintainerr/maintainerr:latest",
		Docs:          "https://docs.maintainerr.info/installation/",
		Port:          6246,
		ContainerPort: 6246,
		Service:       "maintainerr",
	}
}

func (App) Inputs() []app.Input { return nil }

func (App) Directories(rc app.RenderContext) []app.Directory {
	return []app.Directory{{Path: "data", Mode: 0o700, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  maintainerr:
    image: ghcr.io/maintainerr/maintainerr:latest
    restart: unless-stopped
    user: ${PUID}:${PGID}
    environment:
      TZ: ${TZ}
      TELEMETRY: "off"
    volumes:
      - type: bind
        source: ./data
        target: /opt/data
        bind:
          create_host_path: false
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:6246
`
}

func (a App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/health/ready", helpers.WantStatus(200, 401, 403, 301, 302, 303, 307, 308), helpers.NoRedirect()); err != nil {
		return err
	}
	return h.Step(ctx, "media-server", "verify Maintainerr's configured media server", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := a.Check(ctx, h)
			return err == nil, err
		},
		Hint: serverHint,
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return verifyServer(ctx, h)
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "Before starting, prepare the data bind directory as a private mode-0700 directory writable by the selected runtime user. Maintainerr has no login; keep its Address private or behind existing authentication. Open Services to configure and test your chosen media server. Existing rules, collections and retention are preserved; no cleanup is triggered by self-host. Maintainerr's weekly telemetry is disabled in this definition."
}
