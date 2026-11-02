package karakeep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

type ownerIdentity struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func ownerManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + ownerRecoveryHint}
}

func ownerEmail(h *helpers.H) (string, error) {
	email := h.Secret(h.Key("admin_email"))
	if email == "" {
		email = (App{}).AdminEmail(h.AdminUser())
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", ownerManual("Select the existing administrator's email through the private karakeep.admin_email input.")
	}
	return strings.ToLower(email), nil
}

func matchesOwner(owner *ownerIdentity, email string) bool {
	return owner != nil && strings.TrimSpace(owner.ID) != "" && owner.ID == strings.TrimSpace(owner.ID) && strings.EqualFold(owner.Email, email) && owner.Role == "admin"
}

func hasBodyError(value json.RawMessage) bool {
	return len(value) != 0 && string(value) != "null"
}

func readOwner(ctx context.Context, h *helpers.H) (owner ownerIdentity, err error) {
	email, err := ownerEmail(h)
	if err != nil {
		return owner, err
	}
	if h.AdminPassword() == "" {
		return owner, ownerManual("The Admin password is missing; no empty credential was submitted.")
	}
	base, err := url.Parse(h.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Scheme != "http" && base.Scheme != "https" {
		return owner, ownerManual("Karakeep's private API Address is invalid; review the selected Access without bypassing TLS.")
	}
	origin := base.Scheme + "://" + base.Host
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h.Log.Secret(email, h.AdminPassword())
	session := h.Session()
	response, err := session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/auth/sign-in/email", NoRedirect: true,
		Header: map[string]string{"Origin": origin},
		JSON:   map[string]string{"email": email, "password": h.AdminPassword()},
	})
	if err != nil {
		return owner, errors.New("Karakeep administrator sign-in request failed; check the private Address and trusted TLS")
	}
	for _, cookie := range (&http.Response{Header: response.Header}).Cookies() {
		h.Log.Secret(cookie.Value)
	}
	if response.Status != http.StatusOK {
		return owner, ownerManual(fmt.Sprintf("Karakeep sign-in returned HTTP %d; verify the existing account, authentication policy and supported API before retrying.", response.Status))
	}
	defer func() {
		err = errors.Join(err, closeSession(ctx, session, origin))
	}()
	var login struct {
		Token string          `json:"token"`
		User  *ownerIdentity  `json:"user"`
		Error json.RawMessage `json:"error"`
	}
	decodeErr := response.Decode(&login)
	h.Log.Secret(login.Token)
	if decodeErr != nil || strings.TrimSpace(login.Token) == "" || hasBodyError(login.Error) || !matchesOwner(login.User, email) {
		return owner, ownerManual("Karakeep did not positively identify the selected local administrator at sign-in.")
	}
	owner, err = readSessionOwner(ctx, h, session, login.Token, email)
	if err != nil {
		return ownerIdentity{}, err
	}
	if owner.ID != login.User.ID {
		return ownerIdentity{}, ownerManual("Karakeep's authenticated session does not match its sign-in identity.")
	}
	if recorded := h.Secret(h.Key("account_id")); recorded != "" && owner.ID != recorded {
		return ownerIdentity{}, ownerManual("The authenticated Karakeep account differs from the recorded administrator; its ID and credentials were preserved.")
	}
	return owner, nil
}

func readSessionOwner(ctx context.Context, h *helpers.H, session *helpers.Session, token, email string) (ownerIdentity, error) {
	response, err := session.HTTP(ctx, helpers.Request{Path: "/api/auth/get-session", NoRedirect: true})
	if err != nil {
		return ownerIdentity{}, errors.New("Karakeep session verification request failed")
	}
	var current struct {
		User    *ownerIdentity `json:"user"`
		Session *struct {
			UserID    string    `json:"userId"`
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expiresAt"`
		} `json:"session"`
		Error json.RawMessage `json:"error"`
	}
	decodeErr := response.Decode(&current)
	if current.Session != nil {
		h.Log.Secret(current.Session.Token)
	}
	if response.Status != http.StatusOK || decodeErr != nil || hasBodyError(current.Error) || !matchesOwner(current.User, email) || current.Session == nil || current.Session.UserID != current.User.ID || current.Session.Token != token || !current.Session.ExpiresAt.After(h.Now()) {
		return ownerIdentity{}, ownerManual("Karakeep did not return a valid authenticated administrator session.")
	}
	response, err = session.HTTP(ctx, helpers.Request{Path: "/api/v1/users/me", NoRedirect: true})
	if err != nil {
		return ownerIdentity{}, errors.New("Karakeep account verification request failed")
	}
	var profile struct {
		ID        string          `json:"id"`
		Email     string          `json:"email"`
		LocalUser *bool           `json:"localUser"`
		Error     json.RawMessage `json:"error"`
	}
	if response.Status != http.StatusOK || response.Decode(&profile) != nil || hasBodyError(profile.Error) || profile.ID != current.User.ID || !strings.EqualFold(profile.Email, email) || profile.LocalUser == nil || !*profile.LocalUser {
		return ownerIdentity{}, ownerManual("Karakeep's authenticated account API did not confirm the selected local administrator.")
	}
	return *current.User, nil
}

func closeSession(ctx context.Context, session *helpers.Session, origin string) error {
	response, err := session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/auth/sign-out", NoRedirect: true,
		Header: map[string]string{"Origin": origin}, JSON: map[string]string{},
	})
	if err != nil {
		return errors.New("Karakeep could not close its temporary verification session")
	}
	var result struct {
		Success *bool           `json:"success"`
		Error   json.RawMessage `json:"error"`
	}
	if response.Status != http.StatusOK || response.Decode(&result) != nil || result.Success == nil || !*result.Success || hasBodyError(result.Error) {
		return errors.New("Karakeep did not confirm closing its temporary verification session")
	}
	return nil
}
