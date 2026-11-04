// Package immich is the Immich photo library: a private server with its own
// PostgreSQL, Valkey queue, and machine-learning containers.
package immich

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Immich. Traps: docs/catalog/traps/immich.md.
type App struct{ app.Defaults }

func (App) AdminEmail(username string) string { return setup.AdminEmail(username) }

const (
	service = "immich"
	email   = "admin@example.test"
)

func (App) Info() app.Info {
	return app.Info{
		ID: "immich", Name: "Immich", Category: "Photos",
		Description:   "Back up your phone's photos and find any of them by searching",
		Image:         "ghcr.io/immich-app/immich-server:release",
		Docs:          "https://docs.immich.app/install/docker-compose/",
		Port:          2283,
		ContainerPort: 2283,
		WebSockets:    true,
		LongTimeouts:  true,
		Service:       service,
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "db_password", Length: 48}}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  immich:
    image: ghcr.io/immich-app/immich-server:release
    restart: unless-stopped
    environment:
      DB_DATABASE_NAME: immich
      DB_HOSTNAME: immich-database
      DB_USERNAME: postgres
      DB_PASSWORD: ${IMMICH_DB_PASSWORD}
      IMMICH_ALLOW_SETUP: "true"
      IMMICH_MACHINE_LEARNING_URL: http://immich-machine-learning:3003
      REDIS_HOSTNAME: immich-redis
    volumes:
      - ./uploads:/data
    ports:
      - ${BIND_IP}:` + port + `:2283
    depends_on:
      - immich-database
      - immich-redis
    healthcheck:
      test: ["CMD", "immich-healthcheck"]
      interval: 30s
      timeout: 30s
      retries: 3
  immich-machine-learning:
    image: ghcr.io/immich-app/immich-machine-learning:release
    restart: unless-stopped
    volumes:
      - ./model-cache:/cache
    healthcheck:
      test: ["CMD", "python3", "healthcheck.py"]
      interval: 30s
      timeout: 30s
      retries: 3
  immich-redis:
    image: docker.io/valkey/valkey:latest
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 30s
      timeout: 10s
      retries: 3
  immich-database:
    image: ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0
    restart: unless-stopped
    shm_size: 134217728
    environment:
      POSTGRES_DB: immich
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${IMMICH_DB_PASSWORD}
      POSTGRES_INITDB_ARGS: --data-checksums
    volumes:
      - ./database:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "/usr/local/bin/healthcheck.sh"]
      interval: 300s
      timeout: 30s
      start_period: 300s
      retries: 3
`
}

var setup = setupspec.Spec{
	Kind:       setupspec.KindFirstAdmin,
	Name:       "Immich",
	CheckError: "the administrator {email} cannot sign in",
	Probe:      setupspec.Probe{Live: true, Path: "/api/server/config", Timeout: "10m"},
	Email:      email,
	Exists: []setupspec.Call{{
		Path:   "/api/server/config",
		Assert: []setupspec.Assert{{Path: "isInitialized", Equals: "true"}},
	}},
	Create: []setupspec.Call{{
		Path: "/api/auth/admin-sign-up",
		JSON: map[string]any{"email": "{email}", "name": "Admin", "password": "{password}"},
		Ok:   []int{201},
	}},
	Login: []setupspec.Call{{
		Path:   "/api/auth/login",
		JSON:   map[string]any{"email": "{email}", "password": "{password}"},
		Ok:     []int{200, 201},
		Denied: []int{400, 401},
		Assert: []setupspec.Assert{
			{Path: "userEmail", Equals: "{email}"},
			{Path: "isAdmin", Equals: "true"},
			{Path: "accessToken", Present: true},
		},
	}},
	Title: "create the first administrator {email}",
	Hint:  "Open {url}, finish the Immich setup with email {email} and the password shown by `self-host credentials`, then run: self-host setup immich",
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := setup.Setup(ctx, h); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		h.Skip("homarr-key", "Homarr is not installed; no Immich statistics key is needed")
		return nil
	}
	return h.Step(ctx, "homarr-key", "prepare read-only Immich statistics", helpers.StepSpec{
		Check:      func(ctx context.Context) (bool, error) { return homarrKeyWorks(ctx, h) },
		Do:         func(ctx context.Context) error { return createHomarrKey(ctx, h) },
		Hint:       homarrKeyRecovery(h),
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) StandaloneGuide() string {
	return "After the first start, create the administrator on the first visit."
}
