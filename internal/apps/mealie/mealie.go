package mealie

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

var identityID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (App) Info() app.Info {
	return app.Info{
		ID: "mealie", Name: "Mealie", Category: "Productivity",
		Description:   "Keep your recipes together and plan what to cook",
		Image:         "ghcr.io/mealie-recipes/mealie:latest",
		Docs:          "https://docs.mealie.io/documentation/getting-started/installation/sqlite/",
		Port:          9925,
		ContainerPort: 9000,
		Service:       "mealie",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "admin_user_id", Prompt: "Existing Mealie administrator ID", Private: true,
			Help: "Explicitly select the existing administrator's UUID from Mealie's authenticated user information. This verifies the shared Admin login without adopting, creating or changing an account."},
		{Key: "group_id", Prompt: "Existing Mealie group ID", Private: true,
			Help: "Select the intended administrator's existing group UUID. Empty inputs preserve saved values; Setup never creates or moves a group."},
		{Key: "household_id", Prompt: "Existing Mealie household ID", Private: true,
			Help: "Select the intended administrator's existing household UUID. Empty inputs preserve saved values; Setup never creates or moves a household."},
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  mealie:
    image: ghcr.io/mealie-recipes/mealie:latest
    restart: unless-stopped
    mem_limit: 1000m
    environment:
      DB_ENGINE: sqlite
      ALLOW_SIGNUP: "false"
      PUID: ${PUID}
      PGID: ${PGID}
      TZ: ${TZ}
    volumes:
      - ./data:/app/data
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:9000
    healthcheck:
      test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/api/app/about"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 30s
`
}

func recoveryHint(h *helpers.H) string {
	return "Open " + h.URL + " and verify the intended existing administrator and household. Finish first-account setup manually; self-host does not claim the factory account or change passwords. Use the shared Admin login shown by self-host credentials. Supply the confirmed UUIDs through private inputs mealie.admin_user_id, mealie.group_id and mealie.household_id using approved install --set-file and owner-only files, then run self-host setup mealie. Preserve existing Compose authentication, mounts and ports and separately approve any reinstall replacement first. Do not reset users, households or recipes to make verification pass."
}

func manual(h *helpers.H, reason string) error {
	return &helpers.ManualError{Hint: reason + " " + recoveryHint(h)}
}

func verifyAPI(ctx context.Context, h *helpers.H) error {
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/app/about", NoRedirect: true})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Mealie application information returned HTTP %d", resp.Status)
	}
	var about struct {
		Version            string `json:"version"`
		Production         *bool  `json:"production"`
		Demo               *bool  `json:"demoStatus"`
		AllowPasswordLogin *bool  `json:"allowPasswordLogin"`
	}
	if resp.Decode(&about) != nil || !helpers.VersionCompatible(about.Version, "3.30.0") || about.Production == nil || !*about.Production || about.Demo == nil || *about.Demo {
		return manual(h, "Mealie did not return a supported production API identity; review its image and version without changing existing data.")
	}
	if about.AllowPasswordLogin == nil || !*about.AllowPasswordLogin {
		return manual(h, "Mealie password authentication is unavailable; preserve the selected authentication method and review access manually.")
	}
	return nil
}

func sessionToken(ctx context.Context, h *helpers.H) (string, error) {
	if strings.TrimSpace(h.AdminUser()) == "" || h.AdminPassword() == "" {
		return "", manual(h, "The shared Admin login is unavailable; no credentials were guessed or replaced.")
	}
	h.Log.Secret(h.AdminPassword())
	token, err := h.Remember("mealie-admin-session", func() (string, error) {
		resp, err := h.HTTP(ctx, helpers.Request{
			Method: http.MethodPost, Path: "/api/auth/token", NoRedirect: true,
			Form: url.Values{"username": {h.AdminUser()}, "password": {h.AdminPassword()}},
		})
		if err != nil {
			return "", h.Log.RedactError(err)
		}
		if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
			return "", nil
		}
		if resp.Status != http.StatusOK {
			return "", fmt.Errorf("Mealie login returned HTTP %d; no account changes were attempted", resp.Status)
		}
		var session struct {
			Token string `json:"access_token"`
			Type  string `json:"token_type"`
		}
		decodeErr := resp.Decode(&session)
		h.Log.Secret(session.Token)
		if decodeErr != nil || strings.TrimSpace(session.Token) == "" || !strings.EqualFold(session.Type, "bearer") {
			return "", errors.New("Mealie did not return a valid login session")
		}
		return session.Token, nil
	})
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", manual(h, "Mealie rejected the saved Admin login; it was preserved and no factory credentials were tried.")
	}
	return token, nil
}

func verifyIdentity(ctx context.Context, h *helpers.H) error {
	selected := map[string]string{}
	for _, key := range []string{"admin_user_id", "group_id", "household_id"} {
		value := h.Secret(h.Key(key))
		h.Log.Secret(value)
		if !identityID.MatchString(value) {
			return manual(h, "The intended existing Mealie administrator, group and household must be explicitly selected with valid UUIDs.")
		}
		selected[key] = value
	}
	if err := verifyAPI(ctx, h); err != nil {
		return err
	}
	token, err := sessionToken(ctx, h)
	if err != nil {
		return err
	}
	header := (helpers.KeyScheme{Header: "Authorization", Prefix: "Bearer "}).Headers(token)
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/users/self", NoRedirect: true, Header: header})
	if err != nil {
		return h.Log.RedactError(err)
	}
	if resp.Status != http.StatusOK {
		return manual(h, "Mealie did not accept the administrator's authenticated identity; no account changes were attempted.")
	}
	var user struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		Email       string `json:"email"`
		Admin       *bool  `json:"admin"`
		GroupID     string `json:"groupId"`
		HouseholdID string `json:"householdId"`
		Group       string `json:"group"`
		Household   string `json:"household"`
	}
	if resp.Decode(&user) != nil || user.Admin == nil || !*user.Admin || strings.TrimSpace(user.Username) == "" || strings.TrimSpace(user.Email) == "" || strings.TrimSpace(user.Group) == "" || strings.TrimSpace(user.Household) == "" {
		return manual(h, "Mealie did not return a complete administrator identity.")
	}
	if strings.EqualFold(user.Email, "changeme@example.com") {
		return manual(h, "Mealie still has the factory administrator; its presence is not proof of a new or owned account.")
	}
	if !strings.EqualFold(user.ID, selected["admin_user_id"]) || !strings.EqualFold(user.GroupID, selected["group_id"]) || !strings.EqualFold(user.HouseholdID, selected["household_id"]) || !strings.EqualFold(user.Username, h.AdminUser()) && !strings.EqualFold(user.Email, h.AdminUser()) {
		return manual(h, "Mealie's authenticated administrator does not match the explicitly selected account, group and household.")
	}
	resp, err = h.HTTP(ctx, helpers.Request{Path: "/api/households/self", NoRedirect: true, Header: header})
	if err != nil {
		return h.Log.RedactError(err)
	}
	if resp.Status != http.StatusOK {
		return manual(h, "Mealie did not return the authenticated administrator's household.")
	}
	var household struct {
		ID      string `json:"id"`
		GroupID string `json:"groupId"`
		Name    string `json:"name"`
		Group   string `json:"group"`
	}
	if resp.Decode(&household) != nil || !strings.EqualFold(household.ID, selected["household_id"]) || !strings.EqualFold(household.GroupID, selected["group_id"]) || household.Name != user.Household || household.Group != user.Group {
		return manual(h, "Mealie's household information does not match the selected administrator's existing household and group.")
	}
	return nil
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/app/about", helpers.WantStatus(http.StatusOK), helpers.NoRedirect()); err != nil {
		return err
	}
	return h.Step(ctx, "admin", "verify the existing Mealie administrator and household", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := verifyIdentity(ctx, h)
			return err == nil, err
		},
		Do:         func(ctx context.Context) error { return verifyIdentity(ctx, h) },
		Hint:       recoveryHint(h),
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error { return verifyIdentity(ctx, h) }

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "Mealie uses SQLite on local storage; keep the complete data folder across updates and removal. Finish the initial administrator in Mealie manually and keep authentication enabled. Initial-account conversion, API-token issuance and native Homarr configuration are not automated by this App slice."
}
