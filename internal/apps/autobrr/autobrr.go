package autobrr

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

const bootstrapHint = "Open Autobrr and sign in with the intended Admin login from self-host credentials. Only an instance with no user and no OIDC may create its first user automatically. Preserve existing accounts and authentication; restore the intended login instead of resetting it, then run self-host setup autobrr."

var errAdminRejected = errors.New("Autobrr rejected the administrator login")

func (App) Info() app.Info {
	return app.Info{
		ID: "autobrr", Name: "Autobrr", Category: "Requests",
		Description: "Watch release announcements and apply your chosen filters and actions",
		Image:       "ghcr.io/autobrr/autobrr:latest", Docs: "https://autobrr.com/installation/docker/",
		Port: 7474, ContainerPort: 7474, Service: "autobrr",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "homarr_api_key", Prompt: "Known Autobrr Homarr key", Private: true, RecoveryOnly: true,
			Help: "Restore only the known key with its matching ownership and pending evidence through private input recovery. Empty input preserves saved values. Reinstall replaces Compose and requires separate review and approval."},
		{Key: "homarr_key_ownership", Prompt: "Matching Autobrr key ownership", Private: true, RecoveryOnly: true,
			Help: "Restore the known matching administrator, name, creation time, scopes and fingerprint record, not ownership inferred from a key name. Empty input preserves saved values."},
		{Key: "homarr_key_pending", Prompt: "Known Autobrr issuance evidence", Private: true, RecoveryOnly: true,
			Help: "Preserve or restore the exact private issuance journal. Never erase an uncertain attempt to obtain another key. Empty input preserves saved values."},
	}
}

func (App) Directories(rc app.RenderContext) []app.Directory {
	return []app.Directory{{Path: "config", Mode: 0o700, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  autobrr:
    image: ghcr.io/autobrr/autobrr:latest
    restart: unless-stopped
    user: "${PUID}:${PGID}"
    environment:
      TZ: ${TZ}
      AUTOBRR__HOST: 0.0.0.0
    volumes:
      - type: bind
        source: ./config
        target: /config
        bind:
          create_host_path: false
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:7474
    healthcheck:
      test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:7474/api/healthz/readiness"]
      interval: 30s
      timeout: 10s
      retries: 3
`
}

func adminSetup() firstadmin.FirstAdmin {
	return firstadmin.FirstAdmin{
		Ready: firstadmin.Probe{Request: func(*helpers.H) helpers.Request {
			return helpers.Request{Path: "/api/healthz/readiness", NoRedirect: true}
		}, Want: []int{http.StatusOK}},
		Login: func(ctx context.Context, h *helpers.H, _, _ string) (bool, error) {
			_, accepted, err := adminSession(ctx, h)
			return accepted, err
		},
		Create: createAdmin,
		Valid: func(user, password string) error {
			if strings.TrimSpace(user) == "" || password == "" {
				return &helpers.ManualError{Hint: bootstrapHint}
			}
			return nil
		},
		Hint: func(*helpers.H, string) string { return bootstrapHint },
	}
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := adminSetup().Setup(ctx, h); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		h.Skip("homarr-key", "Homarr is not installed; no Autobrr statistics key is needed")
		return nil
	}
	return h.Step(ctx, "homarr-key", "prepare Autobrr statistics access", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			if _, exists, err := homarrIssuance.Pending(h); exists || err != nil {
				return false, err
			}
			if h.Secret(h.Key("homarr_api_key")) == "" && h.Secret(h.Key("homarr_key_ownership")) == "" {
				return false, nil
			}
			err := VerifyHomarr(ctx, h)
			return err == nil, err
		},
		Do: func(ctx context.Context) error { return prepareHomarrKey(ctx, h) }, Hint: HomarrRecoveryHint, Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	_, accepted, err := adminSession(ctx, h)
	if err != nil {
		return err
	}
	if !accepted {
		return &helpers.ManualError{Hint: bootstrapHint}
	}
	if h.Peer("homarr") != nil {
		return VerifyHomarr(ctx, h)
	}
	return nil
}

func (App) StandaloneGuide() string {
	return "For standalone Compose, prepare the configuration bind source for the selected non-root user with private permissions before starting; Docker will not create it. On the first visit create the intended administrator only if this instance has no user. Configure tracker accounts, filters and download clients yourself; no releases are downloaded by Setup. Keep the persistent configuration and database when updating or removing the container."
}

func createAdmin(ctx context.Context, h *helpers.H, user, password string) error {
	for _, name := range []string{"homarr_api_key", "homarr_key_ownership", "homarr_key_pending"} {
		if h.Secret(h.Key(name)) != "" {
			return &helpers.ManualError{Hint: bootstrapHint}
		}
	}
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/auth/onboard", NoRedirect: true})
	if err != nil {
		return errors.New("Autobrr first-user eligibility could not be checked; retry self-host setup autobrr")
	}
	if resp.Status != http.StatusNoContent {
		return &helpers.ManualError{Hint: bootstrapHint}
	}
	resp, err = h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/auth/onboard", NoRedirect: true,
		JSON: map[string]string{"username": user, "password": password}})
	if err != nil || resp.Status != http.StatusOK {
		return &helpers.ManualError{Hint: bootstrapHint}
	}
	return nil
}

func adminSession(ctx context.Context, h *helpers.H) (*helpers.Session, bool, error) {
	cookie, err := h.Remember("autobrr-admin-session", func() (string, error) {
		session := h.Session()
		resp, err := session.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/auth/login", NoRedirect: true,
			JSON: map[string]any{"username": h.AdminUser(), "password": h.AdminPassword(), "remember_me": false}})
		if err != nil {
			return "", errors.New("Autobrr administrator login could not be checked; retry self-host setup autobrr")
		}
		value := resp.Cookie("autobrr_user_session")
		h.Log.Secret(value)
		if resp.Status == http.StatusForbidden {
			return "", errAdminRejected
		}
		if resp.Status != http.StatusNoContent || value == "" {
			return "", &helpers.ManualError{Hint: bootstrapHint}
		}
		return value, nil
	})
	if errors.Is(err, errAdminRejected) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	h.Log.Secret(cookie)
	session := h.Session()
	session.SetCookie("autobrr_user_session", cookie)
	resp, err := session.HTTP(ctx, helpers.Request{Path: "/api/auth/validate", NoRedirect: true})
	if err != nil {
		return nil, false, errors.New("Autobrr administrator identity could not be checked; retry self-host setup autobrr")
	}
	var identity struct {
		Username   string `json:"username"`
		AuthMethod string `json:"auth_method"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&identity) != nil || identity.Username == "" || identity.Username != h.AdminUser() || identity.AuthMethod != "password" {
		return nil, false, &helpers.ManualError{Hint: bootstrapHint}
	}
	return session, true, nil
}
