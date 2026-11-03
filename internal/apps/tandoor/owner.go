package tandoor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ajnart/self-host/internal/helpers"
)

const ownerRecoveryHint = "Open Tandoor and verify the intended administrator using the Admin login shown privately by self-host credentials. For a known new installation, create the first administrator in Tandoor's setup page yourself; otherwise recover the existing account explicitly. Finish any email verification or MFA without disabling it. Select an existing Space owned by that administrator, then run self-host setup tandoor. Owner verification never creates accounts or Spaces, resets passwords or switches Spaces."

const throttleHint = "Tandoor rate-limited verification (HTTP 429). Wait at least one minute for its quota to reset, then retry self-host setup tandoor. Keep existing accounts, Spaces and credentials unchanged."

type account struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	IsActive    *bool  `json:"is_active"`
	IsSuperuser *bool  `json:"is_superuser"`
}

func (a account) matches(id int64, username string) bool {
	return a.ID == id && a.Username == username && a.IsActive != nil && *a.IsActive && a.IsSuperuser != nil && *a.IsSuperuser
}

func ownerSession(ctx context.Context, h *helpers.H) (string, int64, error) {
	value, err := h.Remember("tandoor-owner-session", func() (string, error) {
		token, id, err := loginOwner(ctx, h)
		if err != nil {
			return "", err
		}
		body, err := json.Marshal(struct {
			Token string
			ID    int64
		}{Token: token, ID: id})
		return string(body), err
	})
	if err != nil {
		return "", 0, err
	}
	var session struct {
		Token string
		ID    int64
	}
	if json.Unmarshal([]byte(value), &session) != nil {
		return "", 0, errors.New("Tandoor temporary session could not be read")
	}
	return session.Token, session.ID, nil
}

func loginOwner(ctx context.Context, h *helpers.H) (string, int64, error) {
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return "", 0, &helpers.ManualError{Hint: ownerRecoveryHint}
	}
	h.Log.Secret(h.AdminPassword())
	resp, err := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/_allauth/app/v1/auth/login", NoRedirect: true,
		JSON: map[string]string{"username": h.AdminUser(), "password": h.AdminPassword()},
	})
	if err != nil {
		return "", 0, errors.New("Tandoor administrator sign-in request failed")
	}
	if resp.Status == http.StatusTooManyRequests {
		return "", 0, &helpers.ManualError{Hint: throttleHint}
	}
	if resp.Status >= 300 && resp.Status < 500 {
		return "", 0, &helpers.ManualError{Hint: ownerRecoveryHint}
	}
	if resp.Status != http.StatusOK {
		return "", 0, fmt.Errorf("Tandoor administrator sign-in returned HTTP %d", resp.Status)
	}
	var login struct {
		Status int `json:"status"`
		Data   struct {
			User struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"data"`
		Meta struct {
			Authenticated *bool  `json:"is_authenticated"`
			Token         string `json:"session_token"`
		} `json:"meta"`
	}
	if json.Unmarshal(resp.Body, &login) != nil {
		return "", 0, errors.New("Tandoor did not return a valid administrator sign-in response")
	}
	h.Log.Secret(login.Meta.Token)
	if login.Status != http.StatusOK || login.Meta.Authenticated == nil || !*login.Meta.Authenticated || login.Meta.Token == "" || login.Data.User.ID <= 0 || login.Data.User.Username != h.AdminUser() {
		return "", 0, &helpers.ManualError{Hint: ownerRecoveryHint}
	}
	return login.Meta.Token, login.Data.User.ID, nil
}

func ownerRead(ctx context.Context, h *helpers.H, token, path string, value any) error {
	return authenticatedRead(ctx, h, map[string]string{"X-Session-Token": token}, path, value, ownerRecoveryHint)
}

func authenticatedRead(ctx context.Context, h *helpers.H, header map[string]string, path string, value any, hint string) error {
	resp, err := h.HTTP(ctx, helpers.Request{
		Path: path, NoRedirect: true, Header: header,
	})
	if err != nil {
		return errors.New("Tandoor administrator and Space verification request failed")
	}
	if resp.Status != http.StatusOK {
		if resp.Status == http.StatusTooManyRequests {
			return &helpers.ManualError{Hint: throttleHint}
		}
		if resp.Status >= 300 && resp.Status < 500 {
			return &helpers.ManualError{Hint: hint}
		}
		return fmt.Errorf("Tandoor administrator and Space verification returned HTTP %d", resp.Status)
	}
	if json.Unmarshal(resp.Body, value) != nil {
		return errors.New("Tandoor did not return valid administrator and Space metadata")
	}
	return nil
}

func verifyOwner(ctx context.Context, h *helpers.H) error {
	token, id, err := ownerSession(ctx, h)
	if err != nil {
		return err
	}
	var memberships, current json.RawMessage
	if err := ownerRead(ctx, h, token, "/api/user-space/all_personal/", &memberships); err != nil {
		return err
	}
	if err := ownerRead(ctx, h, token, "/api/space/current/", &current); err != nil {
		return err
	}
	_, err = accountSpace(h, id, memberships, current)
	return err
}

func accountSpace(h *helpers.H, id int64, personal, current json.RawMessage) (int64, error) {
	var memberships []struct {
		ID     int64   `json:"id"`
		Space  int64   `json:"space"`
		User   account `json:"user"`
		Active *bool   `json:"active"`
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	if json.Unmarshal(personal, &memberships) != nil {
		return 0, errors.New("Tandoor did not return valid administrator and Space metadata")
	}
	seenIDs, seenSpaces := map[int64]bool{}, map[int64]bool{}
	var activeSpace int64
	for _, membership := range memberships {
		if membership.ID <= 0 || membership.Space <= 0 || membership.Active == nil || seenIDs[membership.ID] || seenSpaces[membership.Space] || !membership.User.matches(id, h.AdminUser()) {
			return 0, &helpers.ManualError{Hint: ownerRecoveryHint}
		}
		seenIDs[membership.ID], seenSpaces[membership.Space] = true, true
		if !*membership.Active {
			continue
		}
		if activeSpace != 0 {
			return 0, &helpers.ManualError{Hint: ownerRecoveryHint}
		}
		admin := false
		for _, group := range membership.Groups {
			if group.Name == "admin" {
				admin = true
			}
		}
		if !admin {
			return 0, &helpers.ManualError{Hint: ownerRecoveryHint}
		}
		activeSpace = membership.Space
	}
	if activeSpace == 0 {
		return 0, &helpers.ManualError{Hint: ownerRecoveryHint}
	}
	var space struct {
		ID        int64   `json:"id"`
		CreatedBy account `json:"created_by"`
	}
	if json.Unmarshal(current, &space) != nil {
		return 0, errors.New("Tandoor did not return valid administrator and Space metadata")
	}
	if space.ID != activeSpace || !space.CreatedBy.matches(id, h.AdminUser()) {
		return 0, &helpers.ManualError{Hint: ownerRecoveryHint}
	}
	return activeSpace, nil
}
