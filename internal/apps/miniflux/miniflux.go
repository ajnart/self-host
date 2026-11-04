package miniflux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

type readerIdentity struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  *bool  `json:"is_admin"`
}

const readerRecoveryHint = "Miniflux Setup never creates or replaces accounts. Inspect the existing instance and preserve its feeds and read/unread state. Only after independently confirming that no account exists, use the documented interactive administrator creation in a real terminal: docker compose exec miniflux miniflux -create-admin, from the Project directory. Use the Admin login shown by self-host credentials. If an account already exists, restore its matching credentials instead of creating another account or resetting its password. Then run self-host setup miniflux."

func (App) Info() app.Info {
	return app.Info{
		ID: "miniflux", Name: "Miniflux", Category: "Productivity",
		Description:   "Read your favorite feeds in one quiet place",
		Image:         "docker.io/miniflux/miniflux:latest",
		Docs:          "https://miniflux.app/docs/docker.html",
		Port:          8080,
		ContainerPort: 8080,
		Service:       "miniflux",
	}
}

func (App) Inputs() []app.Input {
	inputs := []app.Input{{
		Key: "reader_id", Prompt: "Confirmed original Miniflux reader ID", Private: true, RecoveryOnly: true,
		Help: "Restore only the independently confirmed original reader ID, never another account's ID. Empty private inputs preserve the saved value. Review and preserve existing Compose settings and separately approve replacement before using install --set-file.",
	}}
	for _, name := range []string{"homarr_api_key", "homarr_token_ownership", "homarr_token_pending"} {
		inputs = append(inputs, app.Input{
			Key: name, Prompt: "Confirmed original Miniflux " + name, Private: true, RecoveryOnly: true,
			Help: "Restore only the original matching credential and ownership or issuance evidence after independent reconciliation. Never clear pending evidence or adopt a key by name. Empty private inputs preserve saved values. Review and preserve existing Compose settings and separately approve replacement before using install --set-file.",
		})
	}
	return inputs
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "db_password", Length: 48}}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  miniflux:
    image: docker.io/miniflux/miniflux:latest
    restart: unless-stopped
    environment:
      DATABASE_URL: postgres://miniflux:${MINIFLUX_DB_PASSWORD}@miniflux-postgres:5432/miniflux?sslmode=disable
      RUN_MIGRATIONS: "1"
      CREATE_ADMIN: "0"
      DISABLE_SCHEDULER_SERVICE: "1"
      TZ: ${TZ}
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8080
    depends_on:
      miniflux-postgres:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "/usr/bin/miniflux", "-healthcheck", "auto"]
      interval: 10s
      timeout: 5s
      start_period: 30s
      retries: 5
  miniflux-postgres:
    image: docker.io/library/postgres:18
    restart: unless-stopped
    environment:
      POSTGRES_USER: miniflux
      POSTGRES_DB: miniflux
      POSTGRES_PASSWORD: ${MINIFLUX_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "miniflux", "-d", "miniflux"]
      interval: 5s
      timeout: 5s
      start_period: 30s
      retries: 10
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/healthcheck", helpers.WantStatus(http.StatusOK)); err != nil {
		return err
	}
	if err := h.Step(ctx, "reader", "verify the existing Miniflux reader", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			_, err := verifyReader(ctx, h)
			return err == nil && h.Secret(h.Key("reader_id")) != "", err
		},
		Do: func(ctx context.Context) error {
			id, err := verifyReader(ctx, h)
			if err != nil {
				return err
			}
			if h.Secret(h.Key("reader_id")) != "" {
				return nil
			}
			return h.SetSecret(h.Key("reader_id"), strconv.FormatInt(id, 10))
		},
		Hint:       readerRecoveryHint,
		Credential: "admin",
	}); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		h.Skip("homarr-token", "Homarr is not installed; no Miniflux token is needed")
		return nil
	}
	return h.Step(ctx, "homarr-token", "verify Miniflux access for Homarr", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyHomarr(ctx, h)
			return err == nil, err
		},
		Do:         func(ctx context.Context) error { return prepareHomarr(ctx, h) },
		Hint:       HomarrRecoveryHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if _, err := verifyReader(ctx, h); err != nil {
		return err
	}
	if h.Secret(h.Key("reader_id")) == "" {
		return &helpers.ManualError{Hint: "The Miniflux reader has not been recorded; run self-host setup miniflux to verify its existing identity without changing accounts or feeds."}
	}
	if h.Peer("homarr") != nil {
		return VerifyHomarr(ctx, h)
	}
	return nil
}

func verifyReader(ctx context.Context, h *helpers.H) (int64, error) {
	saved := h.Secret(h.Key("reader_id"))
	if saved != "" {
		id, err := strconv.ParseInt(saved, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != saved {
			return 0, &helpers.ManualError{Hint: "The saved Miniflux reader identity is invalid. Preserve the record and independently reconcile the original account; use the private miniflux.reader_id recovery input only to restore that confirmed original identity, never to select another reader. Review and preserve existing Compose settings before approving recovery, then run self-host setup miniflux."}
		}
	}
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return 0, &helpers.ManualError{Hint: readerRecoveryHint}
	}
	resp, err := readerRequest(ctx, h, "/v1/me")
	if err != nil {
		return 0, err
	}
	reader, err := decodeReader(resp)
	if err != nil {
		return 0, err
	}
	if reader.Username != h.AdminUser() || !*reader.IsAdmin || saved != "" && saved != strconv.FormatInt(reader.ID, 10) {
		return 0, &helpers.ManualError{Hint: "Miniflux did not return the intended existing administrator reader. Preserve the saved identity, accounts and feeds; restore the original matching Admin login rather than changing users or privileges. Then run self-host setup miniflux."}
	}
	resp, err = readerRequest(ctx, h, "/v1/feeds")
	if err != nil {
		return 0, err
	}
	var feeds []struct {
		ID     int64 `json:"id"`
		UserID int64 `json:"user_id"`
	}
	if json.Unmarshal(resp.Body, &feeds) != nil || feeds == nil {
		return 0, errors.New("Miniflux did not return its reader's feed list; retry self-host setup miniflux")
	}
	seen := map[int64]bool{}
	for _, feed := range feeds {
		if feed.ID <= 0 || feed.UserID != reader.ID || seen[feed.ID] {
			return 0, errors.New("Miniflux returned incomplete or mismatched reader feeds; retry self-host setup miniflux")
		}
		seen[feed.ID] = true
	}
	return reader.ID, nil
}

func decodeReader(response helpers.Response) (readerIdentity, error) {
	var reader readerIdentity
	if json.Unmarshal(response.Body, &reader) != nil || reader.ID <= 0 || reader.IsAdmin == nil {
		return readerIdentity{}, errors.New("Miniflux returned incomplete reader identity; retry self-host setup miniflux")
	}
	return reader, nil
}

func readerRequest(ctx context.Context, h *helpers.H, path string) (helpers.Response, error) {
	h.Log.Secret(h.AdminPassword())
	return authenticatedRequest(ctx, h, helpers.Request{Path: path, User: h.AdminUser(), Password: h.AdminPassword()}, readerRecoveryHint)
}

func authenticatedRequest(ctx context.Context, h *helpers.H, request helpers.Request, hint string) (helpers.Response, error) {
	request.NoRedirect = true
	resp, err := h.HTTP(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		return resp, fmt.Errorf("Miniflux authenticated request %s could not be completed; retry self-host setup miniflux", request.Path)
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return resp, &helpers.ManualError{Hint: hint}
	}
	if resp.Status != http.StatusOK {
		return resp, fmt.Errorf("Miniflux authenticated request %s returned HTTP %d; preserve existing configuration and retry self-host setup miniflux", request.Path, resp.Status)
	}
	return resp, nil
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "Administrator creation is deliberately disabled. Inspect existing accounts first; only after independently confirming no account exists, run docker compose exec miniflux miniflux -create-admin in a real terminal. Never reset or replace an existing reader. Automatic feed polling and cleanup are disabled by DISABLE_SCHEDULER_SERVICE; review and approve any change before enabling outbound requests."
}
