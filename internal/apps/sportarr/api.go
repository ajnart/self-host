package sportarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const keyHint = "Restore Sportarr's matching current API key from Settings > General > Security through private Project credential recovery, then run self-host setup sportarr. Do not regenerate it. The owner-only --set-file sportarr.api_key=PATH import is install-only: preserve and review the existing Compose definition and separately approve its replacement before using it. Setup never replaces a rejected saved key."

const loginHint = "Open Sportarr > Settings > General > Security and review the existing administrator. If you choose the Admin login from self-host credentials, require Forms authentication for every address and use that login, then run self-host setup sportarr. Setup does not create or replace accounts, change authentication settings, or configure sports libraries and schedules."

var apiKeyScheme = helpers.KeyScheme{Header: "X-Api-Key"}

func request(ctx context.Context, h *helpers.H, req helpers.Request) (helpers.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := http.DefaultClient
	if h.Config.HTTP != nil {
		client = h.Config.HTTP
	}
	isolated := *client
	isolated.Jar = nil
	local := *h
	local.Config.HTTP = &isolated
	req.NoRedirect = true
	resp, err := local.HTTP(ctx, req)
	if err != nil {
		return resp, h.Log.RedactError(fmt.Errorf("Sportarr %s request failed: %w", req.Path, err))
	}
	return resp, nil
}

func verifyAPIKey(ctx context.Context, h *helpers.H, key string) error {
	h.Log.Secret(key)
	resp, err := request(ctx, h, helpers.Request{Path: "/api/system/status", Header: apiKeyScheme.Headers(key)})
	if err != nil {
		return err
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return &helpers.ManualError{Hint: keyHint}
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Sportarr's native system status returned HTTP %d; inspect self-host debug sportarr", resp.Status)
	}
	var status struct {
		AppName        string `json:"appName"`
		Version        string `json:"version"`
		Authentication string `json:"authentication"`
	}
	if resp.Decode(&status) != nil || status.AppName != "Sportarr" || status.Authentication != "apikey" {
		return errors.New("Sportarr did not return its native authenticated identity; the Sonarr compatibility status is not Sportarr identity")
	}
	parts := strings.Split(status.Version, ".")
	if len(parts) < 3 || len(parts) > 4 {
		return errors.New("Sportarr returned an invalid native version")
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 31); err != nil || strings.Trim(part, "0123456789") != "" {
			return errors.New("Sportarr returned an invalid native version")
		}
	}
	if parts[0] != "4" {
		return &helpers.ManualError{Hint: "Sportarr's native API is outside the inspected 4.x contract. Preserve its configuration and review the running version before retrying self-host setup sportarr."}
	}
	for _, header := range []map[string]string{nil, apiKeyScheme.Headers(key + "-self-host-invalid")} {
		resp, err := request(ctx, h, helpers.Request{Path: "/api/system/status", Header: header})
		if err != nil {
			return err
		}
		if resp.Status != http.StatusUnauthorized && resp.Status != http.StatusForbidden {
			return &helpers.ManualError{Hint: "Sportarr did not reject anonymous or invalid-key access to its native API. Review authentication without disabling it, then retry self-host setup sportarr."}
		}
	}
	return nil
}

func authenticatedKey(ctx context.Context, h *helpers.H) (key string, err error) {
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	h.Log.Secret(h.AdminPassword())
	resp, err := request(ctx, h, helpers.Request{
		Method: http.MethodPost, Path: "/api/login",
		JSON: map[string]any{"username": h.AdminUser(), "password": h.AdminPassword(), "rememberMe": false},
	})
	if err != nil {
		return "", err
	}
	var sessions []*http.Cookie
	for _, cookie := range (&http.Response{Header: resp.Header}).Cookies() {
		if cookie.Name == "SportarrAuth" && cookie.Value != "" {
			h.Log.Secret(cookie.Value)
			sessions = append(sessions, cookie)
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, cookie := range sessions {
			logoutErr := logout(cleanup, h, cookie)
			err = errors.Join(err, logoutErr)
		}
	}()
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden || resp.Status == http.StatusNotFound || resp.Status == http.StatusMethodNotAllowed {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	if resp.Status != http.StatusOK {
		return "", fmt.Errorf("Sportarr's JSON login returned HTTP %d; retry self-host setup sportarr", resp.Status)
	}
	var login struct {
		Success *bool `json:"success"`
	}
	if resp.Decode(&login) != nil || login.Success == nil || !*login.Success || len(sessions) != 1 || !sessions[0].HttpOnly {
		return "", errors.New("Sportarr did not return a verified JSON login and private session cookie")
	}
	cookie := (&http.Cookie{Name: "SportarrAuth", Value: sessions[0].Value}).String()
	header := map[string]string{"Cookie": cookie}
	resp, err = request(ctx, h, helpers.Request{Path: "/api/auth/check", Header: header})
	if err != nil {
		return "", err
	}
	var auth struct {
		Authenticated *bool  `json:"authenticated"`
		AuthDisabled  bool   `json:"authDisabled"`
		AuthMethod    string `json:"authMethod"`
		Username      string `json:"username"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&auth) != nil || auth.Authenticated == nil {
		return "", errors.New("Sportarr did not return its authenticated session identity")
	}
	if !*auth.Authenticated || auth.AuthDisabled || auth.AuthMethod != "" && auth.AuthMethod != "forms" || auth.Username != h.AdminUser() {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	resp, err = request(ctx, h, helpers.Request{Path: "/api/settings", Header: header})
	if err != nil {
		return "", err
	}
	var settings struct {
		SecuritySettings string `json:"securitySettings"`
	}
	var security struct {
		AuthenticationMethod   string `json:"authenticationMethod"`
		AuthenticationRequired string `json:"authenticationRequired"`
		Username               string `json:"username"`
		APIKey                 string `json:"apiKey"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&settings) != nil || json.Unmarshal([]byte(settings.SecuritySettings), &security) != nil {
		return "", errors.New("Sportarr did not return its native encoded security settings")
	}
	h.Log.Secret(security.APIKey)
	if security.AuthenticationMethod != "forms" || security.AuthenticationRequired != "enabled" || security.Username != h.AdminUser() {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	if strings.TrimSpace(security.APIKey) == "" || strings.TrimSpace(security.APIKey) != security.APIKey {
		return "", &helpers.ManualError{Hint: keyHint}
	}
	return security.APIKey, nil
}

func logout(ctx context.Context, h *helpers.H, cookie *http.Cookie) error {
	resp, err := request(ctx, h, helpers.Request{
		Method: http.MethodPost, Path: "/api/logout",
		Header: map[string]string{"Cookie": (&http.Cookie{Name: "SportarrAuth", Value: cookie.Value}).String()},
	})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Sportarr's temporary verification session could not be closed (HTTP %d)", resp.Status)
	}
	return nil
}
