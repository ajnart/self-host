package xteve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"

	"github.com/ajnart/self-host/internal/helpers"
)

const recoveryHint = "Finish the first-run tuner, EPG and playlist choices yourself in xTeVe; nothing is configured for you. Then create or keep a dedicated xTeVe user with API permission only and save it with the private xteve username and password inputs, or set the private xteve auth input to none only when you intentionally keep the API unauthenticated. This CLI never creates, rotates or disables xTeVe accounts or authentication. If the API is disabled, enable it in xTeVe settings. Never open the xTeVe web page on an empty instance just to probe it. Then retry self-host setup xteve and self-host setup homarr."

type apiReply struct {
	Status  bool   `json:"status"`
	Err     string `json:"err"`
	Token   string `json:"token"`
	Version struct {
		API   string `json:"api"`
		Xteve string `json:"xteve"`
	} `json:"version"`
	Epg struct {
		Source string `json:"source"`
	} `json:"epg"`
	Streams map[string]json.RawMessage `json:"streams"`
}

type credentials struct{ user, password string }

func (c credentials) none() bool { return c.user == "" && c.password == "" }

func manual(format string, args ...any) error {
	return &helpers.ManualError{Hint: fmt.Sprintf(format, args...) + " " + recoveryHint}
}

func selected(h *helpers.H) (credentials, error) {
	c := credentials{user: h.Secret(h.Key("username")), password: h.Secret(h.Key("password"))}
	switch {
	case (c.user == "") != (c.password == ""):
		return c, manual("xTeVe requires both saved API credentials, never a partial or unauthenticated fallback")
	case c.none() && h.Secret(h.Key("auth")) != "none":
		return c, manual("no xTeVe API credentials are saved; missing credentials are not proof that authentication is disabled")
	}
	return c, nil
}

func call(ctx context.Context, h *helpers.H, body map[string]string) (apiReply, error) {
	response, err := h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/", JSON: body, NoRedirect: true})
	if err != nil {
		return apiReply{}, errors.New("xTeVe API is unavailable; inspect self-host logs xteve and retry self-host setup xteve")
	}
	switch {
	case response.Status == http.StatusLocked:
		return apiReply{}, manual("the xTeVe API is disabled in its settings")
	case response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status >= 300 && response.Status < 400:
		return apiReply{}, manual("xTeVe requires a different authentication scheme or Address than the Catalog's internal HTTP Address (HTTP %d)", response.Status)
	case response.Status != http.StatusOK:
		return apiReply{}, fmt.Errorf("xTeVe API returned HTTP %d; retry self-host setup xteve", response.Status)
	}
	var reply apiReply
	if response.Decode(&reply) != nil {
		return apiReply{}, errors.New("xTeVe returned a malformed API response; retry self-host setup xteve")
	}
	return reply, nil
}

func count(raw map[string]json.RawMessage, key string) (float64, error) {
	value, found := raw[key]
	if !found {
		return 0, nil
	}
	var number float64
	if string(value) == "null" || json.Unmarshal(value, &number) != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || math.Trunc(number) != number {
		return 0, errors.New("xTeVe returned an invalid stream count; retry self-host setup xteve")
	}
	return number, nil
}

func verifyStatus(reply apiReply) error {
	if !reply.Status || reply.Err != "" || reply.Version.API == "" || reply.Version.Xteve == "" || reply.Epg.Source == "" || reply.Streams == nil {
		return errors.New("xTeVe did not return a genuine status response; retry self-host setup xteve")
	}
	all, err := count(reply.Streams, "all")
	if err != nil {
		return err
	}
	active, err := count(reply.Streams, "active")
	if err != nil {
		return err
	}
	if _, err := count(reply.Streams, "xepg"); err != nil {
		return err
	}
	if active > all {
		return errors.New("xTeVe returned inconsistent stream counts; retry self-host setup xteve")
	}
	return nil
}

func verifyIdentity(ctx context.Context, h *helpers.H) error {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/discover.json", NoRedirect: true})
	if err != nil || response.Status != http.StatusOK {
		return errors.New("xTeVe discovery is unavailable; inspect self-host logs xteve and retry self-host setup xteve")
	}
	var identity struct {
		DeviceAuth string `json:"DeviceAuth"`
		DeviceID   string `json:"DeviceID"`
	}
	if response.Decode(&identity) != nil || identity.DeviceAuth != "xteve" || identity.DeviceID == "" {
		return errors.New("the service at the xTeVe Address did not identify itself as xTeVe; retry self-host setup xteve")
	}
	return nil
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	credential, err := selected(h)
	if err != nil {
		return err
	}
	if base, err := url.Parse(h.BaseURL); err != nil || base.Scheme != "http" {
		return manual("the native xTeVe Link requires the Catalog's internal HTTP Address; preserve existing HTTPS")
	}
	if err := verifyIdentity(ctx, h); err != nil {
		return err
	}
	login := map[string]string{"cmd": "login"}
	if !credential.none() {
		login["username"], login["password"] = credential.user, credential.password
	}
	session, err := call(ctx, h, login)
	if err != nil {
		return err
	}
	if !session.Status || session.Err != "" {
		return manual("xTeVe rejected the selected API credentials or the account lacks API permission")
	}
	if credential.none() && session.Token != "" {
		return manual("xTeVe API authentication is enabled; recover the matching API credentials instead of the unauthenticated mode")
	}
	if !credential.none() && session.Token == "" {
		return manual("xTeVe API authentication is disabled while credentials are saved; confirm the intended authentication mode without changing it for you")
	}
	reply, err := call(ctx, h, map[string]string{"cmd": "status", "token": session.Token})
	if err != nil {
		return err
	}
	if !reply.Status && reply.Err != "" {
		return manual("xTeVe rejected the API status request")
	}
	return verifyStatus(reply)
}
