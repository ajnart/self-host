// Package keycloak is the Keycloak identity server: a loopback-only OpenID
// Connect provider backed by a private PostgreSQL database.
package keycloak

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Keycloak. Traps: docs/catalog/traps/keycloak.md.
type App struct{ app.Defaults }

const service = "keycloak"

func (App) Info() app.Info {
	return app.Info{
		ID: "keycloak", Name: "Keycloak", Category: "Identity",
		Description:   "Manage logins in one place, so people sign in once to reach many apps",
		Image:         "quay.io/keycloak/keycloak:latest",
		Docs:          "https://www.keycloak.org/server/configuration-production",
		Port:          8080,
		ContainerPort: 8080,
		Service:       service,
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "db_password", Length: 48}}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  keycloak:
    image: quay.io/keycloak/keycloak:latest
    command: ["start", "--http-enabled=true", "--hostname-strict=false", "--db=postgres", "--db-url-host=keycloak-db", "--db-username=keycloak"]
    restart: unless-stopped
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: ${ADMIN_USER}
      KC_BOOTSTRAP_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      KC_DB_PASSWORD: ${KEYCLOAK_DB_PASSWORD}
    ports:
      - ${BIND_IP}:` + port + `:8080
    depends_on:
      keycloak-db:
        condition: service_healthy
  keycloak-db:
    image: docker.io/library/postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: keycloak
      POSTGRES_USER: keycloak
      POSTGRES_PASSWORD: ${KEYCLOAK_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "keycloak", "-d", "keycloak"]
      interval: 5s
      timeout: 5s
      start_period: 10s
      retries: 10
`
}

// setup signs in through the admin-cli token flow and reads the master realm
// back with the resulting access token.
var setup = setupspec.Spec{
	Kind:        setupspec.KindFirstAdmin,
	Probe:       setupspec.Probe{Path: "/realms/master"},
	Name:        "Keycloak",
	Title:       "the Admin login reads the master realm",
	CreateError: "the bootstrap administrator is created only at first start",
	Hint:        "Recreate Keycloak so it reads KC_BOOTSTRAP_ADMIN_PASSWORD from .env: self-host start keycloak",
	Login: []setupspec.Call{
		{
			Path:    "/realms/master/protocol/openid-connect/token",
			Form:    map[string]string{"client_id": "admin-cli", "username": "{user}", "password": "{password}", "grant_type": "password"},
			Ok:      []int{200},
			Capture: map[string]string{"token": "access_token"},
		},
		{
			Path:   "/admin/realms/master",
			Header: map[string]string{"Authorization": "Bearer {token}"},
			Ok:     []int{200},
			Assert: []setupspec.Assert{{Path: "realm", Equals: "master"}},
		},
	},
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
