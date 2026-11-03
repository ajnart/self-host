package komga

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "komga", Name: "Komga", Category: "Media",
		Description:   "Read your comics, manga and ebooks in a private library",
		Image:         "gotson/komga:1.x",
		Docs:          "https://komga.org/docs/installation/docker/",
		Port:          25600,
		ContainerPort: 25600,
		Service:       "komga",
		UsesMedia:     true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "homarr_user_id", Prompt: "Existing limited Komga user ID", Private: true, RecoveryOnly: true},
		{Key: "homarr_user_email", Prompt: "Existing limited Komga user email", Private: true, RecoveryOnly: true},
		{Key: "homarr_user_password", Prompt: "Existing limited Komga user password", Private: true, RecoveryOnly: true},
		{Key: "homarr_api_key", Prompt: "Confirmed matching Komga Homarr key", Private: true, RecoveryOnly: true},
		{Key: "homarr_key_ownership", Prompt: "Confirmed matching Komga Homarr key ownership", Private: true, RecoveryOnly: true},
	}
}

func (App) AdminEmail(username string) string {
	if username == "" {
		return ""
	}
	return username + "@selfhost.local"
}

func (App) Directories(rc app.RenderContext) []app.Directory {
	return []app.Directory{{Path: "config", Mode: 0o700, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  komga:
    image: gotson/komga:1.x
    restart: unless-stopped
    user: "${PUID}:${PGID}"
    environment:
      TZ: ${TZ}
    volumes:
      - type: bind
        source: ./config
        target: /config
        bind:
          create_host_path: false
      - ${MEDIA_ROOT}/books:/data:ro
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:25600
`
}

type administrator struct {
	ID    string   `json:"id"`
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

func recoveryHint(h *helpers.H) string {
	return "Open " + h.URL + " and review the administrator email and password shown by self-host credentials. Restore the matching Project login privately if needed, preserve existing users, libraries and reading progress, then run: self-host setup komga. Do not reset Komga or replace an existing account."
}

func manual(h *helpers.H, reason string) error {
	return &helpers.ManualError{Hint: reason + " " + recoveryHint(h)}
}

func validEmail(user string) bool {
	address, err := mail.ParseAddress(user)
	return err == nil && address.Address == user
}

func validLogin(user, password string) error {
	if !validEmail(user) || strings.TrimSpace(password) == "" {
		return &helpers.ManualError{Hint: "Komga needs a valid Admin email and a nonempty shared Admin password; review self-host credentials and restore the intended Project login privately, then run: self-host setup komga"}
	}
	return nil
}

func identity(h *helpers.H, resp helpers.Response, email string) (string, error) {
	var user administrator
	if resp.Decode(&user) != nil || strings.TrimSpace(user.ID) == "" || user.ID != strings.TrimSpace(user.ID) || user.Email != email || !slices.Contains(user.Roles, "ADMIN") {
		return "", manual(h, "Komga did not verify the intended administrator identity and role.")
	}
	if saved := h.Secret(h.Key("admin_user_id")); saved != "" && saved != user.ID {
		return "", manual(h, "Komga's administrator differs from its recorded identity; the saved identity was preserved.")
	}
	return user.ID, nil
}

func signedIn(ctx context.Context, h *helpers.H, email, password string) (string, error) {
	if err := validLogin(email, password); err != nil {
		return "", err
	}
	h.Log.Secret(password)
	resp, err := h.HTTP(ctx, helpers.Request{
		Path: "/api/v2/users/me", User: email, Password: password, NoRedirect: true,
	})
	if err != nil {
		return "", manual(h, "Komga's authenticated administrator check could not complete; check connectivity and trusted TLS without changing credentials.")
	}
	if resp.Status != http.StatusOK {
		return "", manual(h, fmt.Sprintf("Komga's administrator check returned HTTP %d.", resp.Status))
	}
	return identity(h, resp, email)
}

func administratorExists(ctx context.Context, h *helpers.H) (bool, error) {
	if h.Secret(h.Key("admin_user_id")) != "" {
		return true, manual(h, "The recorded Komga administrator could not be verified; no new account was created.")
	}
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/v1/claim", NoRedirect: true})
	if err != nil || resp.Status != http.StatusOK {
		return false, manual(h, "Komga did not establish whether any users already exist; no account was created.")
	}
	var claim struct {
		IsClaimed *bool `json:"isClaimed"`
	}
	if resp.Decode(&claim) != nil || claim.IsClaimed == nil {
		return false, manual(h, "Komga returned an ambiguous claim status; no account was created.")
	}
	if *claim.IsClaimed {
		return true, manual(h, "Komga already has users, but the intended Admin login was not verified; existing accounts were preserved.")
	}
	return false, nil
}

func createAdministrator(ctx context.Context, h *helpers.H, email, password string) error {
	if _, err := administratorExists(ctx, h); err != nil {
		return err
	}
	resp, err := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/v1/claim", NoRedirect: true,
		Header: map[string]string{"X-Komga-Email": email, "X-Komga-Password": password},
	})
	if err != nil || resp.Status != http.StatusOK {
		return manual(h, "Komga did not confirm its first administrator claim; verify the intended login before retrying Setup.")
	}
	id, err := identity(h, resp, email)
	if err != nil {
		return err
	}
	return h.SetSecret(h.Key("admin_user_id"), id)
}

func firstAdmin() firstadmin.FirstAdmin {
	return firstadmin.FirstAdmin{
		Username: func(_ context.Context, h *helpers.H) (string, error) {
			return (App{}).AdminEmail(h.AdminUser()), nil
		},
		Exists: administratorExists,
		Create: createAdministrator,
		Login: func(ctx context.Context, h *helpers.H, user, password string) (bool, error) {
			_, err := signedIn(ctx, h, user, password)
			return err == nil, err
		},
		Valid: validLogin,
		Hint:  func(h *helpers.H, _ string) string { return recoveryHint(h) },
	}
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/v1/claim", helpers.NoRedirect()); err != nil {
		return err
	}
	if err := firstAdmin().Admin(ctx, h); err != nil {
		return err
	}
	id, err := signedIn(ctx, h, (App{}).AdminEmail(h.AdminUser()), h.AdminPassword())
	if err != nil {
		return err
	}
	if h.Secret(h.Key("admin_user_id")) == "" {
		if err := h.SetSecret(h.Key("admin_user_id"), id); err != nil {
			return err
		}
	}
	if !homarrRequested(h) {
		h.Skip("homarr-key", "No Komga Homarr account is selected and Homarr is not installed")
		return nil
	}
	return h.Step(ctx, "homarr-key", "verify the selected Komga Homarr credential", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyHomarr(ctx, h)
			return err == nil, err
		},
		Do:         func(ctx context.Context) error { return prepareHomarrKey(ctx, h) },
		Hint:       HomarrRecoveryHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if homarrRequested(h) {
		return VerifyHomarr(ctx, h)
	}
	_, err := signedIn(ctx, h, (App{}).AdminEmail(h.AdminUser()), h.AdminPassword())
	return err
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "Create the first administrator on the first visit. Keep the configuration on a local filesystem. Books are mounted read-only; choose an existing library yourself when ready."
}
