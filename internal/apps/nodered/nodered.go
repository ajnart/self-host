// Package nodered is the Node-RED flow editor and Admin API. Its
// administrator is enabled before the first start through a bcrypt-hashed
// credential in /data/settings.js.
package nodered

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Node-RED. Traps: docs/catalog/traps/node-red.md.
type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "node-red", Name: "Node-RED", Category: "Home automation",
		Description:   "Connect devices and services with simple drag-and-drop automations",
		Image:         "nodered/node-red:latest",
		Docs:          "https://nodered.org/docs/getting-started/docker",
		Port:          1880,
		ContainerPort: 1880,
		WebSockets:    true,
		Service:       "node-red",
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  node-red:
    image: nodered/node-red:latest
    restart: unless-stopped
    user: ${PUID}:${PGID}
    environment:
      TZ: ${TZ}
      NODE_RED_ADMIN_AUTH_HASH: ${ADMIN_PASSWORD}
    volumes:
      - ./data:/data
      - ./settings:/data/settings.js
    ports:
      - ${BIND_IP}:` + port + `:1880
`
}

// Files writes the editor authentication settings.js with a Go-generated
// bcrypt hash of the shared Admin login. The file is mode 0600 so the hash
// never lands in a world-readable config.
func (App) Files(_ app.RenderContext, secret func(name string) string) []app.File {
	hash, err := helpers.HashPassword(helpers.Bcrypt, secret("ADMIN_PASSWORD"))
	if err != nil {
		return nil
	}
	content := "module.exports = {\n" +
		"  adminAuth: {\n" +
		"    type: 'credentials',\n" +
		"    users: [{ username: '" + secret("ADMIN_USER") + "', password: '" + hash + "', permissions: '*' }]\n" +
		"  }\n" +
		"};\n"
	return []app.File{
		{Path: "settings", Content: content, Mode: 0o600},
		{Path: "data/.keep", Mode: 0o644},
	}
}

var setup = setupspec.Spec{
	Kind:       setupspec.KindFirstAdmin,
	Name:       "Node-RED",
	CheckError: "the administrator {user} cannot sign in to Node-RED",
	Probe:      setupspec.Probe{Path: "/auth/login", Timeout: "3m"},
	Login: []setupspec.Call{
		{
			Path:    "/auth/login",
			Lenient: true,
			Assert:  []setupspec.Assert{{Path: "type", Equals: "credentials"}},
		},
		{Path: "/flows", Ok: []int{401, 403}, Lenient: true},
		{
			Path: "/auth/token",
			JSON: map[string]any{
				"client_id": "node-red-admin", "grant_type": "password", "scope": "*",
				"username": "{user}", "password": "{password}",
			},
			Lenient: true,
			Capture: map[string]string{"token": "access_token"},
			Assert: []setupspec.Assert{
				{Path: "token_type", Equals: "Bearer"},
				{Path: "expires_in", Present: true},
			},
		},
		{
			Path:    "/flows",
			Header:  map[string]string{"Authorization": "Bearer {token}"},
			Ok:      []int{200},
			Lenient: true,
		},
	},
	StepID:      "admin-login",
	Title:       "verify the administrator {user}",
	CreateError: "Node-RED reads /data/settings.js at start; the file is generated before the container starts",
	Hint: "Regenerate /data/settings.js and recreate the container: self-host install node-red, then self-host start node-red. " +
		"Sign in at {url} as {user} with the password from `self-host credentials`.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
