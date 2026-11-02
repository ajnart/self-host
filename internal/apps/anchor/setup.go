package anchor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

type administratorIdentity struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type administratorSession struct {
	Token    string                `json:"token"`
	Identity administratorIdentity `json:"identity"`
}

type administratorUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	IsAdmin *bool  `json:"isAdmin"`
	Status  string `json:"status"`
}

func administratorRecovery(h *helpers.H) string {
	return "Open " + h.URL + ". Only on a genuinely new instance, deliberately create the intended first administrator with the Admin login shown by self-host credentials. For an existing instance, restore its intended login and known matching anchor.admin_email / anchor.admin_identity through private install --set-file inputs. Installing replaces Compose: preserve existing mounts, image/auth/Access settings and separately approve replacement; do not register a replacement, rename or reset another account, or weaken authentication. Then run: self-host setup anchor"
}

func administratorManual(h *helpers.H, reason string) error {
	return &helpers.ManualError{Hint: reason + " " + administratorRecovery(h)}
}

func administratorSetup() firstadmin.FirstAdmin {
	var loginError error
	return firstadmin.FirstAdmin{
		Ready: firstadmin.Probe{Path: "/api/health", Want: []int{http.StatusOK}, Request: func(*helpers.H) helpers.Request {
			return helpers.Request{Path: "/api/health", NoRedirect: true}
		}},
		Username: func(_ context.Context, h *helpers.H) (string, error) {
			return administratorEmail(h), nil
		},
		Login: func(ctx context.Context, h *helpers.H, email, password string) (bool, error) {
			loginError = checkHealth(ctx, h)
			if loginError == nil {
				_, loginError = inspectAdministrator(ctx, h, email, password)
			}
			return loginError == nil, loginError
		},
		Create: func(_ context.Context, h *helpers.H, _, _ string) error {
			if loginError != nil {
				return loginError
			}
			return administratorManual(h, "Anchor first-account creation is Manual because fresh owned account storage is not established.")
		},
		Title: func(*helpers.H, string) string { return "verify the intended Anchor administrator" },
		Hint:  func(h *helpers.H, _ string) string { return administratorRecovery(h) },
	}
}

func checkHealth(ctx context.Context, h *helpers.H) error {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/health", NoRedirect: true})
	if err != nil || response.Status != http.StatusOK {
		return errors.New("Anchor health is unavailable; inspect self-host logs anchor and retry Setup")
	}
	var health struct {
		App     string `json:"app"`
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if response.Decode(&health) != nil || health.App != "anchor" || health.Status != "ok" || strings.TrimSpace(health.Version) == "" {
		return errors.New("the selected endpoint did not return Anchor health and version; preserve the Project and inspect its configured port")
	}
	return nil
}

func validEmail(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && parsed.Name == "" && strings.TrimSpace(value) == value
}

func administratorEmail(h *helpers.H) string {
	email := h.Secret(h.Key("admin_email"))
	if email == "" {
		email = (App{}).AdminEmail(h.AdminUser())
	}
	h.Log.Secret(email)
	return email
}

func savedAdministrator(h *helpers.H) (administratorIdentity, bool, error) {
	raw := h.Secret(h.Key("admin_identity"))
	if raw == "" {
		return administratorIdentity{}, false, nil
	}
	h.Log.Secret(raw)
	var identity administratorIdentity
	if json.Unmarshal([]byte(raw), &identity) != nil || strings.TrimSpace(identity.ID) == "" || identity.ID != strings.TrimSpace(identity.ID) || !validEmail(identity.Email) {
		return administratorIdentity{}, false, administratorManual(h, "Anchor's saved administrator selection is incomplete; it was preserved.")
	}
	return identity, true, nil
}

func validAdministrator(user administratorUser, email string) bool {
	return strings.TrimSpace(user.ID) != "" && user.ID == strings.TrimSpace(user.ID) && user.Email == email && user.IsAdmin != nil && *user.IsAdmin && user.Status == "active"
}

func inspectAdministrator(ctx context.Context, h *helpers.H, email, password string) (administratorSession, error) {
	if !validEmail(email) {
		return administratorSession{}, administratorManual(h, "Anchor needs the intended administrator's valid email selection.")
	}
	saved, exists, err := savedAdministrator(h)
	if err != nil {
		return administratorSession{}, err
	}
	if exists && saved.Email != email {
		return administratorSession{}, administratorManual(h, "Anchor's selected email differs from its saved account; neither account was changed.")
	}
	if password == "" {
		return administratorSession{}, administratorManual(h, "The intended Anchor Admin password is unavailable.")
	}
	h.Log.Secret(password)
	cacheKey := fmt.Sprintf("anchor-administrator-session:%x", sha256.Sum256([]byte(email+"\x00"+password)))
	raw, err := h.Remember(cacheKey, func() (string, error) {
		response, err := h.HTTP(ctx, helpers.Request{
			Method: http.MethodPost, Path: "/api/auth/login", NoRedirect: true,
			JSON: map[string]string{"email": email, "password": password},
		})
		if err != nil || response.Status != http.StatusOK {
			return "", administratorManual(h, "Anchor did not accept the intended administrator login; no account was created or changed.")
		}
		var login struct {
			AccessToken  string            `json:"access_token"`
			RefreshToken string            `json:"refresh_token"`
			User         administratorUser `json:"user"`
		}
		decodeErr := response.Decode(&login)
		h.Log.Secret(login.AccessToken, login.RefreshToken)
		if decodeErr != nil || strings.TrimSpace(login.AccessToken) == "" || !validAdministrator(login.User, email) {
			return "", administratorManual(h, "Anchor did not return a verified active administrator login.")
		}
		encoded, err := json.Marshal(administratorSession{Token: login.AccessToken, Identity: administratorIdentity{ID: login.User.ID, Email: login.User.Email}})
		if err != nil {
			return "", errors.New("cannot retain the verified Anchor login in this Setup invocation")
		}
		h.Log.Secret(string(encoded))
		return string(encoded), nil
	})
	if err != nil {
		return administratorSession{}, err
	}
	var session administratorSession
	if json.Unmarshal([]byte(raw), &session) != nil || session.Token == "" || session.Identity.Email != email {
		return administratorSession{}, administratorManual(h, "Anchor's current login evidence could not be verified.")
	}
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/auth/me", NoRedirect: true, Header: map[string]string{"Authorization": "Bearer " + session.Token}})
	if err != nil || response.Status != http.StatusOK {
		return administratorSession{}, administratorManual(h, "Anchor could not verify the JWT-authenticated administrator identity.")
	}
	var current administratorUser
	if response.Decode(&current) != nil || !validAdministrator(current, email) || current.ID != session.Identity.ID || exists && current.ID != saved.ID {
		return administratorSession{}, administratorManual(h, "Anchor authenticated a different or incomplete administrator identity; the saved selection was preserved.")
	}
	return session, nil
}

func selectedAdministrator(ctx context.Context, h *helpers.H) (administratorSession, error) {
	return inspectAdministrator(ctx, h, administratorEmail(h), h.AdminPassword())
}

func rememberAdministrator(ctx context.Context, h *helpers.H) error {
	return h.Step(ctx, "admin-identity", "remember the verified existing Anchor administrator", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			_, exists, err := savedAdministrator(h)
			if err != nil || !exists {
				return false, err
			}
			_, err = selectedAdministrator(ctx, h)
			return err == nil, err
		},
		Do: func(ctx context.Context) error {
			session, err := selectedAdministrator(ctx, h)
			if err != nil {
				return err
			}
			_, exists, err := savedAdministrator(h)
			if err != nil || exists {
				return err
			}
			encoded, err := json.Marshal(session.Identity)
			if err != nil {
				return errors.New("cannot record the verified Anchor administrator selection")
			}
			return h.SetSecret(h.Key("admin_identity"), string(encoded))
		},
		Hint: administratorRecovery(h),
	})
}

func checkNotes(ctx context.Context, h *helpers.H) error {
	session, err := selectedAdministrator(ctx, h)
	if err != nil {
		return err
	}
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/notes?limit=1", NoRedirect: true, Header: map[string]string{"Authorization": "Bearer " + session.Token}})
	if err != nil || response.Status != http.StatusOK {
		return &helpers.ManualError{Hint: "Anchor's verified administrator could not read notes. Preserve existing notes and authentication, inspect the account's read access, then run: self-host setup anchor"}
	}
	var notes []struct {
		ID         string    `json:"id"`
		Title      *string   `json:"title"`
		UpdatedAt  string    `json:"updatedAt"`
		IsPinned   *bool     `json:"isPinned"`
		TagIDs     []*string `json:"tagIds"`
		Permission string    `json:"permission"`
	}
	if response.Decode(&notes) != nil || notes == nil || len(notes) > 1 {
		return errors.New("Anchor returned incomplete note data; no note content was changed or included in diagnostics")
	}
	for _, note := range notes {
		_, timestampErr := time.Parse(time.RFC3339Nano, note.UpdatedAt)
		if strings.TrimSpace(note.ID) == "" || note.Title == nil || note.IsPinned == nil || note.TagIDs == nil || timestampErr != nil || note.Permission != "owner" && note.Permission != "viewer" && note.Permission != "editor" {
			return errors.New("Anchor returned invalid note metadata; preserve the notes and inspect the upstream API")
		}
		for _, tag := range note.TagIDs {
			if tag == nil {
				return errors.New("Anchor returned invalid note tags; preserve the notes and inspect the upstream API")
			}
		}
	}
	return nil
}
