// Package seafile is Seafile: private file libraries with sync clients and web
// access, backed by MariaDB and Redis. The first administrator is seeded from
// the Admin password environment.
// Traps: docs/catalog/traps/seafile.md.
package seafile

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
		ID: "seafile", Name: "Seafile", Category: "Files",
		Description:   "Sync and share your files across all your devices",
		Image:         "seafileltd/seafile-mc:13.0-latest",
		Docs:          "https://manual.seafile.com/13.0/setup/setup_ce_by_docker/",
		Port:          8080,
		ContainerPort: 80,
		Service:       "seafile",
	}
}

func (App) Inputs() []app.Input { return nil }

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "db_password", Length: 48},
		{Name: "db_root_password", Length: 48},
		{Name: "jwt_private_key", Length: 48},
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  seafile:
    image: seafileltd/seafile-mc:13.0-latest
    restart: unless-stopped
    environment:
      SEAFILE_MYSQL_DB_HOST: seafile-db
      SEAFILE_MYSQL_DB_PORT: "3306"
      SEAFILE_MYSQL_DB_USER: seafile
      SEAFILE_MYSQL_DB_CCNET_DB_NAME: ccnet_db
      SEAFILE_MYSQL_DB_SEAFILE_DB_NAME: seafile_db
      SEAFILE_MYSQL_DB_SEAHUB_DB_NAME: seahub_db
      INIT_SEAFILE_ADMIN_EMAIL: ${ADMIN_USER}@selfhost.local
      SEAFILE_SERVER_PROTOCOL: http
      SEAFILE_SERVER_HOSTNAME: ${LAN_IP}:` + port + `
      CACHE_PROVIDER: redis
      REDIS_HOST: seafile-redis
      REDIS_PORT: "6379"
      ENABLE_SEADOC: "false"
      ENABLE_NOTIFICATION_SERVER: "false"
      ENABLE_SEAFILE_AI: "false"
      ENABLE_FACE_RECOGNITION: "false"
      SEAFILE_MYSQL_DB_PASSWORD: ${SEAFILE_DB_PASSWORD}
      INIT_SEAFILE_MYSQL_ROOT_PASSWORD: ${SEAFILE_DB_ROOT_PASSWORD}
      INIT_SEAFILE_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      JWT_PRIVATE_KEY: ${SEAFILE_JWT_PRIVATE_KEY}
    volumes:
      - ./data:/shared
    ports:
      - ${BIND_IP}:` + port + `:80
    depends_on:
      - seafile-db
      - seafile-redis
  seafile-db:
    image: mariadb:10.11.19
    restart: unless-stopped
    environment:
      MYSQL_LOG_CONSOLE: "true"
      MARIADB_AUTO_UPGRADE: "1"
      MYSQL_ROOT_PASSWORD: ${SEAFILE_DB_ROOT_PASSWORD}
    volumes:
      - ./database:/var/lib/mysql
    healthcheck:
      test: ["CMD", "/usr/local/bin/healthcheck.sh", "--connect", "--mariadbupgrade", "--innodb_initialized"]
      interval: 20s
      timeout: 5s
      start_period: 30s
      retries: 10
  seafile-redis:
    image: redis:alpine
    restart: unless-stopped
`
}

// setup signs in with the saved password and then reads an administrator-only
// endpoint, so a token alone is not accepted as proof.
var setup = setupspec.Spec{
	Kind:       setupspec.KindFirstAdmin,
	Probe:      setupspec.Probe{Path: "/", Want: []int{200, 302}, Timeout: "10m"},
	Email:      "{user}@selfhost.local",
	StepID:     "admin-login",
	Title:      "administrator {email} signs in",
	Hint:       "Open {url} and sign in as {email} with the password shown by `self-host credentials`; read the logs with: self-host logs seafile",
	CreateWait: "10m",
	Name:       "Seafile",
	Login: []setupspec.Call{
		{
			Path:    "/api2/auth-token/",
			Form:    map[string]string{"username": "{email}", "password": "{password}"},
			Ok:      []int{200},
			Capture: map[string]string{"token": "token"},
		},
		{
			Path:   "/api/v2.1/admin/users/?page=1&per_page=1",
			Header: map[string]string{"Authorization": "Bearer {token}"},
			Ok:     []int{200},
		},
	},
}

func (App) Setup(ctx context.Context, h *helpers.H) error { return setup.Setup(ctx, h) }

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "Sign in with the email made of the ADMIN_USER value and @selfhost.local, and the ADMIN_PASSWORD value."
}
