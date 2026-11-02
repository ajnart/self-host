package homebox

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "homebox", Name: "Homebox", Category: "Productivity",
		Description:   "Keep track of your belongings, their locations and their warranties",
		Image:         "ghcr.io/sysadminsmedia/homebox:latest",
		Docs:          "https://homebox.software/en/quick-start/",
		Port:          3100,
		ContainerPort: 7745,
		Service:       "homebox",
	}
}

func (App) AdminEmail(username string) string {
	if username == "" || strings.Contains(username, "@") {
		return strings.ToLower(username)
	}
	return strings.ToLower(username) + "@selfhost.local"
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "account_identity", Prompt: "Known Homebox account and inventory identity", Private: true, RecoveryOnly: true,
			Help: "Optional matching JSON with accountId, email and groupId for the intended existing account. Restore only independently verified identities through private --set-file input. Empty input preserves the saved selection; changing it requires an explicit inventory selection decision."},
		{Key: "api_key", Prompt: "Existing Homebox account API key", Private: true, RecoveryOnly: true,
			Help: "Optional existing raw API key, including for OIDC-only accounts. Also supply its known account_identity. Keys have the owning account's write privileges; this App only reads inventory statistics and never issues or rotates keys. A rejected key never falls back to the Admin password. Empty input preserves the saved key."},
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "api_key_pepper", Length: 64, Hex: true}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  homebox:
    image: ghcr.io/sysadminsmedia/homebox:latest
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      HBOX_AUTH_API_KEY_PEPPER: ${HOMEBOX_API_KEY_PEPPER}
      HBOX_DEMO: "false"
      HBOX_OPTIONS_ALLOW_REGISTRATION: "false"
      HBOX_OPTIONS_ALLOW_ANALYTICS: "false"
      HBOX_OPTIONS_GITHUB_RELEASE_CHECK: "false"
      HBOX_OTEL_ENABLED: "false"
    volumes:
      - ./data:/data
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:7745
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/v1/status", helpers.WantStatus(http.StatusOK), helpers.NoRedirect()); err != nil {
		return err
	}
	return h.Step(ctx, "account", "verify the selected Homebox account and inventory", helpers.StepSpec{
		Do:         func(ctx context.Context) error { return verifyAccount(ctx, h, true) },
		Hint:       accountRecovery,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return verifyAccount(ctx, h, false)
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "Registration starts disabled. For a genuinely new inventory only, explicitly enable registration in the Compose file, create the intended account, then disable registration again. Otherwise sign in to the existing account without replacing it. Keep the complete data folder and HOMEBOX_API_KEY_PEPPER together in private backups; changing the pepper invalidates existing API keys. Automatic first-account creation, API-key issuance and native Homarr linking are not yet supported."
}
