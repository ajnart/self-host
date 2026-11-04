// Package patchmon is PatchMon: a private dashboard of host patch status, with
// PostgreSQL, an authenticated Redis and a Guacamole daemon. Its first
// administrator is created through the dashboard API from an owner-chosen
// email, and host enrollment stays a separate approval.
// Traps: docs/catalog/traps/patchmon.md.
package patchmon

import (
	"context"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "patchmon", Name: "PatchMon", Category: "Monitoring",
		Description:   "See which of your computers are waiting for software updates",
		Image:         "ghcr.io/patchmon/patchmon-server:latest",
		Docs:          "https://docs.patchmon.net/",
		Port:          3000,
		ContainerPort: 3000,
		Service:       "patchmon",
	}
}

// Inputs asks for the administrator email the first-owner API requires.
func (App) Inputs() []app.Input {
	return []app.Input{{Key: "admin_email", Prompt: "Administrator email"}}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "db_password", Length: 32, Hex: true},
		{Name: "redis_password", Length: 32, Hex: true},
		{Name: "jwt_secret", Length: 64, Hex: true},
		{Name: "session_secret", Length: 64, Hex: true},
		{Name: "ai_encryption_key", Length: 64, Hex: true},
	}
}

func (App) Files(_ app.RenderContext, secret func(string) string) []app.File {
	return []app.File{
		{Path: "redis/.keep", Mode: 0o644},
		{Path: "redis-config", Content: "requirepass " + secret("redis_password") + "\n", Mode: 0o600},
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  patchmon:
    image: ghcr.io/patchmon/patchmon-server:latest
    restart: unless-stopped
    environment:
      APP_ENV: production
      PORT: "3000"
      POD_ID: patchmon-server
      REDIS_HOST: patchmon-redis
      REDIS_PORT: "6379"
      REDIS_DB: "0"
      GUACD_ADDRESS: patchmon-guacd:4822
      CORS_ORIGIN: http://127.0.0.1:` + port + `
      DATABASE_URL: postgresql://patchmon_user:${PATCHMON_DB_PASSWORD}@patchmon-database:5432/patchmon_db
      REDIS_PASSWORD: ${PATCHMON_REDIS_PASSWORD}
      JWT_SECRET: ${PATCHMON_JWT_SECRET}
      SESSION_SECRET: ${PATCHMON_SESSION_SECRET}
      AI_ENCRYPTION_KEY: ${PATCHMON_AI_ENCRYPTION_KEY}
    ports:
      - ${BIND_IP}:` + port + `:3000
    depends_on:
      - patchmon-database
      - patchmon-redis
      - patchmon-guacd
  patchmon-database:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: patchmon_user
      POSTGRES_DB: patchmon_db
      POSTGRES_PASSWORD: ${PATCHMON_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "patchmon_user", "-d", "patchmon_db"]
      interval: 3s
      timeout: 5s
      start_period: 0s
      retries: 7
  patchmon-redis:
    image: redis:alpine
    user: ${PUID}:${PGID}
    # Keep the selected user: the image entrypoint drops container 0 to redis.
    entrypoint: ["/bin/sh", "-c", "umask 077; exec \"$$@\"", "--"]
    command: ["redis-server", "/run/secrets/redis-config"]
    restart: unless-stopped
    volumes:
      - ./redis:/data
      - ./redis-config:/run/secrets/redis-config:ro
  patchmon-guacd:
    image: guacamole/guacd:1.6.0
    restart: unless-stopped
    read_only: true
    cap_drop: ["ALL"]
    tmpfs:
      - /tmp
    healthcheck:
      test: ["CMD", "nc", "-z", "localhost", "4822"]
      interval: 10s
      timeout: 5s
      start_period: 10s
      retries: 3
`
}

// LegacyComposes is the v0.6.0 render, whose guacd service carried cgroup limits.
func (a App) LegacyComposes(rc app.RenderContext) []string {
	return []string{strings.Replace(a.Compose(rc), "    cap_drop: [\"ALL\"]\n    tmpfs:", "    cap_drop: [\"ALL\"]\n    mem_limit: 512m\n    cpus: \"1\"\n    tmpfs:", 1)}
}

// A stable, non-secret client identity lets the pinned server reuse the
// bootstrap session when an immediate check issues the same refresh JWT.
const clientDeviceID = "self-host-patchmon"

var setup = setupspec.Spec{
	Kind:       setupspec.KindFirstAdmin,
	CheckError: "the Admin login cannot sign in to patchmon",
	Probe:      setupspec.Probe{Path: "/health", Want: []int{200}},
	Email:      "{user}@selfhost.local",
	EmailInput: "admin_email",
	Manual:     "Open {url} and create the first administrator as {email}.",
	Exists: []setupspec.Call{{
		Path:    "/api/v1/settings/login-settings",
		Require: []setupspec.Assert{{Path: "hasAdminUsers", Type: "bool"}},
		Assert:  []setupspec.Assert{{Path: "hasAdminUsers", Present: true}},
	}},
	Create: []setupspec.Call{{
		Path:   "/api/v1/auth/setup-admin",
		Header: map[string]string{"X-Device-ID": clientDeviceID},
		JSON:   map[string]any{"firstName": "Dashboard", "lastName": "Administrator", "username": "{user}", "email": "{email}", "password": "{password}"},
		Ok:     []int{201},
	}},
	Login: []setupspec.Call{
		{
			Path:       "/api/v1/auth/login",
			NoRedirect: true,
			Header:     map[string]string{"X-Device-ID": clientDeviceID},
			JSON:       map[string]any{"username": "{user}", "password": "{password}"},
			Ok:         []int{200},
			Capture:    map[string]string{"token": "token"},
		},
		{
			Path:       "/api/v1/auth/profile",
			NoRedirect: true,
			Header:     map[string]string{"Authorization": "Bearer {token}"},
			Ok:         []int{200},
			Assert: []setupspec.Assert{
				{Path: "user.id", Present: true},
				{Path: "user.username", Equals: "{user}"},
				{Path: "user.email", Equals: "{email}"},
				{Path: "user.role", Equals: "superadmin"},
				{Path: "user.is_active", Present: true},
			},
		},
	},
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := setup.Setup(ctx, h); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		h.Skip("homarr-credential", "Homarr is not installed; no PatchMon statistics credential is needed")
		return nil
	}
	return h.Step(ctx, "homarr-credential", "review PatchMon statistics access", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			return false, HomarrReadiness(ctx, h)
		},
		Do:         func(ctx context.Context) error { return HomarrReadiness(ctx, h) },
		Hint:       HomarrHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) StandaloneGuide() string {
	return "After the first start, create the administrator on the first visit."
}
