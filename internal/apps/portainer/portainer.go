// Package portainer is Portainer CE: a private container-management UI. The
// owner-admin is preseeded from a generated password file and no Docker socket
// is mounted, so credential preparation is all this App claims.
package portainer

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Portainer. Traps: docs/catalog/traps/portainer.md.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "portainer", Name: "Portainer", Category: "Containers",
		Description:   "A web page for managing apps; you give it access to your apps yourself",
		Image:         "docker.io/portainer/portainer-ce:latest",
		Docs:          "https://docs.portainer.io/start/install-ce/server/docker/linux",
		Port:          9000,
		ContainerPort: 9000,
		WebSockets:    true,
		Service:       "portainer",
	}
}

func (App) Inputs() []app.Input { return nil }
func (App) Links() []app.Link   { return nil }

// Files writes the shared Admin password to the bootstrap file Portainer
// reads with --admin-password-file at first start.
func (App) Files(rc app.RenderContext, secret func(string) string) []app.File {
	return []app.File{{Path: "admin-bootstrap", Content: secret("ADMIN_PASSWORD") + "\n", Mode: 0o600}}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  portainer:
    image: docker.io/portainer/portainer-ce:latest
    command: ["--http-enabled", "--bind", ":9000", "--admin-password-file=/run/secrets/admin-bootstrap"]
    restart: unless-stopped
    volumes:
      - ./data:/data
      - ./admin-bootstrap:/run/secrets/admin-bootstrap:ro
    ports:
      - ${BIND_IP}:` + port + `:9000
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "Portainer",
	Probe: setupspec.Probe{Path: "/", Timeout: "3m"},
	Login: []setupspec.Call{
		{
			Path:    "/api/auth",
			JSON:    map[string]any{"Username": "{user}", "Password": "{password}"},
			Capture: map[string]string{"token": "jwt"},
		},
		{
			Path:    "/api/users/me",
			Header:  map[string]string{"Authorization": "Bearer {token}"},
			Lenient: true,
			Assert:  []setupspec.Assert{{Path: "Username", Equals: "{user}"}},
		},
	},
	Title:       "the preseeded administrator signs in",
	CreateError: "Portainer seeds its administrator from the password file at first start",
	Hint:        "Open {url}, sign in as {user} with the password shown by `self-host credentials`, then connect a Docker environment separately.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
