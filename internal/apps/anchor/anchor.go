package anchor

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "anchor", Name: "Anchor", Category: "Productivity",
		Description:   "Keep private notes and share selected notes with other people",
		Image:         "ghcr.io/zhfahim/anchor:latest",
		Docs:          "https://github.com/ZhFahim/anchor#self-hosting-with-docker",
		Port:          3000,
		ContainerPort: 3000,
		Service:       "anchor",
	}
}

func (App) AdminEmail(username string) string {
	if username == "" {
		return ""
	}
	return username + "@selfhost.local"
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "admin_email", Prompt: "Intended existing Anchor administrator email", Private: true, RecoveryOnly: true,
			Help: "Optional explicit selection of the intended existing administrator using the shared Admin password. Empty private inputs preserve saved values. Restore the matching saved identity too when recovering a selection; never rename another account to match it. Installing replaces Compose, so preserve existing settings and separately approve replacement before using install --set-file."},
		{Key: "homarr_api_key", Prompt: "Saved self-host Homarr Anchor token", Private: true, RecoveryOnly: true,
			Help: "Restore only the known permanent token self-host issued for the selected administrator, together with its matching ownership record. Empty private inputs preserve saved values."},
		{Key: "homarr_token_ownership", Prompt: "Saved self-host Homarr Anchor token ownership", Private: true, RecoveryOnly: true,
			Help: "Restore only the known matching JSON record with accountId, email and fingerprint. Empty private inputs preserve saved values."},
		{Key: "admin_identity", Prompt: "Saved Anchor administrator identity", Private: true, RecoveryOnly: true,
			Help: "Restore only the known matching JSON selection with id and email, never guessed IDs or another account. This records a selected existing account, not ownership of an account self-host created. Empty private inputs preserve saved values."},
	}
}

func (App) Compose(rc app.RenderContext) string {
	host := "${LAN_IP}"
	if rc.Settings["EXPOSURE"] == "local" {
		host = "127.0.0.1"
	}
	port := strconv.Itoa(rc.Port)
	return `services:
  anchor:
    image: ghcr.io/zhfahim/anchor:latest
    restart: unless-stopped
    environment:
      APP_URL: http://` + host + `:` + port + `
    volumes:
      - ./data:/data
    ports:
      - ${BIND_IP}:` + port + `:3000
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := administratorSetup().Setup(ctx, h); err != nil {
		return err
	}
	if err := rememberAdministrator(ctx, h); err != nil {
		return err
	}
	if err := h.Step(ctx, "notes", "verify authenticated Anchor note access", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := checkNotes(ctx, h)
			return err == nil, err
		},
		Do:   func(ctx context.Context) error { return checkNotes(ctx, h) },
		Hint: "Preserve existing notes and verify the intended Anchor account's read access, then run: self-host setup anchor",
	}); err != nil {
		return err
	}
	return prepareHomarr(ctx, h)
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if err := administratorSetup().Check(ctx, h); err != nil {
		return err
	}
	return checkNotes(ctx, h)
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "The image includes PostgreSQL and keeps its database, uploads and signing secret in ./data. On a genuinely new instance, deliberately create the intended first administrator; otherwise preserve and use the existing account. Registration is enabled by default: review registration in the administrator settings before allowing untrusted access. Configure APP_URL for the actual browser Address when using a proxy."
}
