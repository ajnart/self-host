package changedetection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"

	"github.com/ajnart/self-host/internal/helpers"
)

func verify(ctx context.Context, h *helpers.H) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := h.Secret(h.Key("api_key"))
	if key == "" {
		return &helpers.ManualError{Hint: keyHint}
	}
	if h.AdminPassword() == "" {
		return &helpers.ManualError{Hint: passwordHint}
	}
	h.Log.Secret(key, h.AdminPassword())
	response, err := readAPI(ctx, h, "/api/v1/systeminfo", key)
	if err != nil {
		return err
	}
	var info struct {
		Version    string   `json:"version"`
		Uptime     *float64 `json:"uptime"`
		WatchCount *int64   `json:"watch_count"`
	}
	if response.Decode(&info) != nil || !helpers.VersionAtLeast(info.Version, "0.60.10") || info.Uptime == nil || math.IsNaN(*info.Uptime) || math.IsInf(*info.Uptime, 0) || *info.Uptime < 0 || info.WatchCount == nil || *info.WatchCount < 0 {
		return errors.New("Changedetection.io did not return supported system information; verify its version and API before retrying self-host setup changedetection")
	}
	response, err = readAPI(ctx, h, "/api/v1/watch", key)
	if err != nil {
		return err
	}
	var watches map[string]struct {
		LastChanged *float64 `json:"last_changed"`
		Viewed      *bool    `json:"viewed"`
	}
	if response.Decode(&watches) != nil || watches == nil {
		return errors.New("Changedetection.io did not return a watch collection; retry self-host setup changedetection")
	}
	for id, watch := range watches {
		if id == "" || watch.LastChanged == nil || math.IsNaN(*watch.LastChanged) || math.IsInf(*watch.LastChanged, 0) || *watch.LastChanged < 0 || watch.Viewed == nil {
			return errors.New("Changedetection.io returned incomplete watch metadata; retry self-host setup changedetection")
		}
	}
	for _, rejectedKey := range []string{"", key + "-invalid"} {
		response, err := h.HTTP(ctx, helpers.Request{
			Path: "/api/v1/systeminfo", NoRedirect: true,
			Header: map[string]string{"x-api-key": rejectedKey},
		})
		if err != nil {
			return errors.New("Changedetection.io API protection could not be verified; retry self-host setup changedetection")
		}
		if response.Status != http.StatusForbidden {
			return &helpers.ManualError{Hint: "Changedetection.io must reject missing and invalid API keys. Review Settings > API privately without changing watches or disabling authentication, then run self-host setup changedetection."}
		}
	}
	return verifyLogin(ctx, h)
}

func readAPI(ctx context.Context, h *helpers.H, path, key string) (helpers.Response, error) {
	response, err := h.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true, Header: map[string]string{"x-api-key": key}})
	if err != nil {
		return helpers.Response{}, errors.New("Changedetection.io API request failed; verify its Address and TLS trust before retrying self-host setup changedetection")
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		return helpers.Response{}, &helpers.ManualError{Hint: keyHint}
	}
	if response.Status != http.StatusOK {
		return helpers.Response{}, fmt.Errorf("Changedetection.io API returned HTTP %d; retry self-host setup changedetection", response.Status)
	}
	return response, nil
}

func verifyLogin(ctx context.Context, h *helpers.H) error {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/settings", NoRedirect: true, Header: map[string]string{"Accept": "text/html"}})
	if err != nil {
		return errors.New("Changedetection.io web protection could not be verified; retry self-host setup changedetection")
	}
	base, baseErr := url.Parse(h.BaseURL)
	location, locationErr := url.Parse(response.Header.Get("Location"))
	if response.Status != http.StatusFound || baseErr != nil || locationErr != nil {
		return &helpers.ManualError{Hint: passwordHint}
	}
	login := base.ResolveReference(location)
	if login.Scheme != base.Scheme || login.Host != base.Host || login.User != nil || login.Path != "/login" {
		return &helpers.ManualError{Hint: passwordHint}
	}
	session := h.Session()
	token, err := session.FormToken(ctx, "/login", "csrf_token")
	if err != nil {
		return &helpers.ManualError{Hint: passwordHint}
	}
	h.Log.Secret(token)
	response, err = session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/login", NoRedirect: true,
		Form:   url.Values{"csrf_token": {token}, "password": {h.AdminPassword()}},
		Header: map[string]string{"Accept": "text/html"},
	})
	if err != nil {
		return errors.New("Changedetection.io login request failed; retry self-host setup changedetection")
	}
	if response.Status != http.StatusFound {
		return &helpers.ManualError{Hint: passwordHint}
	}
	response, err = session.HTTP(ctx, helpers.Request{Path: "/settings", NoRedirect: true, Header: map[string]string{"Accept": "text/html"}})
	if err != nil {
		return errors.New("Changedetection.io authenticated settings could not be verified; retry self-host setup changedetection")
	}
	if response.Status != http.StatusOK || !bytes.Contains(response.Body, []byte(`id="api-key"`)) {
		return &helpers.ManualError{Hint: passwordHint}
	}
	return nil
}
