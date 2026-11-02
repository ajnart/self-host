package romm

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "romm", Name: "RomM", Category: "Media",
		Description: "Organize your game collection and play it in your browser",
		Image:       "rommapp/romm:latest",
		Docs:        "https://docs.romm.app/latest/getting-started/quick-start/",
		Port:        8080, ContainerPort: 8080, Service: "romm",
		UsesMedia: true, WebSockets: true, LongTimeouts: true,
	}
}

func (App) Inputs() []app.Input { return nil }
func (App) Links() []app.Link   { return nil }

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "auth_secret", Length: 64, Hex: true},
		{Name: "db_password", Length: 48},
		{Name: "db_root_password", Length: 48},
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  romm:
    image: rommapp/romm:latest
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      DB_HOST: romm-db
      DB_NAME: romm
      DB_USER: romm-user
      DB_PASSWD: ${ROMM_DB_PASSWORD}
      ROMM_AUTH_SECRET_KEY: ${ROMM_AUTH_SECRET}
      KIOSK_MODE: "false"
      DISABLE_DOWNLOAD_ENDPOINT_AUTH: "false"
      DISABLE_CSRF_PROTECTION: "false"
      HASHEOUS_API_ENABLED: "false"
      LAUNCHBOX_API_ENABLED: "false"
      ENABLE_RESCAN_ON_FILESYSTEM_CHANGE: "false"
      ENABLE_SCHEDULED_RESCAN: "false"
      ENABLE_SCHEDULED_UPDATE_SWITCH_TITLEDB: "false"
      ENABLE_SCHEDULED_UPDATE_LAUNCHBOX_METADATA: "false"
      ENABLE_SYNC_FOLDER_WATCHER: "false"
      ENABLE_SYNC_PUSH_PULL: "false"
      SENTRY_DSN: ""
      OTEL_SDK_DISABLED: "true"
    volumes:
      - ./data:/romm
      - ./redis:/redis-data
      - type: bind
        source: ${MEDIA_ROOT}/romm
        target: /romm/library
        bind:
          create_host_path: false
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8080
    depends_on:
      romm-db:
        condition: service_healthy
  romm-db:
    image: mariadb:11
    restart: unless-stopped
    environment:
      MARIADB_ROOT_PASSWORD: ${ROMM_DB_ROOT_PASSWORD}
      MARIADB_DATABASE: romm
      MARIADB_USER: romm-user
      MARIADB_PASSWORD: ${ROMM_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/mysql
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
      start_period: 30s
      interval: 10s
      timeout: 5s
      retries: 5
`
}

const libraryHint = "Prepare the approved RomM library directory inside the media folder as romm, with permissions suitable for the official image, then retry self-host install romm. Do not move existing libraries, change existing ownership, import ROMs or fetch firmware to finish Setup."

func (App) Preflight(ctx context.Context, h *helpers.H) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := h.Settings["MEDIA_ROOT"]
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return &helpers.ManualError{Hint: libraryHint}
	}
	info, err := os.Stat(filepath.Join(root, "romm"))
	if err != nil || !info.IsDir() {
		return &helpers.ManualError{Hint: libraryHint}
	}
	return nil
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := firstAdmin().Setup(ctx, h); err != nil {
		return err
	}
	user := adminUser(h)
	if h.Secret(h.Key("admin_user")) == "" && user != h.AdminUser() {
		return h.Secrets.Set(h.Key("admin_user"), user)
	}
	return nil
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	_, accepted, err := authenticate(ctx, h, adminUser(h), h.AdminPassword())
	if err != nil {
		return err
	}
	if !accepted {
		return &helpers.ManualError{Hint: accountHint(h, adminUser(h))}
	}
	return nil
}

func adminUser(h *helpers.H) string {
	if saved := h.Secret(h.Key("admin_user")); saved != "" {
		return saved
	}
	return strings.ToLower(h.AdminUser())
}

func (App) StandaloneGuide() string {
	return "Create the romm directory inside ${MEDIA_ROOT} before starting; the library mount never creates its host path. After the first start, create the first administrator in the browser. Keep authentication and CSRF protection enabled. No ROMs, firmware, scans or metadata-provider credentials are required for an empty library. The media library is excluded from App backups; back it up separately."
}
