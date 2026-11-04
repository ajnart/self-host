package jellystat

import (
	"context"
	"errors"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "jellystat", Name: "Jellystat", Category: "Media",
		Description: "See playback statistics for your Jellyfin or Emby server",
		Image:       "cyfershepard/jellystat:latest", Docs: "https://github.com/CyferShepard/Jellystat#readme",
		Port: 3000, ContainerPort: 3000, Service: "jellystat", WebSockets: true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "media_server", Prompt: "Expected media server", Default: "jellyfin",
			Help: "jellyfin or emby. Select the existing backend deliberately; this never replaces Jellystat's saved connection.",
			Validate: func(value string) error {
				if value != "jellyfin" && value != "emby" {
					return errors.New("media_server must be jellyfin or emby")
				}
				return nil
			}},
		{Key: "server_address", Prompt: "Expected existing media-server Address", Private: true,
			Help: "The exact existing Jellyfin or Emby API Address, including its base path. Used only for identity verification, never to configure or replace a connection."},
		{Key: "server_admin_id", Prompt: "Expected existing media-server administrator ID", Private: true,
			Help: "A known administrator ID on the selected backend. Verification requires Jellystat to read that identity using its existing connection; no user or activity is created."},
		{Key: "homarr_api_key", Prompt: "Saved Jellystat Homarr API key", Private: true, RecoveryOnly: true,
			Help: "Restore only the known issued key with its matching ownership and pending records. Empty input preserves the saved secret; reinstalling replaces Compose and requires separate review."},
		{Key: "homarr_ownership", Prompt: "Saved Jellystat Homarr key ownership", Private: true, RecoveryOnly: true,
			Help: "Restore the exact verified ownership record, including its key fingerprint and pinned account/backend. A name or matching endpoint alone is not ownership."},
		{Key: "homarr_pending", Prompt: "Saved Jellystat key issuance journal", Private: true, RecoveryOnly: true,
			Help: "Preserve unresolved issuance evidence. Never clear this record to trigger another key; reconcile the original selected credential privately before retrying Setup."},
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "db_password", Length: 48},
		{Name: "jwt_secret", Length: 48},
	}
}

func (App) Compose(rc app.RenderContext) string {
	emby := "false"
	if rc.Inputs["media_server"] == "emby" {
		emby = "true"
	}
	return `services:
  jellystat:
    image: cyfershepard/jellystat:latest
    restart: unless-stopped
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${JELLYSTAT_DB_PASSWORD}
      POSTGRES_IP: jellystat-db
      POSTGRES_PORT: 5432
      POSTGRES_DB: jfstat
      JWT_SECRET: ${JELLYSTAT_JWT_SECRET}
      TZ: ${TZ}
      IS_EMBY_API: "` + emby + `"
    volumes:
      - ./backup-data:/app/backend/backup-data
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:3000
    depends_on:
      jellystat-db:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:3000/auth/isConfigured"]
      interval: 60s
      timeout: 30s
      retries: 5
      start_period: 30s
  jellystat-db:
    image: postgres:18
    restart: unless-stopped
    shm_size: 1gb
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${JELLYSTAT_DB_PASSWORD}
      POSTGRES_DB: jfstat
    volumes:
      - ./database:/var/lib/postgresql
    healthcheck:
      test: ["CMD", "pg_isready", "--dbname=jfstat", "--username=postgres"]
      interval: 10s
      timeout: 5s
      retries: 5
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/auth/isConfigured", helpers.WantStatus(200), helpers.NoRedirect()); err != nil {
		return err
	}
	if err := h.Step(ctx, "verify", "verify the existing Jellystat account and selected media server", helpers.StepSpec{
		Do: func(ctx context.Context) error {
			_, err := verifiedSession(ctx, h)
			return err
		},
		Hint: accountRecovery, Credential: "admin",
	}); err != nil {
		return err
	}
	if !homarrRelevant(h) {
		return nil
	}
	return h.Step(ctx, "homarr-key", "prepare Jellystat access for Homarr statistics", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyHomarr(ctx, h)
			return err == nil, err
		},
		Do:   func(ctx context.Context) error { return setupHomarrKey(ctx, h) },
		Hint: homarrRecovery, Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return verify(ctx, h)
}

func (App) StandaloneGuide() string {
	return "After the first start, review the private instance and create its administrator, then deliberately connect the intended Jellyfin or Emby server. Configuration starts automatic monitoring and scheduled library synchronization; do not connect a real server or import playback history merely to test statistics."
}
