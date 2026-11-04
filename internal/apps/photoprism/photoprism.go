// Package photoprism is PhotoPrism, a private photo library. Its administrator
// is seeded from the Admin login and verified through a real session.
package photoprism

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is PhotoPrism. Traps: docs/catalog/traps/photoprism.md.
type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "photoprism", Name: "PhotoPrism", Category: "Photos",
		Description:   "Browse, organize and search your photo collection privately",
		Image:         "docker.io/photoprism/photoprism:latest",
		Docs:          "https://docs.photoprism.app/getting-started/docker-compose/",
		Port:          2342,
		ContainerPort: 2342,
		Service:       "photoprism",
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  photoprism:
    image: docker.io/photoprism/photoprism:latest
    restart: unless-stopped
    environment:
      PHOTOPRISM_ADMIN_USER: ${ADMIN_USER}
      PHOTOPRISM_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      PHOTOPRISM_DATABASE_DRIVER: sqlite
    volumes:
      - ./originals:/photoprism/originals
      - ./storage:/photoprism/storage
    ports:
      - ${BIND_IP}:` + port + `:2342
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "PhotoPrism",
	Probe: setupspec.Probe{Path: "/api/v1/status", Timeout: "3m"},
	Login: []setupspec.Call{
		{Path: "/api/v1/session", Ok: []int{401}, Lenient: true},
		{
			Path:    "/api/v1/session",
			JSON:    map[string]any{"username": "{user}", "password": "{password}"},
			Ok:      []int{200},
			Lenient: true,
			Capture: map[string]string{"token": "access_token"},
			Assert: []setupspec.Assert{
				{Path: "status", Equals: "success"},
				{Path: "user.Name", Equals: "{user}"},
				{Path: "user.Role", Equals: "admin"},
				{Path: "user.CanLogin", Equals: "true"},
			},
		},
		{
			Path:    "/api/v1/session",
			Header:  map[string]string{"Authorization": "Bearer {token}"},
			Ok:      []int{200},
			Lenient: true,
			Assert: []setupspec.Assert{
				{Path: "status", Equals: "success"},
				{Path: "access_token", Equals: "{token}"},
				{Path: "user.Name", Equals: "{user}"},
				{Path: "user.Role", Equals: "admin"},
			},
		},
	},
	StepID:      "admin-login",
	Title:       "verify the administrator {user}",
	CreateError: "PhotoPrism creates the administrator from PHOTOPRISM_ADMIN_USER/PHOTOPRISM_ADMIN_PASSWORD only on first start",
	Hint: "PhotoPrism seeds the administrator on first start. Recreate the container: self-host start photoprism, " +
		"then open {url} and sign in with the password from `self-host credentials`.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

// Check verifies the administrator session only; the read-only API app
// password and photo ingestion stay separate acceptance steps.
func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
