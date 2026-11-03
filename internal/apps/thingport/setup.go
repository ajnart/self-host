package thingport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

type ownerSession struct {
	Token string `json:"token"`
	User  struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	} `json:"user"`
}

func ownerHint(h *helpers.H) string {
	return "Open " + h.URL + " privately and sign in as the instance owner selected by thingport.email, using the password shown by self-host credentials. " +
		"If the instance has no owner, create its first account there; open registration does not prove an empty instance. " +
		"Keep existing users, passwords, CAPTCHA, email verification and single sign-on settings. Resolve any recorded owner mismatch explicitly, then run: self-host setup thingport"
}

func tokenHint() string {
	return "Thingport has one API token per user; generating another invalidates its other consumers. " +
		"Keep existing tokens and permissions unchanged. This build cannot safely prepare and verify that token for Homarr. " +
		"Update to a self-host build with supported Thingport single-token recovery before retrying self-host setup thingport and self-host setup homarr; library counts or the token's last four characters do not prove its owner."
}

func loginSession(ctx context.Context, h *helpers.H) (ownerSession, error) {
	var session ownerSession
	email := h.Inputs["email"]
	if validateEmail(email) != nil || h.AdminPassword() == "" {
		return session, &helpers.ManualError{Hint: "Select the Thingport owner's email with the public thingport.email install input and retain the shared administrator password, then run: self-host setup thingport"}
	}
	raw, err := h.Remember("thingport-owner-session", func() (string, error) {
		response, err := h.HTTP(ctx, helpers.Request{
			Method: http.MethodPost, Path: "/api/login", NoRedirect: true,
			JSON: map[string]string{"email": strings.ToLower(email), "password": h.AdminPassword()},
		})
		if err != nil {
			return "", err
		}
		if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status == http.StatusBadRequest || response.Status >= 300 && response.Status < 400 {
			return "", &helpers.ManualError{Hint: ownerHint(h)}
		}
		if response.Status != http.StatusOK {
			return "", fmt.Errorf("Thingport login returned HTTP %d", response.Status)
		}
		var result ownerSession
		if response.Decode(&result) != nil {
			return "", errors.New("Thingport did not return a valid login response")
		}
		h.Log.Secret(result.Token)
		if result.Token == "" || result.User.ID == "" || strings.TrimSpace(result.User.ID) != result.User.ID {
			return "", errors.New("Thingport did not return a session and account identity")
		}
		if !strings.EqualFold(result.User.Email, email) || result.User.Role != "ADMIN" {
			return "", &helpers.ManualError{Hint: ownerHint(h)}
		}
		data, err := json.Marshal(result)
		return string(data), err
	})
	if err != nil {
		return session, err
	}
	if json.Unmarshal([]byte(raw), &session) != nil {
		return session, errors.New("Thingport's verified session is unavailable; retry Setup")
	}
	return session, nil
}

func verifiedOwner(ctx context.Context, h *helpers.H) (string, error) {
	session, err := loginSession(ctx, h)
	if err != nil {
		return "", err
	}
	if saved := h.Secret(h.Key("owner_id")); saved != "" && saved != session.User.ID {
		return "", &helpers.ManualError{Hint: ownerHint(h)}
	}
	response, err := h.HTTP(ctx, helpers.Request{
		Path: "/api/admin/users", NoRedirect: true,
		Header: map[string]string{"Authorization": "Bearer " + session.Token},
	})
	if err != nil {
		return "", err
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status >= 300 && response.Status < 400 {
		return "", &helpers.ManualError{Hint: ownerHint(h)}
	}
	if response.Status != http.StatusOK {
		return "", fmt.Errorf("Thingport owner verification returned HTTP %d", response.Status)
	}
	var users *[]struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Role    string `json:"role"`
		IsOwner *bool  `json:"is_owner"`
	}
	if response.Decode(&users) != nil || users == nil {
		return "", errors.New("Thingport did not return its administrator account inventory")
	}
	owners, matches := 0, 0
	selected := false
	for _, user := range *users {
		if user.IsOwner != nil && *user.IsOwner {
			owners++
		}
		if user.ID == session.User.ID {
			matches++
			selected = strings.EqualFold(user.Email, h.Inputs["email"]) && user.Role == "ADMIN" && user.IsOwner != nil && *user.IsOwner
		}
	}
	if owners != 1 || matches != 1 || !selected {
		return "", &helpers.ManualError{Hint: ownerHint(h)}
	}
	return session.User.ID, nil
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/health", helpers.WantStatus(http.StatusOK)); err != nil {
		return err
	}
	if err := h.Step(ctx, "owner", "verify the selected Thingport instance owner", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			id, err := verifiedOwner(ctx, h)
			return err == nil && h.Secret(h.Key("owner_id")) == id, err
		},
		Do: func(ctx context.Context) error {
			id, err := verifiedOwner(ctx, h)
			if err != nil {
				return err
			}
			if h.Secret(h.Key("owner_id")) != "" {
				return nil
			}
			return h.SetSecret(h.Key("owner_id"), id)
		},
		Hint: ownerHint(h), Credential: "admin",
	}); err != nil {
		return err
	}
	if h.Peer("homarr") != nil {
		return h.Step(ctx, "homarr-token", "review Thingport's single-token prerequisite", helpers.StepSpec{
			Do:   func(context.Context) error { return &helpers.ManualError{Hint: tokenHint()} },
			Hint: tokenHint(),
		})
	}
	return nil
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if h.Secret(h.Key("owner_id")) == "" {
		return &helpers.ManualError{Hint: ownerHint(h)}
	}
	_, err := verifiedOwner(ctx, h)
	return err
}
