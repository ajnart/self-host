// Package scrypted is the Scrypted camera hub: a bridge-only bootstrap that
// creates the private first administrator and its durable owner token.
package scrypted

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Scrypted. Traps: docs/catalog/traps/scrypted.md.
type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "scrypted", Name: "Scrypted", Category: "Cameras",
		Description:   "Bring your security cameras into Apple Home and other smart home apps",
		Image:         "ghcr.io/koush/scrypted:lite",
		Docs:          "https://docs.scrypted.app/",
		Port:          11080,
		ContainerPort: 11080,
		Service:       "scrypted",
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  scrypted:
    image: ghcr.io/koush/scrypted:lite
    restart: unless-stopped
    volumes:
      - ./data:/server/volume
    ports:
      - ${BIND_IP}:` + port + `:11080
`
}

func (App) StandaloneGuide() string {
	return "After the first start, create the first administrator on the login page."
}

var setup = setupspec.Spec{
	Kind:       setupspec.KindFirstAdmin,
	Name:       "Scrypted",
	CheckError: "the Admin login cannot sign in to scrypted",
	Probe:      setupspec.Probe{Path: "/", Want: []int{200, 302}},
	Exists: []setupspec.Call{{
		Path:    "/login",
		JSON:    map[string]any{"username": "", "password": ""},
		Denied:  []int{401, 403},
		Lenient: true,
		Assert:  []setupspec.Assert{{Path: "hasLogin", Equals: "true"}},
	}},
	Create: []setupspec.Call{{
		Path:   "/login",
		JSON:   map[string]any{"username": "{user}", "password": "{password}"},
		Assert: []setupspec.Assert{{Path: "authorization", Present: true}},
	}},
	Login: []setupspec.Call{{
		Path: "/login",
		JSON: map[string]any{"username": "{user}", "password": "{password}"},
		Assert: []setupspec.Assert{
			{Path: "username", Equals: "{user}"},
			{Path: "authorization", Present: true},
		},
	}},
	Manual: "Scrypted creates the first administrator on its login page.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
