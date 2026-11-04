// Package nginxproxymanager is the Nginx Proxy Manager admin UI.
// Traps: docs/catalog/traps/nginx-proxy-manager.md.
package nginxproxymanager

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

type App struct{}

func (App) AdminEmail(username string) string { return setup.AdminEmail(username) }

func (App) Info() app.Info {
	return app.Info{
		ID: "nginx-proxy-manager", Name: "Nginx Proxy Manager", Category: "Ingress",
		Description:   "Give your apps easy web addresses and secure padlocks from a web page",
		Image:         "jc21/nginx-proxy-manager:latest",
		Docs:          "https://nginxproxymanager.com/guide/",
		Port:          8181,
		ContainerPort: 81,
		Service:       "nginx-proxy-manager",
	}
}

func (App) Inputs() []app.Input { return nil }

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  nginx-proxy-manager:
    image: jc21/nginx-proxy-manager:latest
    restart: unless-stopped
    volumes:
      - ./data:/data
      - ./letsencrypt:/etc/letsencrypt
    ports:
      - ${BIND_IP}:` + port + `:81
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "Nginx Proxy Manager",
	Probe: setupspec.Probe{Path: "/api/", Web: true},
	Email: "{user}@selfhost.local",
	// v2.16.0's setup flag means an active user already exists.
	Exists: []setupspec.Call{{
		Path:    "/api/",
		Ok:      []int{200},
		Require: []setupspec.Assert{{Path: "setup", Type: "bool"}},
		Assert:  []setupspec.Assert{{Path: "setup", Equals: "true"}},
	}},
	Create: []setupspec.Call{{
		Path: "/api/users",
		JSON: map[string]any{
			"name": "Administrator", "nickname": "Admin", "email": "{email}",
			"auth": map[string]any{"type": "password", "secret": "{password}"},
		},
		Ok: []int{201},
	}},
	Login: []setupspec.Call{
		{
			Path:    "/api/tokens",
			JSON:    map[string]any{"identity": "{email}", "secret": "{password}"},
			Ok:      []int{200},
			Lenient: true,
			Capture: map[string]string{"token": "token"},
		},
		{
			Path:    "/api/users/me",
			Header:  map[string]string{"Authorization": "Bearer {token}"},
			Ok:      []int{200},
			Lenient: true,
			Assert: []setupspec.Assert{
				{Path: "email", Equals: "{email}"},
				{Path: "is_disabled", Equals: "false"},
				{Path: "roles", Some: []setupspec.Assert{{Equals: "admin"}}},
			},
		},
	},
	StepID: "admin-login",
	Title:  "create the administrator {email}",
	Hint:   "Open {url} and create the administrator {email} with the password shown by `self-host credentials`, then run: self-host setup nginx-proxy-manager",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "After the first start, create the administrator on the first visit."
}
