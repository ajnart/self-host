package bindery

import (
	"context"
	"errors"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/mediastorage"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{ app.Defaults }

const (
	bootstrapHint = "Open Bindery and finish first-administrator creation using the Admin login from self-host credentials. Automatic first-account and key bootstrap is not implemented yet. Preserve any existing account, then run self-host setup bindery."
	loginHint     = "Sign in to Bindery with the existing administrator and reconcile the Admin login privately before retrying self-host setup bindery. Do not replace accounts, change passwords automatically or retry a rate-limited login immediately."
	authHint      = "Bindery verification requires enabled authentication and local administrator login. Review its existing Security or SSO configuration explicitly, keep authentication enabled, and retry self-host setup bindery; Setup does not rewrite that choice."
	keyHint       = "Restore the existing Bindery instance API key from Settings > General > Security as BINDERY_API_KEY through the Project's private credential boundary, then run self-host setup bindery. Do not regenerate the key or put it in arguments, chat or logs."
)

var (
	versionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

func (App) Info() app.Info {
	return app.Info{
		ID: "bindery", Name: "Bindery", Category: "Requests",
		Description:   "Find and organize ebooks and audiobooks in your own library",
		Image:         "ghcr.io/vavallee/bindery:latest",
		Docs:          "https://github.com/vavallee/bindery/blob/main/docs/DEPLOYMENT.md",
		Port:          8787,
		ContainerPort: 8787,
		Service:       "bindery",
		UsesMedia:     true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{
		Key: "api_key", Prompt: "Existing Bindery instance API key", Private: true, RecoveryOnly: true,
		Help: "Recover the current key privately, without regenerating it. This instance key grants administrator access; Bindery has no scoped read-only alternative. Reinstalling to import a private input requires reviewing and approving Compose replacement first.",
	}}
}

func (App) Directories(rc app.RenderContext) []app.Directory {
	return []app.Directory{{Path: "config", Mode: 0o755, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (App) Compose(rc app.RenderContext) string {
	mounts := "      - ${MEDIA_ROOT}:/media\n      - ${DOWNLOADS_ROOT}:/downloads\n"
	if mediastorage.Shared(rc.Settings) {
		mounts = "      - ${DATA_ROOT}:/data\n"
	}
	return `services:
  bindery:
    image: ghcr.io/vavallee/bindery:latest
    restart: unless-stopped
    user: ${PUID}:${PGID}
    environment:
      BINDERY_LIBRARY_DIR: ` + mediastorage.Media(rc.Settings) + `/books
      BINDERY_DOWNLOAD_DIR: ` + mediastorage.Torrents(rc.Settings) + `
      BINDERY_TELEMETRY_DISABLED: "true"
      BINDERY_DOWNLOAD_RETENTION_DAYS: "0"
    volumes:
      - type: bind
        source: ./config
        target: /config
        bind:
          create_host_path: false
` + mounts + `    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8787
    healthcheck:
      test: ["CMD", "/bindery", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 20s
`
}

func (App) StandaloneGuide() string {
	return "Before starting, create the config folder and the shared media/download parent with the UID/GID selected by user:. The image is distroless; PUID/PGID environment variables cannot change its user. Finish first-administrator creation in Bindery, then review existing library roots and profiles before selecting books folders. No downloader, indexer, scan, import or release feed is configured automatically. Telemetry is disabled before the first ping and download-history retention is disabled. Configuration persists under /config; media needs its own backup."
}

func (a App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/v1/health"); err != nil {
		return err
	}
	var checkErr error
	return h.Step(ctx, "configuration", "verify the existing Bindery administrator, API key and library data", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			checkErr = a.Check(ctx, h)
			return checkErr == nil, checkErr
		},
		Do:         func(context.Context) error { return checkErr },
		Hint:       "Run self-host setup bindery after privately repairing its existing Admin login and API key. Setup does not replace credentials, create library roots or change profiles.",
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := verifyAdmin(ctx, h); err != nil {
		return err
	}
	if err := verifyAPIKey(ctx, h); err != nil {
		return err
	}
	if err := verifyRoots(ctx, h); err != nil {
		return err
	}
	return verifyMedia(ctx, h)
}

func APIKey(h *helpers.H) string { return h.Secret(h.Key("api_key")) }

type authStatus struct {
	Authenticated    *bool  `json:"authenticated"`
	SetupRequired    *bool  `json:"setupRequired"`
	LocalAuthEnabled *bool  `json:"localAuthEnabled"`
	Mode             string `json:"mode"`
	Username         string `json:"username"`
	Role             string `json:"role"`
}

func decodeAuthStatus(resp helpers.Response) (authStatus, error) {
	var status authStatus
	if resp.Status != http.StatusOK || resp.Decode(&status) != nil || status.Authenticated == nil || status.SetupRequired == nil || status.LocalAuthEnabled == nil || status.Mode == "" {
		return status, errors.New("Bindery did not return a complete authentication status; retry self-host setup bindery")
	}
	return status, nil
}

func verifyAdmin(ctx context.Context, h *helpers.H) error {
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/v1/auth/status", NoRedirect: true})
	if err != nil {
		return errors.New("Bindery authentication status could not be read; retry self-host setup bindery")
	}
	status, err := decodeAuthStatus(resp)
	if err != nil {
		return err
	}
	if status.Mode != "enabled" || !*status.LocalAuthEnabled {
		return &helpers.ManualError{Hint: authHint}
	}
	if *status.SetupRequired {
		return &helpers.ManualError{Hint: bootstrapHint}
	}
	if *status.Authenticated {
		return errors.New("Bindery unexpectedly authenticated a request without credentials; review its authentication configuration")
	}
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return &helpers.ManualError{Hint: loginHint}
	}
	session := h.Session()
	login, err := session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/v1/auth/login", NoRedirect: true,
		JSON: map[string]string{"username": h.AdminUser(), "password": h.AdminPassword()},
	})
	if err != nil {
		return errors.New("Bindery administrator login could not be verified; retry self-host setup bindery")
	}
	if login.Status == http.StatusUnauthorized || login.Status == http.StatusForbidden || login.Status == http.StatusTooManyRequests {
		return &helpers.ManualError{Hint: loginHint}
	}
	var signedIn struct {
		OK       *bool  `json:"ok"`
		Username string `json:"username"`
	}
	if login.Status != http.StatusOK || login.Decode(&signedIn) != nil || signedIn.OK == nil || !*signedIn.OK || signedIn.Username != h.AdminUser() || session.Cookie("bindery_session") == "" {
		return errors.New("Bindery did not confirm the administrator login and session; retry self-host setup bindery")
	}
	h.Log.Secret(session.Cookie("bindery_session"))
	identity, err := session.HTTP(ctx, helpers.Request{Path: "/api/v1/auth/status", NoRedirect: true})
	if err != nil {
		return errors.New("Bindery administrator identity could not be read; retry self-host setup bindery")
	}
	status, err = decodeAuthStatus(identity)
	if err != nil {
		return err
	}
	if !*status.Authenticated || *status.SetupRequired || !*status.LocalAuthEnabled || status.Mode != "enabled" || status.Username != h.AdminUser() || status.Role != "admin" {
		return &helpers.ManualError{Hint: loginHint}
	}
	return nil
}

func keyRequest(h *helpers.H, endpoint string) helpers.Request {
	return helpers.Request{
		Path: endpoint, NoRedirect: true,
		Header: (helpers.KeyScheme{Header: "X-Api-Key"}).Headers(APIKey(h)),
	}
}

func verifyAPIKey(ctx context.Context, h *helpers.H) error {
	if strings.TrimSpace(APIKey(h)) == "" {
		return &helpers.ManualError{Hint: keyHint}
	}
	unsigned, err := h.HTTP(ctx, helpers.Request{Path: "/api/v1/system/status", NoRedirect: true})
	if err != nil {
		return errors.New("Bindery API authentication could not be checked; retry self-host setup bindery")
	}
	if unsigned.Status != http.StatusUnauthorized {
		return errors.New("Bindery's protected status did not reject a request without credentials; review its authentication configuration")
	}
	resp, err := h.HTTP(ctx, keyRequest(h, "/api/v1/system/status"))
	if err != nil {
		return errors.New("Bindery's saved API key could not be verified; retry self-host setup bindery")
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return &helpers.ManualError{Hint: keyHint}
	}
	var status struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&status) != nil || !versionPattern.MatchString(status.Version) || !commitPattern.MatchString(status.Commit) {
		return errors.New("Bindery did not return its authenticated release version and commit; retry self-host setup bindery")
	}
	return nil
}

func verifyRoots(ctx context.Context, h *helpers.H) error {
	resp, err := h.HTTP(ctx, keyRequest(h, "/api/v1/rootfolder"))
	if err != nil {
		return errors.New("Bindery library roots could not be read; retry self-host setup bindery")
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return &helpers.ManualError{Hint: keyHint}
	}
	var roots []struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&roots) != nil || roots == nil {
		return errors.New("Bindery did not return an explicit library root list; retry self-host setup bindery")
	}
	ids := map[int64]bool{}
	for _, root := range roots {
		if root.ID <= 0 || ids[root.ID] || !path.IsAbs(root.Path) || strings.TrimSpace(root.Path) == "" {
			return errors.New("Bindery returned an invalid or duplicate library root; review existing roots without replacing their paths")
		}
		ids[root.ID] = true
	}
	return nil
}
