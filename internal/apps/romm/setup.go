package romm

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

type account struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Enabled  *bool  `json:"enabled"`
	Role     string `json:"role"`
}

func (a account) accepts(user string) bool {
	return a.ID > 0 && a.Username == user && a.Enabled != nil && *a.Enabled && a.Role == "admin"
}

func accountHint(h *helpers.H, user string) string {
	return "Open " + h.URL + " and verify the existing RomM administrator " + user +
		" with the Admin login shown by self-host credentials. If RomM confirms that no administrator exists, finish its first-admin page, then run self-host setup romm. Preserve existing users, profiles, library paths and credentials; do not reset accounts, disable authentication or delete the database."
}

func authenticate(ctx context.Context, h *helpers.H, user, password string) (account, bool, error) {
	hold := func() (account, bool, error) {
		return account{}, false, &helpers.ManualError{Hint: accountHint(h, user)}
	}
	if user == "" || password == "" {
		return hold()
	}
	anonymous, err := h.HTTP(ctx, helpers.Request{Path: "/api/users/me", NoRedirect: true})
	if err != nil || anonymous.Status != http.StatusUnauthorized {
		if ctx.Err() != nil {
			return account{}, false, ctx.Err()
		}
		return hold()
	}
	response, err := h.HTTP(ctx, helpers.Request{
		Path: "/api/users/me", User: user, Password: password, NoRedirect: true,
	})
	if err != nil {
		if ctx.Err() != nil {
			return account{}, false, ctx.Err()
		}
		return hold()
	}
	if response.Status == http.StatusUnauthorized {
		return account{}, false, nil
	}
	var identity account
	if response.Status != http.StatusOK || response.Decode(&identity) != nil || !identity.accepts(user) {
		return hold()
	}
	return identity, true, nil
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,255}$`)

func firstAdmin() firstadmin.FirstAdmin {
	var loginErr error
	var createdID int64
	return firstadmin.FirstAdmin{
		Ready: firstadmin.Probe{
			Path: "/api/heartbeat", Want: []int{http.StatusOK},
			Request: func(*helpers.H) helpers.Request {
				return helpers.Request{Path: "/api/heartbeat", NoRedirect: true}
			},
		},
		Username: func(_ context.Context, h *helpers.H) (string, error) {
			return adminUser(h), nil
		},
		Title: func(_ *helpers.H, user string) string { return "verify the RomM administrator " + user },
		Hint:  accountHint,
		Valid: func(user, password string) error {
			if !usernamePattern.MatchString(user) || len(password) < 6 || len(password) > 255 || strings.TrimSpace(password) == "" {
				return &helpers.ManualError{Hint: "RomM requires a 3–255 character username containing only letters, digits, underscores or hyphens and a 6–255 character ASCII password. Preserve the shared Admin login and complete the account recovery before retrying self-host setup romm."}
			}
			for _, character := range password {
				if character > 127 {
					return &helpers.ManualError{Hint: "RomM requires an ASCII password. Preserve existing credentials and review the Admin login before retrying self-host setup romm."}
				}
			}
			return nil
		},
		Login: func(ctx context.Context, h *helpers.H, user, password string) (bool, error) {
			identity, accepted, err := authenticate(ctx, h, user, password)
			if err == nil && createdID > 0 && (!accepted || identity.ID != createdID) {
				err = &helpers.ManualError{Hint: "RomM did not verify the same administrator identity after creation. Preserve the account and database and review the existing identity before retrying self-host setup romm."}
			}
			loginErr = err
			return accepted && err == nil, err
		},
		Create: func(ctx context.Context, h *helpers.H, user, password string) error {
			if loginErr != nil {
				return loginErr
			}
			session := h.Session()
			response, err := session.HTTP(ctx, helpers.Request{Path: "/api/heartbeat", NoRedirect: true})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var heartbeat struct {
				System struct {
					Version string `json:"VERSION"`
					Setup   *bool  `json:"SHOW_SETUP_WIZARD"`
				} `json:"SYSTEM"`
			}
			if err != nil || response.Status != http.StatusOK || response.Decode(&heartbeat) != nil ||
				!helpers.VersionCompatible(heartbeat.System.Version, "5.4.1") || heartbeat.System.Setup == nil || !*heartbeat.System.Setup {
				return &helpers.ManualError{Hint: accountHint(h, user)}
			}
			csrf := session.Cookie("romm_csrftoken")
			if csrf == "" {
				return &helpers.ManualError{Hint: "RomM did not provide its first-admin CSRF cookie. Keep CSRF protection enabled, finish the first administrator in the browser and retry self-host setup romm."}
			}
			response, err = session.HTTP(ctx, helpers.Request{
				Method: http.MethodPost, Path: "/api/users", NoRedirect: true,
				Header: map[string]string{"X-CSRFToken": csrf},
				JSON:   map[string]string{"username": user, "password": password, "email": "", "role": "admin"},
			})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var identity account
			if err != nil || response.Status != http.StatusCreated || response.Decode(&identity) != nil || !identity.accepts(user) {
				return &helpers.ManualError{Hint: "RomM administrator creation could not be confirmed. Sign in with the existing Admin login or review the first-admin page, preserve users and credentials, then retry self-host setup romm. Do not reset or replace an account after an uncertain response."}
			}
			createdID = identity.ID
			return nil
		},
	}
}
