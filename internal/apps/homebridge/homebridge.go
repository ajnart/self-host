// Package homebridge is Homebridge. Traps: docs/catalog/traps/homebridge.md.
package homebridge

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Homebridge. Traps: docs/catalog/traps/homebridge.md.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "homebridge", Name: "Homebridge", Category: "Home automation",
		Description:   "Make smart home gadgets work with Apple Home",
		Image:         "homebridge/homebridge:latest",
		Docs:          "https://github.com/homebridge/homebridge/wiki/Install-Homebridge-on-Docker",
		Port:          8581,
		ContainerPort: 8581,
		Service:       "homebridge",
	}
}

func (App) Inputs() []app.Input { return nil }
func (App) Links() []app.Link   { return nil }

func (App) StandaloneGuide() string {
	return "After the first start, complete the setup wizard and create the administrator. " +
		"Bridge networking publishes only the web UI, so HomeKit pairing and LAN device discovery do not work; upstream runs Homebridge with host networking for that."
}

// Compose disables Avahi and publishes administration on loopback; HomeKit
// discovery needs separate LAN networking approval.
func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  homebridge:
    image: homebridge/homebridge:latest
    restart: unless-stopped
    environment:
      ENABLE_AVAHI: "0"
      TZ: ${TZ}
    volumes:
      - ./data:/homebridge
    ports:
      - ${BIND_IP}:` + port + `:8581
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "Homebridge",
	Probe: setupspec.Probe{Path: "/api/auth/settings"},
	Exists: []setupspec.Call{{
		Path:   "/api/auth/settings",
		Assert: []setupspec.Assert{{Path: "env.setupWizardComplete", Equals: "true"}},
	}},
	Create: []setupspec.Call{{
		Path: "/api/setup-wizard/create-first-user",
		JSON: map[string]any{"username": "{user}", "name": "Administrator", "admin": true, "password": "{password}"},
	}},
	Login: []setupspec.Call{
		{
			Path:    "/api/auth/login",
			JSON:    map[string]any{"username": "{user}", "password": "{password}"},
			Capture: map[string]string{"token": "access_token"},
		},
		{
			Path:   "/api/users",
			Header: map[string]string{"Authorization": "Bearer {token}"},
			Assert: []setupspec.Assert{{Some: []setupspec.Assert{
				{Path: "username", Equals: "{user}"},
				{Path: "admin", Equals: "true"},
			}}},
		},
	},
	Manual: "complete the setup wizard and create the administrator with the password from `self-host credentials`",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
