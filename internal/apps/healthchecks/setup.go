package healthchecks

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const accountHint = "Healthchecks account bootstrap is Manual in this App-only slice. On a confirmed fresh instance, use the official image's interactive createsuperuser command; never put passwords in command arguments. For an existing account, recover the known Admin login through self-host credentials and Healthchecks Account Settings without replacing users or disabling two-factor authentication. Then retry self-host setup healthchecks."
const projectHint = "Choose the exact project owned by the Admin login and supply its UUID as healthchecks.project_id. Preserve the existing project and review Compose before any reinstall used to change inputs. Then retry self-host setup healthchecks; Setup never creates or selects a different project."
const keyHint = "Healthchecks read-only key issuance awaits the reviewed shared helper. Restore the selected project's known read-only API key through the Project's private-input boundary; review and separately approve any reinstall because it can replace Compose. If the secret is lost or rejected, recover its matching Backup or explicitly review that project's read-only key in Project Settings. Do not replace write keys, ping keys or notification settings. Then retry self-host setup healthchecks."

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := (App{}).Preflight(ctx, h); err != nil {
		return err
	}
	if err := h.Ready(ctx, "/health", helpers.WantStatus(http.StatusOK), helpers.Contains("OK")); err != nil {
		return err
	}
	return h.Step(ctx, "project-access", "verify Healthchecks project and read-only access", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := verifyProject(ctx, h)
			return err == nil, err
		},
		Hint: accountHint + " " + projectHint + " " + keyHint, Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if err := (App{}).Preflight(ctx, h); err != nil {
		return err
	}
	response, err := h.HTTP(ctx, helpers.Request{Path: "/health", NoRedirect: true})
	if err != nil || response.Status != http.StatusOK || strings.TrimSpace(string(response.Body)) != "OK" {
		return errors.New("Healthchecks database readiness is unavailable; read self-host logs healthchecks, then retry Setup")
	}
	return verifyProject(ctx, h)
}

var profileIdentity = regexp.MustCompile(`(?s)<h1\b[^>]*class=["']settings-title["'][^>]*>.*?<small>\s*([^<]+)\s*</small>`)
var projectTable = regexp.MustCompile(`(?s)<table\b[^>]*\bid=["']my-projects["'][^>]*>(.*?)</table>`)
var ownedProject = regexp.MustCompile(`href=["']/projects/([0-9a-f-]{36})/settings/["']`)
var projectBadge = regexp.MustCompile(`(?s)<div\b[^>]*\bid=["']preview["'][^>]*>.*?<img\b[^>]*\bsrc=["']([^"']+)["']`)
var badgePath = regexp.MustCompile(`/badge/[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]{8}-2\.svg$`)

func account(ctx context.Context, h *helpers.H) (*helpers.Session, error) {
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return nil, &helpers.ManualError{Hint: accountHint}
	}
	cookie, err := h.Remember("healthchecks-session", func() (string, error) {
		session := h.Session()
		csrf, err := session.FormToken(ctx, "/accounts/login/", "csrfmiddlewaretoken")
		if err != nil {
			return "", &helpers.ManualError{Hint: accountHint}
		}
		h.Log.Secret(csrf)
		response, err := session.HTTP(ctx, helpers.Request{
			Method: http.MethodPost, Path: "/accounts/login/", NoRedirect: true,
			Form:   url.Values{"action": {"login"}, "email": {(App{}).AdminEmail(h.AdminUser())}, "password": {h.AdminPassword()}, "csrfmiddlewaretoken": {csrf}},
			Header: map[string]string{"Referer": h.BaseURL + "/accounts/login/"},
		})
		if err != nil || response.Status != http.StatusFound || session.Cookie("sessionid") == "" {
			return "", &helpers.ManualError{Hint: accountHint}
		}
		cookie := session.Cookie("sessionid")
		h.Log.Secret(cookie)
		if _, err := profile(ctx, h, session); err != nil {
			return "", err
		}
		return cookie, nil
	})
	if err != nil {
		return nil, err
	}
	session := h.Session()
	session.SetCookie("sessionid", cookie)
	return session, nil
}

func profile(ctx context.Context, h *helpers.H, session *helpers.Session) ([]byte, error) {
	response, err := session.HTTP(ctx, helpers.Request{Path: "/accounts/profile/", NoRedirect: true})
	if err != nil || response.Status != http.StatusOK {
		return nil, &helpers.ManualError{Hint: accountHint}
	}
	identity := profileIdentity.FindSubmatch(response.Body)
	if len(identity) != 2 || !strings.EqualFold(strings.TrimSpace(html.UnescapeString(string(identity[1]))), (App{}).AdminEmail(h.AdminUser())) {
		return nil, &helpers.ManualError{Hint: accountHint}
	}
	return response.Body, nil
}

func verifyProject(ctx context.Context, h *helpers.H) error {
	session, err := account(ctx, h)
	if err != nil {
		return err
	}
	body, err := profile(ctx, h, session)
	if err != nil {
		return err
	}
	id := strings.ToLower(h.Inputs["project_id"])
	table := projectTable.FindSubmatch(body)
	if !projectPattern.MatchString(id) || len(table) != 2 {
		return &helpers.ManualError{Hint: projectHint}
	}
	found := false
	for _, match := range ownedProject.FindAllSubmatch(table[1], -1) {
		if string(match[1]) == id {
			found = true
		}
	}
	if !found {
		return &helpers.ManualError{Hint: projectHint}
	}
	key := h.Secret(h.Key("homarr_api_key"))
	if key == "" {
		return &helpers.ManualError{Hint: keyHint}
	}
	h.Log.Secret(key)
	response, err := readAPI(ctx, h, key, "/api/v3/checks/")
	if err != nil {
		return err
	}
	if err := validateChecks(response.Body); err != nil {
		return err
	}
	permissions, err := h.HTTP(ctx, helpers.Request{Path: "/api/v3/channels/", NoRedirect: true, Header: helpers.KeyScheme{Header: "X-Api-Key"}.Headers(key)})
	if err != nil || permissions.Status != http.StatusUnauthorized {
		return &helpers.ManualError{Hint: "Healthchecks did not prove this key is read-only. " + keyHint}
	}
	selected, err := session.HTTP(ctx, helpers.Request{Path: "/projects/" + id + "/badges/", NoRedirect: true})
	if err != nil || selected.Status != http.StatusOK {
		return &helpers.ManualError{Hint: projectHint}
	}
	badge := projectBadge.FindSubmatch(selected.Body)
	if len(badge) != 2 {
		return errors.New("Healthchecks did not return the selected project's overall badge identity; preserve the project and retry Setup")
	}
	expected := html.UnescapeString(string(badge[1]))
	u, err := validateAddress(expected)
	if err != nil || !badgePath.MatchString(u.Path) {
		return errors.New("Healthchecks returned an invalid overall badge identity; preserve the project and retry Setup")
	}
	response, err = readAPI(ctx, h, key, "/api/v3/badges/")
	if err != nil {
		return err
	}
	var result struct {
		Badges map[string]struct {
			SVG string `json:"svg"`
		} `json:"badges"`
		Error json.RawMessage `json:"error"`
	}
	if response.Decode(&result) != nil || len(result.Error) != 0 || result.Badges == nil || result.Badges["*"].SVG != expected {
		return &helpers.ManualError{Hint: "Healthchecks key does not identify the selected project's overall badge. " + keyHint}
	}
	return nil
}

func readAPI(ctx context.Context, h *helpers.H, key, path string) (helpers.Response, error) {
	response, err := h.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true, Header: helpers.KeyScheme{Header: "X-Api-Key"}.Headers(key)})
	if err != nil {
		return response, errors.New("Healthchecks Management API is unavailable; read self-host logs healthchecks and retry Setup")
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		return response, &helpers.ManualError{Hint: keyHint}
	}
	if response.Status != http.StatusOK {
		return response, fmt.Errorf("Healthchecks Management API returned HTTP %d; preserve authentication and retry Setup", response.Status)
	}
	return response, nil
}

func validateChecks(body []byte) error {
	var result struct {
		Checks []struct {
			Status    string          `json:"status"`
			UniqueKey string          `json:"unique_key"`
			NPings    *uint64         `json:"n_pings"`
			UUID      json.RawMessage `json:"uuid"`
			PingURL   json.RawMessage `json:"ping_url"`
			UpdateURL json.RawMessage `json:"update_url"`
			PauseURL  json.RawMessage `json:"pause_url"`
			ResumeURL json.RawMessage `json:"resume_url"`
			Channels  json.RawMessage `json:"channels"`
		} `json:"checks"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &result) != nil || result.Checks == nil || len(result.Error) != 0 {
		return errors.New("Healthchecks did not return an explicit check list; preserve existing checks and retry Setup")
	}
	seen := map[string]bool{}
	for _, check := range result.Checks {
		if len(check.UUID)+len(check.PingURL)+len(check.UpdateURL)+len(check.PauseURL)+len(check.ResumeURL)+len(check.Channels) != 0 {
			return &helpers.ManualError{Hint: "Healthchecks returned write-capable check data. " + keyHint}
		}
		key, err := hex.DecodeString(check.UniqueKey)
		if err != nil || len(key) != 20 || check.NPings == nil || seen[check.UniqueKey] {
			return errors.New("Healthchecks returned incomplete check identity or ping counts; preserve existing checks and retry Setup")
		}
		seen[check.UniqueKey] = true
		switch check.Status {
		case "new", "up", "grace", "down", "paused":
		default:
			return errors.New("Healthchecks returned an unknown check status; preserve existing checks and retry Setup")
		}
	}
	return nil
}
