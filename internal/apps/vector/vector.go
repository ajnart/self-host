// Package vector is Vector: a private, self-only telemetry pipeline whose API
// only proves it is initialized and serving, not that logs flow anywhere.
package vector

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

// App is Vector. Traps: docs/catalog/traps/vector.md.
type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "vector", Name: "Vector", Category: "Observability",
		Description:   "Collect your apps' activity messages and send them where you choose",
		Image:         "ghcr.io/vectordotdev/vector:latest-debian",
		Docs:          "https://vector.dev/docs/setup/installation/platforms/docker/",
		Port:          8686,
		ContainerPort: 8686,
		Service:       "vector",
	}
}

const config = `api:
  enabled: true
  address: 0.0.0.0:8686
sources:
  vector_logs:
    type: internal_logs
sinks:
  console:
    type: console
    inputs:
      - vector_logs
    target: stdout
    encoding:
      codec: text
`

func (App) Files(app.RenderContext, func(string) string) []app.File {
	return []app.File{{Path: "config", Content: config}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  vector:
    image: ghcr.io/vectordotdev/vector:latest-debian
    restart: unless-stopped
    volumes:
      - ./config:/etc/vector/vector.yaml
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8686
`
}

var probe = firstadmin.NoAccount{Probe: firstadmin.Probe{Path: "/health", Contains: `"ok":true`}}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := probe.Setup(ctx, h); err != nil {
		return err
	}
	h.Info("Vector's starter configuration sends only its own internal_logs to the console. It collects no other App or host logs and sends nothing to Loki. Review apps/vector/config before adding sources or destinations; API health alone does not prove log delivery.")
	return nil
}

func (App) Check(ctx context.Context, h *helpers.H) error { return probe.Check(ctx, h) }

func (App) StandaloneGuide() string {
	return "The starter configuration sends only Vector's own internal_logs to the console; it collects no other App or host logs and sends nothing to Loki. Review the configuration mounted at /etc/vector/vector.yaml before adding sources or destinations. A healthy API does not prove log delivery."
}
