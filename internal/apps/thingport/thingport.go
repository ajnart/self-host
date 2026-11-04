package thingport

import (
	"context"
	"errors"
	"net/mail"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "thingport", Name: "Thingport", Category: "Productivity",
		Description: "Organize your private collection of 3D printing models",
		Image:       "ghcr.io/tautvydasderzinskas/thingport-frontend:latest",
		Docs:        "https://thingport.net/docs/",
		Port:        8786, ContainerPort: 80, Service: "thingport", LongTimeouts: true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{
		Key: "email", Prompt: "Thingport instance owner's email", Required: true,
		Help:     "The existing instance owner's email. Thingport uses this email, not the shared administrator username, with the password shown by self-host credentials. First-owner creation requires a Manual step.",
		Validate: validateEmail,
	}, {
		Key: "homarr_api_key", Prompt: "Saved self-host Homarr access token", Private: true, RecoveryOnly: true,
		Help: "Optional restoration of the known dashboard-only token of the selected owner; Thingport keeps one token per user and this build never regenerates it. Empty private inputs preserve saved values.",
	}}
}

func validateEmail(value string) error {
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || strings.TrimSpace(value) != value || !strings.Contains(value, "@") {
		return errors.New("enter the Thingport owner's plain email address without a display name or surrounding whitespace")
	}
	return nil
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "auth_secret", Length: 64, Hex: true},
		{Name: "db_password", Length: 48, Hex: true},
	}
}

func (App) Preflight(ctx context.Context, h *helpers.H) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	uid, err := strconv.ParseUint(h.Settings["PUID"], 10, 32)
	if err != nil {
		return &helpers.ManualError{Hint: "Thingport needs a valid nonzero container PUID in the effective public Host settings before startup. Review or restore those settings without changing existing Project IDs or storage ownership, then retry."}
	}
	if uid == 0 {
		return &helpers.ManualError{Hint: "Thingport's official backend cannot use container PUID 0, including self-host's rootless defaults. Use a rootful Docker Project with a nonzero PUID and correctly owned Thingport storage, or wait for a supported rootless configuration. Do not change existing Project IDs or recursively chown data to bypass this hold."}
	}
	return nil
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  thingport:
    image: ghcr.io/tautvydasderzinskas/thingport-frontend:latest
    restart: unless-stopped
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:80
    depends_on:
      thingport-backend:
        condition: service_healthy
    networks:
      default:
      thingport-private:
  thingport-backend:
    image: ghcr.io/tautvydasderzinskas/thingport-backend:latest
    restart: unless-stopped
    environment:
      PUID: ${PUID}
      PGID: ${PGID}
      TZ: ${TZ}
      CHOWN_MODE: minimal
      API_PORT: "8000"
      AUTH_SECRET: ${THINGPORT_AUTH_SECRET}
      FILE_STORAGE: /app/storage
      DATABASE_URL: postgresql://thingport:${THINGPORT_DB_PASSWORD}@thingport-database:5432/thingport?schema=public
      FLARESOLVERR_URL: ""
      COLLECTION_SYNC_ENABLED: "false"
      SYSTEM_NOTIFICATIONS_URL: ""
      RELEASE_NOTES_URL: ""
    volumes:
      - ./storage:/app/storage
    depends_on:
      thingport-database:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "node", "-e", "fetch('http://127.0.0.1:8000/api/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"]
      interval: 10s
      timeout: 5s
      start_period: 60s
      retries: 12
    networks:
      thingport-private:
        aliases:
          - backend
  thingport-database:
    image: docker.io/library/postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: thingport
      POSTGRES_DB: thingport
      POSTGRES_PASSWORD: ${THINGPORT_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "thingport", "-d", "thingport"]
      interval: 5s
      timeout: 5s
      retries: 12
    networks:
      thingport-private:
networks:
  thingport-private:
`
}

func (App) StandaloneGuide() string {
	return "Create the first instance owner on the private Thingport page. " +
		"Automatic collection sync and remote announcements are disabled; no consume folder or optional FlareSolverr fetcher is configured. " +
		"Provider connections, model imports and printing require separate choices. Keep the database and storage folders together when backing up."
}
