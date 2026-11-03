// Package fileflows is FileFlows: Setup only proves the web console answers;
// the EULA, security, libraries, flows and licence stay with the owner.
package fileflows

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

// App is FileFlows. Traps: docs/catalog/traps/fileflows.md.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "fileflows", Name: "FileFlows", Category: "Media",
		Description:   "Process and convert files with flows you control",
		Image:         "revenz/fileflows:stable",
		Docs:          "https://fileflows.com/docs/installation/docker/",
		Port:          19200,
		ContainerPort: 5000,
		Service:       "fileflows",
	}
}

func (App) Inputs() []app.Input { return nil }

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  fileflows:
    image: revenz/fileflows:stable
    restart: unless-stopped
    environment:
      PUID: ${PUID}
      PGID: ${PGID}
      TZ: ${TZ}
    volumes:
      - ./data:/app/Data
      - ./logs:/app/Logs
      - ./temp:/temp
    ports:
      - ${BIND_IP}:` + port + `:5000
`
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "After the first start, review and accept the EULA on the first-run page, then set up users and security before exposing it."
}

func setup() firstadmin.NoAccount {
	return firstadmin.NoAccount{Probe: firstadmin.Probe{Path: "/"}}
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup().Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup().Check(ctx, h) }
