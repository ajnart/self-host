package karakeep

import (
	"context"
	"net/http"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

const ownerRecoveryHint = "Open Karakeep privately and verify the existing administrator selected by self-host credentials. Preserve its account, bookmarks, tags and attachments. Restore its matching credentials and recorded KARAKEEP_ACCOUNT_ID through the Project's private credential boundary; never clear the ID to accept another account. For a new instance, separately approve first-account creation before briefly enabling signups in the private Compose definition, then disable signups again. SSO, email verification and CAPTCHA remain Manual; do not disable authentication or verification. Retry self-host setup karakeep."

func (App) Info() app.Info {
	return app.Info{
		ID: "karakeep", Name: "Karakeep", Category: "Productivity",
		Description:   "Save and organize bookmarks, notes and files",
		Image:         "ghcr.io/karakeep-app/karakeep:release",
		Docs:          "https://docs.karakeep.app/installation/minimal-install/",
		Port:          3000,
		ContainerPort: 3000,
		Service:       "karakeep",
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
		{Key: "admin_email", Prompt: "Existing Karakeep administrator email", Private: true,
			Help: "Optional selection of an existing local administrator using the shared Admin password. The default is the Admin username at selfhost.local. This never creates an account or changes its password. Supply private values through an owner-only input file; preserve existing Compose before any separately approved reinstall."},
		{Key: "account_id", Prompt: "Recorded Karakeep administrator ID", Private: true, RecoveryOnly: true,
			Help: "Restore only the recorded ID of the selected account after independently verifying its identity. An empty value preserves the saved ID. Never clear or replace an ID to accept a different account."},
		{Key: "homarr_api_key", Prompt: "Recorded Karakeep Homarr key", Private: true, RecoveryOnly: true, Help: HomarrRecoveryHint},
		{Key: "homarr_key_ownership", Prompt: "Recorded Karakeep Homarr key ownership", Private: true, RecoveryOnly: true, Help: HomarrRecoveryHint},
		{Key: "homarr_key_pending", Prompt: "Recorded Karakeep key issuance journal", Private: true, RecoveryOnly: true, Help: HomarrRecoveryHint},
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "auth_secret", Length: 48}}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	host := "${LAN_IP}"
	if rc.Settings["EXPOSURE"] == "local" {
		host = "127.0.0.1"
	}
	return `services:
  karakeep:
    image: ghcr.io/karakeep-app/karakeep:release
    restart: unless-stopped
    environment:
      DATA_DIR: /data
      NEXTAUTH_SECRET: ${KARAKEEP_AUTH_SECRET}
      NEXTAUTH_URL: http://` + host + `:` + port + `
      DISABLE_SIGNUPS: "true"
      DISABLE_NEW_RELEASE_CHECK: "true"
      WORKERS_ENABLED_WORKERS: backup
      WORKERS_DISABLED_WORKERS: backup
      INFERENCE_ENABLE_AUTO_TAGGING: "false"
      INFERENCE_ENABLE_AUTO_SUMMARIZATION: "false"
      EMBEDDING_ENABLE_AUTO_INDEXING: "false"
      OCR_LANGS: ""
    volumes:
      - ./data:/data
    ports:
      - ${BIND_IP}:` + port + `:3000
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/health", helpers.WantStatus(http.StatusOK), helpers.Contains(`"status":"ok"`)); err != nil {
		return err
	}
	var checkErr error
	if err := h.Step(ctx, "owner", "verify the existing Karakeep administrator", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			if h.Secret(h.Key("account_id")) == "" || h.Secret(h.Key("admin_email")) == "" {
				return false, nil
			}
			_, checkErr = readOwner(ctx, h)
			return checkErr == nil, checkErr
		},
		Do: func(ctx context.Context) error {
			if checkErr != nil {
				return checkErr
			}
			owner, err := readOwner(ctx, h)
			if err != nil {
				return err
			}
			if h.Secret(h.Key("account_id")) == "" {
				if err := h.SetSecret(h.Key("account_id"), owner.ID); err != nil {
					return err
				}
			}
			if h.Secret(h.Key("admin_email")) == "" {
				return h.SetSecret(h.Key("admin_email"), owner.Email)
			}
			return nil
		},
		Hint: ownerRecoveryHint, Credential: "admin",
	}); err != nil {
		return err
	}
	checkErr = nil
	return h.Step(ctx, "homarr-key", "prepare read-only Karakeep statistics access", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			_, exists, err := homarrIssuance.Pending(h)
			if err != nil {
				checkErr = err
				return false, err
			}
			if exists || h.Secret(h.Key("homarr_api_key")) == "" && h.Secret(h.Key("homarr_key_ownership")) == "" {
				return false, nil
			}
			checkErr = VerifyHomarr(ctx, h)
			return checkErr == nil, checkErr
		},
		Do: func(ctx context.Context) error {
			if checkErr != nil {
				return checkErr
			}
			return ensureHomarrKey(ctx, h)
		},
		Hint: HomarrRecoveryHint, Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if h.Secret(h.Key("account_id")) == "" || h.Secret(h.Key("admin_email")) == "" {
		return ownerManual("Karakeep has no verified recorded administrator; run self-host setup karakeep.")
	}
	return VerifyHomarr(ctx, h)
}

func (App) StandaloneGuide() string {
	return "This minimal installation stores the database and attachments in ./data. Signups start disabled with DISABLE_SIGNUPS=true. For a new private instance, explicitly approve first-account creation, briefly set DISABLE_SIGNUPS=false, create the first administrator, then restore DISABLE_SIGNUPS=true and recreate the container. Background workers, crawling, search, browser automation, AI and OCR start disabled; enable only the features you deliberately configure."
}
