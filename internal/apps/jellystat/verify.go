package jellystat

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/sha3"

	"github.com/ajnart/self-host/internal/helpers"
)

const accountRecovery = "Review Jellystat at its private Address and restore the known Admin login through the Project's private credential boundary, then run self-host setup jellystat. Preserve the existing account, database and media-server credentials; configuration state is not permission to create or replace an account."

const backendRecovery = "Review Jellystat's existing Jellyfin or Emby connection without replacing it. Explicitly provide the expected media_server, private server_address and private server_admin_id through the approved Project input boundary, then run self-host setup jellystat. An unavailable or different backend is not an empty statistics instance; do not import history or create playback activity."

type configuration struct {
	Host         string          `json:"JF_HOST"`
	User         string          `json:"APP_USER"`
	RequireLogin *bool           `json:"REQUIRE_LOGIN"`
	IsJellyfin   *bool           `json:"IS_JELLYFIN"`
	Error        json.RawMessage `json:"error"`
}

func passwordHash(password string) string {
	hash := sha3.NewLegacyKeccak512()
	hash.Write([]byte(password))
	return hex.EncodeToString(hash.Sum(nil))
}

func request(ctx context.Context, h *helpers.H, req helpers.Request, value any) error {
	req.NoRedirect = true
	response, err := h.HTTP(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &helpers.ManualError{Hint: "Jellystat verification could not reach its private API. Check the Address, TLS trust and private logs without bypassing authentication or certificate checks, then run self-host setup jellystat."}
	}
	if response.Status != http.StatusOK {
		return &helpers.ManualError{Hint: fmt.Sprintf("Jellystat verification returned HTTP %d. Preserve the saved credentials and selected backend, review authentication, TLS and API compatibility, then run self-host setup jellystat; no credential was replaced.", response.Status)}
	}
	if response.Decode(value) != nil {
		return &helpers.ManualError{Hint: "Jellystat returned an unsupported verification response. Review its API compatibility without changing credentials or importing history, then run self-host setup jellystat."}
	}
	return nil
}

func verify(ctx context.Context, h *helpers.H) error {
	_, err := verifiedSession(ctx, h)
	if err != nil {
		return err
	}
	if homarrRelevant(h) {
		return verifySavedHomarr(ctx, h)
	}
	return nil
}

func verifiedSession(ctx context.Context, h *helpers.H) (string, error) {
	var state struct {
		State   *int   `json:"state"`
		Version string `json:"version"`
	}
	if err := request(ctx, h, helpers.Request{Path: "/auth/isConfigured"}, &state); err != nil {
		return "", err
	}
	if state.State == nil || *state.State < 0 || *state.State > 2 || !helpers.VersionCompatible(state.Version, "1.1.12") {
		return "", &helpers.ManualError{Hint: "Jellystat did not identify a supported configuration state and version. Review its private database and API compatibility, then run self-host setup jellystat; no bootstrap was attempted."}
	}
	if *state.State == 0 || h.AdminUser() == "" || h.AdminPassword() == "" {
		return "", &helpers.ManualError{Hint: accountRecovery}
	}
	password := passwordHash(h.AdminPassword())
	h.Log.Secret(h.AdminPassword(), password)
	var login struct {
		Token string `json:"token"`
	}
	if err := request(ctx, h, helpers.Request{
		Method: http.MethodPost, Path: "/auth/login",
		JSON: map[string]string{"username": h.AdminUser(), "password": password},
	}, &login); err != nil {
		return "", err
	}
	if strings.TrimSpace(login.Token) == "" {
		return "", &helpers.ManualError{Hint: accountRecovery}
	}
	h.Log.Secret(login.Token)
	header := map[string]string{"Authorization": "Bearer " + login.Token}
	var config configuration
	if err := request(ctx, h, helpers.Request{Path: "/api/getconfig", Header: header}, &config); err != nil {
		return "", err
	}
	if err := verifyAccount(h, config); err != nil {
		return "", err
	}
	if *state.State != 2 {
		return "", &helpers.ManualError{Hint: backendRecovery}
	}
	if err := verifySelection(h, config); err != nil {
		return "", err
	}
	var admins []struct {
		ID     string `json:"Id"`
		Policy struct {
			IsAdministrator *bool `json:"IsAdministrator"`
		} `json:"Policy"`
	}
	if err := request(ctx, h, helpers.Request{Path: "/proxy/getAdminUsers", Header: header}, &admins); err != nil {
		return "", err
	}
	matches := 0
	for _, admin := range admins {
		if admin.ID == "" || admin.Policy.IsAdministrator == nil || !*admin.Policy.IsAdministrator {
			return "", &helpers.ManualError{Hint: backendRecovery}
		}
		if admin.ID == h.Secret(h.Key("server_admin_id")) {
			matches++
		}
	}
	if matches != 1 {
		return "", &helpers.ManualError{Hint: backendRecovery}
	}
	return login.Token, nil
}

func verifyAccount(h *helpers.H, config configuration) error {
	if len(config.Error) != 0 || config.User != h.AdminUser() || config.RequireLogin == nil || !*config.RequireLogin {
		return &helpers.ManualError{Hint: accountRecovery}
	}
	return nil
}

func verifySelection(h *helpers.H, config configuration) error {
	kind := h.Inputs["media_server"]
	if kind == "" {
		kind = "jellyfin"
	}
	expected := h.Secret(h.Key("server_address"))
	adminID := h.Secret(h.Key("server_admin_id"))
	h.Log.Secret(expected, adminID)
	want, valid := normalizedAddress(expected)
	have, supported := normalizedAddress(config.Host)
	if !valid || !supported || have != want || strings.TrimSpace(adminID) == "" || config.IsJellyfin == nil {
		return &helpers.ManualError{Hint: backendRecovery}
	}
	if kind != "jellyfin" && kind != "emby" || *config.IsJellyfin != (kind == "jellyfin") {
		return &helpers.ManualError{Hint: backendRecovery}
	}
	return nil
}

func normalizedAddress(value string) (string, bool) {
	address, err := url.Parse(value)
	if err != nil || address.Hostname() == "" || address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return "", false
	}
	if address.Scheme != "http" && address.Scheme != "https" {
		return "", false
	}
	address.Host = strings.ToLower(address.Host)
	return strings.TrimRight(address.String(), "/"), true
}
