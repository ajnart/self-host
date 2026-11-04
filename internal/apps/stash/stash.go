package stash

import (
	"context"
	"net/http"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "stash", Name: "Stash", Category: "Media",
		Description:   "Organize your private adult-media library",
		Image:         "stashapp/stash:latest",
		Docs:          "https://docs.stashapp.cc/installation/docker/",
		Port:          9999,
		ContainerPort: 9999,
		Service:       "stash",
		UsesMedia:     true,
		WebSockets:    true,
		LongTimeouts:  true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "api_key", Prompt: "Existing Stash instance API key", Private: true, RecoveryOnly: true,
			Help: "Restore the selected instance's existing key through private input. Stash has one unscoped key: never generate another key merely for Homarr. Empty input preserves the saved key. Reinstall replaces Compose; separately approve and preserve existing mounts, paths and Access before using install --set-file."},
		{Key: "account_username", Prompt: "Existing Stash account username", Private: true, RecoveryOnly: true,
			Help: "Select the existing account without changing it. When absent, verification expects the shared Admin login username. Restore this value with the matching existing API key; do not rename the server's account."},
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  stash:
    image: stashapp/stash:latest
    restart: unless-stopped
    entrypoint: ["/bin/sh", "-c", "umask 077; exec stash \"$$@\"", "--"]
    environment:
      STASH_CONFIG_FILE: /root/.stash/config.yml
      TZ: ${TZ}
    volumes:
      - ./config:/root/.stash
      - ./metadata:/metadata
      - ./cache:/cache
      - ./blobs:/blobs
      - ./generated:/generated
      - ${MEDIA_ROOT}/adult:/data:ro
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:9999
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/login", helpers.WantStatus(http.StatusOK, http.StatusFound), helpers.NoRedirect()); err != nil {
		return err
	}
	return h.Step(ctx, "account", "verify the protected Stash account and existing instance key", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := verify(ctx, h)
			return err == nil, err
		},
		Hint:       recoveryHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return verify(ctx, h)
}

func (App) StandaloneGuide() string {
	return "Complete the first visit with authentication enabled and no library paths or scans. Keep configuration and the database in /root/.stash; select /metadata, /cache, /blobs and /generated for their corresponding storage when wanted. New writes use a private umask and content at /data is read-only; existing permissions are not changed. Preserve existing paths and the single unscoped instance API key; self-host does not bootstrap accounts or generate keys in this partial App slice."
}
