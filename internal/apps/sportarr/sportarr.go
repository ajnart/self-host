package sportarr

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/mediastorage"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "sportarr", Name: "Sportarr", Category: "Requests",
		Description:   "Organize your sports event recordings and library",
		Image:         "sportarr/sportarr:latest",
		Docs:          "https://wiki.sportarr.net/getting-started/installation/",
		Port:          1867,
		ContainerPort: 1867,
		Service:       "sportarr",
		UsesMedia:     true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{
		Key: "api_key", Prompt: "Existing Sportarr API key (optional recovery)",
		Private: true, RecoveryOnly: true,
		Help: "Restore the current key from Sportarr > Settings > General > Security through an owner-only install --set-file sportarr.api_key=PATH. Install can replace Compose: preserve and review existing mounts, ports and authentication and separately approve replacement first. Setup never regenerates keys or replaces the administrator.",
	}}
}

func (App) StandaloneGuide() string {
	return "Keep /config writable by the selected user. In Settings > General > Security, require Forms authentication for every address and configure your administrator. Choose your sports library, profiles and download clients yourself; no events or downloads are added by self-host."
}

func (App) Directories(rc app.RenderContext) []app.Directory {
	return []app.Directory{{Path: "config", Mode: 0o755, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (App) Compose(rc app.RenderContext) string {
	mounts := "      - ${MEDIA_ROOT}:/media\n      - ${DOWNLOADS_ROOT}:/downloads\n"
	if mediastorage.Shared(rc.Settings) {
		mounts = "      - ${DATA_ROOT}:/data\n"
	}
	return `services:
  sportarr:
    image: sportarr/sportarr:latest
    user: "${PUID}:${PGID}"
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      UMASK: ${UMASK}
    volumes:
      - type: bind
        source: ./config
        target: /config
        bind:
          create_host_path: false
` + mounts + `    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:1867
    healthcheck:
      test: ["CMD-SHELL", "curl -fs -o /dev/null http://localhost:1867/ping || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 60s
`
}

func (a App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/ping", helpers.WantStatus(200)); err != nil {
		return err
	}
	var checkErr error
	return h.Step(ctx, "credentials", "verify Sportarr's API key and existing Admin login", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			checkErr = a.Check(ctx, h)
			return checkErr == nil, checkErr
		},
		Do: func(ctx context.Context) error {
			if h.Secret(h.Key("api_key")) != "" {
				return checkErr
			}
			key, err := authenticatedKey(ctx, h)
			if err != nil {
				return err
			}
			if err := verifyAPIKey(ctx, h, key); err != nil {
				return err
			}
			return h.SetSecret(h.Key("api_key"), key)
		},
		Hint: loginHint, Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	key := h.Secret(h.Key("api_key"))
	if key == "" {
		return &helpers.ManualError{Hint: keyHint}
	}
	if err := verifyAPIKey(ctx, h, key); err != nil {
		return err
	}
	current, err := authenticatedKey(ctx, h)
	if err != nil {
		return err
	}
	if current != key {
		return &helpers.ManualError{Hint: keyHint}
	}
	return nil
}
