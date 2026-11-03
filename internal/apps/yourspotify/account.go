package yourspotify

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const AccountRecoveryHint = "In Your Spotify Settings > Account infos, select the intended Account ID; use that account's existing raw public token from Settings > Account > Public token. Supply only your-spotify.homarr_api_key and your-spotify.homarr_user_id privately through owner-only files with --set-file after reviewing preservation of Compose and saved credentials, then retry self-host setup your-spotify. Do not regenerate a shared token, switch accounts, scrape the database, mint a login or replace Spotify OAuth credentials."

const loggingHint = "Before any public-token request, Your Spotify's running API service must expose LOG_LEVEL=warn or error. Default info/debug request logs reveal the query token. Review the existing service configuration without replacing credentials or history; if the server is held, keep it held until startup and Spotify ingestion are separately approved. No token request was sent."

func VerifyAccount(ctx context.Context, h *helpers.H) error {
	token, account := h.Secret(h.Key("homarr_api_key")), h.Secret(h.Key("homarr_user_id"))
	decoded, err := hex.DecodeString(account)
	if token == "" || strings.ContainsAny(token, " \t\r\n/?#") || err != nil || len(decoded) != 12 {
		return &helpers.ManualError{Hint: AccountRecoveryHint}
	}
	base, err := url.Parse(h.BaseURL)
	if err != nil || base.Scheme != "http" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return &helpers.ManualError{Hint: startupHint}
	}
	ip := net.ParseIP(base.Hostname())
	if ip == nil || !ip.IsLoopback() || validateAPIPort(apiPort(h.Inputs)) != nil {
		return &helpers.ManualError{Hint: startupHint}
	}
	if h.Config.Exec == nil {
		return &helpers.ManualError{Hint: loggingHint}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := h.ExecIn(ctx, serverService, nil, "printenv", "LOG_LEVEL")
	level := strings.TrimSpace(string(result.Stdout))
	if err != nil || result.ExitCode != 0 || level != "warn" && level != "error" {
		return &helpers.ManualError{Hint: loggingHint}
	}
	h.Log.Secret(token)
	client := *http.DefaultClient
	if h.Config.HTTP != nil {
		client = *h.Config.HTTP
	}
	client.Jar = nil
	probe := *h
	probe.Config.HTTP = &client
	response, err := probe.HTTP(ctx, helpers.Request{
		BaseURL: "http://" + net.JoinHostPort(base.Hostname(), apiPort(h.Inputs)),
		Path:    "/me?" + url.Values{"token": {token}}.Encode(), NoRedirect: true,
	})
	if err != nil {
		return errors.New("Your Spotify's local account request failed; preserve the token and review the held API service before retrying Setup")
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status >= 300 && response.Status < 400 {
		return &helpers.ManualError{Hint: AccountRecoveryHint}
	}
	if response.Status != http.StatusOK {
		return errors.New("Your Spotify did not return its local account identity; review the API service without exposing request details")
	}
	var identity struct {
		Status *bool `json:"status"`
		User   *struct {
			ID          string `json:"_id"`
			SpotifyID   string `json:"spotifyId"`
			PublicToken string `json:"publicToken"`
		} `json:"user"`
	}
	if response.Decode(&identity) != nil {
		return errors.New("Your Spotify returned an invalid local account response; no credentials were changed")
	}
	if identity.Status == nil || !*identity.Status || identity.User == nil || identity.User.ID != account || identity.User.SpotifyID == "" || identity.User.PublicToken != token {
		return &helpers.ManualError{Hint: AccountRecoveryHint}
	}
	return nil
}
