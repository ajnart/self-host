// Package printstash installs the official single-container PrintStash image.
// Deployment and authentication sources: docs/catalog/traps/printstash.md.
package printstash

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "printstash", Name: "PrintStash", Category: "Productivity",
		Description: "Organize 3D printing files, revisions and print history",
		Image:       "ghcr.io/xiao-villamor/printstash:latest",
		Docs:        "https://github.com/xiao-villamor/PrintStash",
		Port:        3000, ContainerPort: 3000, Service: "printstash",
		WebSockets: true, LongTimeouts: true, HomeAddressOnly: true,
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  printstash:
    image: ghcr.io/xiao-villamor/printstash:latest
    restart: unless-stopped
    stop_grace_period: 60s
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:3000
    environment:
      PUID: "${PUID}"
      PGID: "${PGID}"
      VAULT_SETUP_MODE: trusted_network
      VAULT_RESTART_ENABLED: "true"
    volumes:
      - ./data:/data
`
}

func (App) Directories(rc app.RenderContext) []app.Directory {
	return []app.Directory{{Path: "data", Mode: 0o700, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (App) Preflight(ctx context.Context, h *helpers.H) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, key := range []string{"PUID", "PGID"} {
		id, err := strconv.ParseUint(h.Settings[key], 10, 32)
		if err != nil || id == 0 || id == 1<<32-1 {
			return &helpers.ManualError{Hint: "PrintStash requires positive numeric PUID and PGID values, matching the data folder's owner. Use a Project with supported nonzero runtime IDs. Preserve existing data and ownership."}
		}
	}
	return nil
}

func manual(h *helpers.H, message string) error {
	return &helpers.ManualError{Hint: message + " Open " + h.URL + " on a trusted local network. For a fresh installation, create the first administrator with the shared Admin login and choose storage. Preserve any existing account and data. After resolving the issue, retry self-host setup printstash."}
}

func request(ctx context.Context, h *helpers.H, req helpers.Request) (helpers.Response, error) {
	req.NoRedirect = true
	response, err := h.WithoutCookies().HTTP(ctx, req)
	if err != nil {
		return response, h.Log.RedactError(err)
	}
	if response.Status >= http.StatusInternalServerError {
		return response, fmt.Errorf("PrintStash %s returned HTTP %d", req.Path, response.Status)
	}
	return response, nil
}

func setupReady(ctx context.Context, h *helpers.H) error {
	r, err := request(ctx, h, helpers.Request{Path: "/api/v1/setup/status"})
	if err != nil {
		return err
	}
	if r.Status != http.StatusOK {
		return manual(h, fmt.Sprintf("PrintStash setup status returned HTTP %d.", r.Status))
	}
	var s struct {
		Configured *bool `json:"configured"`
		Recovery   *bool `json:"recovery_required"`
		Storage    *bool `json:"storage_choice_required"`
	}
	if r.Decode(&s) != nil || s.Configured == nil || !*s.Configured || s.Recovery == nil || *s.Recovery || s.Storage == nil || *s.Storage {
		return manual(h, "PrintStash setup or storage needs review.")
	}
	return nil
}

func session(ctx context.Context, h *helpers.H) (string, error) {
	if strings.TrimSpace(h.AdminUser()) == "" || h.AdminPassword() == "" {
		return "", manual(h, "The shared Admin login is unavailable.")
	}
	h.Log.Secret(h.AdminPassword())
	return h.Remember("printstash-session", func() (string, error) {
		r, err := request(ctx, h, helpers.Request{
			Method: http.MethodPost, Path: "/api/v1/auth/login",
			JSON: map[string]string{"username": h.AdminUser(), "password": h.AdminPassword()},
		})
		if err != nil {
			return "", err
		}
		if r.Status != http.StatusOK {
			return "", manual(h, fmt.Sprintf("PrintStash login returned HTTP %d. Check the shared Admin login with self-host credentials.", r.Status))
		}
		var result struct {
			Token   string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Type    string `json:"token_type"`
		}
		decodeErr := r.Decode(&result)
		h.Log.Secret(result.Token)
		h.Log.Secret(result.Refresh)
		if decodeErr != nil || strings.TrimSpace(result.Token) == "" || !strings.EqualFold(result.Type, "bearer") {
			return "", manual(h, "PrintStash did not return a valid session.")
		}
		return result.Token, nil
	})
}

func verify(ctx context.Context, h *helpers.H) error {
	if err := setupReady(ctx, h); err != nil {
		return err
	}
	token, err := session(ctx, h)
	if err != nil {
		return err
	}
	header := map[string]string{"Authorization": "Bearer " + token}
	r, err := request(ctx, h, helpers.Request{Path: "/api/v1/auth/me", Header: header})
	if err != nil {
		return err
	}
	var u struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Active   *bool  `json:"is_active"`
		Admin    *bool  `json:"is_superuser"`
	}
	if r.Status != http.StatusOK || r.Decode(&u) != nil || u.ID <= 0 || u.Username != h.AdminUser() || u.Active == nil || !*u.Active || u.Admin == nil || !*u.Admin {
		return manual(h, "PrintStash did not verify the selected active administrator.")
	}
	r, err = request(ctx, h, helpers.Request{Path: "/api/v1/health/details", Header: header})
	if err != nil {
		return err
	}
	var health struct {
		Status     string `json:"status"`
		Components map[string]struct {
			OK *bool `json:"ok"`
		} `json:"components"`
	}
	if r.Status != http.StatusOK || r.Decode(&health) != nil || health.Status != "ok" || health.Components["database"].OK == nil || !*health.Components["database"].OK || health.Components["storage"].OK == nil || !*health.Components["storage"].OK {
		return manual(h, "PrintStash did not report healthy database and storage components.")
	}
	return nil
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/v1/setup/status", helpers.WantStatus(http.StatusOK), helpers.NoRedirect()); err != nil {
		return err
	}
	return h.Step(ctx, "admin", "verify PrintStash administrator and storage", helpers.StepSpec{
		Do:         func(ctx context.Context) error { return verify(ctx, h) },
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error { return verify(ctx, h) }

func (App) StandaloneGuide() string {
	return "Create the first administrator and choose storage on a trusted local network. Set PUID and PGID to the positive numeric owner of the data folder. Keep that folder across restarts and upgrades. Before internet access, use HTTPS and close first-run setup; follow the PrintStash installation guide."
}
