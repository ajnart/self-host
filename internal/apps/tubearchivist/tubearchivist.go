package tubearchivist

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/names"
)

type App struct{ app.Defaults }

const (
	prerequisiteHint = "Tube Archivist startup is held until Elasticsearch's Docker host or VM has verified vm.max_map_count of at least 1048576, writable nonroot persistent storage, and reviewed resource admission above its 1 GiB Java heap. Its automatic schedules and outbound activity also need containment. Review docs/catalog/traps/tubearchivist.md before retrying installation; do not change host settings, lower the heap or bypass fixture guards to continue."
	accountHint      = "Sign in to the existing Tube Archivist administrator using self-host credentials privately. Preserve its account, authentication, archive and subscriptions; restore the intended Admin login through approved account recovery, then run self-host setup tubearchivist. Setup does not create or reset an account, read a database, or issue an API token."
)

func (App) Info() app.Info {
	return app.Info{
		ID: "tubearchivist", Name: "Tube Archivist", Category: "Media",
		Description:   "Keep and watch your own archive of YouTube videos",
		Image:         "bbilly1/tubearchivist:latest",
		Docs:          "https://docs.tubearchivist.com/installation/docker-compose/",
		Port:          8000,
		ContainerPort: 8000,
		Service:       "tubearchivist",
		UsesMedia:     true,
		LongTimeouts:  true,
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "elastic_password", Length: 48, Hex: true}}
}

func (App) Files(_ app.RenderContext, secret func(string) string) []app.File {
	return []app.File{
		{Path: "cache/.keep", Mode: 0o600, Keep: true},
		{Path: "redis/.keep", Mode: 0o600, Keep: true},
		{Path: "elasticsearch/.keep", Mode: 0o600, Keep: true},
		{Path: "secrets/admin_password", Content: secret("ADMIN_PASSWORD") + "\n", Mode: 0o600, Keep: true},
		{Path: "secrets/elastic_password", Content: secret("elastic_password") + "\n", Mode: 0o600, Keep: true},
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	origins := []string{"http://127.0.0.1:" + port, "http://localhost:" + port, "http://tubearchivist:8000"}
	if rc.Settings["EXPOSURE"] != "local" {
		origins = append(origins, "http://${LAN_IP}:"+port)
	}
	if rc.Settings["EASY_NAMES"] == "on" {
		if host, ok := names.Host("tubearchivist", rc.Settings["LAN_IP"]); ok {
			origins = append(origins, "http://"+host)
		}
	}
	return `services:
  tubearchivist:
    image: bbilly1/tubearchivist:latest
    user: ${PUID}:${PGID}
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      TA_HOST: ` + strconv.Quote(strings.Join(origins, " ")) + `
      TA_USERNAME: ${ADMIN_USER}
      TA_PASSWORD_FILE: /run/secrets/admin_password
      ES_URL: http://tubearchivist-es:9200
      ELASTIC_PASSWORD_FILE: /run/secrets/elastic_password
      REDIS_CON: redis://tubearchivist-redis:6379
    volumes:
      - ./cache:/cache
      - ${MEDIA_ROOT}/tubearchivist:/youtube
      - ./secrets/admin_password:/run/secrets/admin_password:ro
      - ./secrets/elastic_password:/run/secrets/elastic_password:ro
    ports:
      - ${BIND_IP}:` + port + `:8000
    depends_on:
      tubearchivist-redis:
        condition: service_healthy
      tubearchivist-es:
        condition: service_started
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8000/api/health/"]
      interval: 2m
      timeout: 10s
      retries: 3
      start_period: 30s
  tubearchivist-redis:
    image: redis:8
    user: ${PUID}:${PGID}
    restart: unless-stopped
    command: ["redis-server", "--appendonly", "yes"]
    volumes:
      - ./redis:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5
  tubearchivist-es:
    image: docker.elastic.co/elasticsearch/elasticsearch:9.5.5
    user: ${PUID}:0
    restart: unless-stopped
    environment:
      ELASTIC_PASSWORD_FILE: /run/secrets/elastic_password
      ES_JAVA_OPTS: -Xms1g -Xmx1g
      xpack.security.enabled: "true"
      discovery.type: single-node
      path.repo: /usr/share/elasticsearch/data/snapshot
    volumes:
      - ./elasticsearch:/usr/share/elasticsearch/data
      - ./secrets/elastic_password:/run/secrets/elastic_password:ro
`
}

func (App) Preflight(ctx context.Context, _ *helpers.H) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &helpers.ManualError{Hint: prerequisiteHint}
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/health/", helpers.WantStatus(http.StatusOK), helpers.Contains(`"OK"`)); err != nil {
		return err
	}
	if err := h.Step(ctx, "admin-login", "verify the existing Tube Archivist administrator", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			_, _, err := signIn(ctx, h)
			return err == nil, err
		},
		Hint: accountHint, Credential: "admin",
	}); err != nil {
		return err
	}
	if !homarrKeyNeeded(h) {
		return nil
	}
	session, id, err := signIn(ctx, h)
	if err != nil {
		return err
	}
	return setupHomarrKey(ctx, h, session, strconv.FormatInt(id, 10))
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	_, _, err := signIn(ctx, h)
	return err
}

func signIn(ctx context.Context, h *helpers.H) (*helpers.Session, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	user, password := h.AdminUser(), h.AdminPassword()
	if strings.TrimSpace(user) == "" || password == "" {
		return nil, 0, &helpers.ManualError{Hint: accountHint}
	}
	h.Log.Secret(password)
	session := h.Session()
	login, err := session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/user/login/", NoRedirect: true,
		JSON: map[string]string{"username": user, "password": password, "remember_me": "off"},
	})
	if err != nil {
		return nil, 0, h.Log.RedactError(fmt.Errorf("Tube Archivist login request failed: %w", err))
	}
	if login.Status == http.StatusBadRequest || login.Status == http.StatusUnauthorized || login.Status == http.StatusForbidden {
		return nil, 0, &helpers.ManualError{Hint: accountHint}
	}
	if login.Status != http.StatusNoContent {
		return nil, 0, fmt.Errorf("Tube Archivist login returned HTTP %d; inspect its authentication and logs without resetting credentials", login.Status)
	}
	if session.Cookie("sessionid") == "" {
		return nil, 0, &helpers.ManualError{Hint: accountHint}
	}
	h.Log.Secret(session.Cookie("sessionid"))
	account, err := session.HTTP(ctx, helpers.Request{Path: "/api/user/account/", NoRedirect: true})
	if err != nil {
		return nil, 0, h.Log.RedactError(fmt.Errorf("Tube Archivist account request failed: %w", err))
	}
	if account.Status != http.StatusOK {
		return nil, 0, &helpers.ManualError{Hint: accountHint}
	}
	var identity struct {
		ID          *int64 `json:"id"`
		Name        string `json:"name"`
		IsSuperuser *bool  `json:"is_superuser"`
		IsStaff     *bool  `json:"is_staff"`
	}
	if account.Decode(&identity) != nil || identity.ID == nil || *identity.ID <= 0 || identity.Name != user || identity.IsSuperuser == nil || !*identity.IsSuperuser || identity.IsStaff == nil || !*identity.IsStaff {
		return nil, 0, &helpers.ManualError{Hint: accountHint}
	}
	ping, err := session.HTTP(ctx, helpers.Request{Path: "/api/ping/", NoRedirect: true})
	if err != nil {
		return nil, 0, h.Log.RedactError(fmt.Errorf("Tube Archivist authenticated ping request failed: %w", err))
	}
	if ping.Status != http.StatusOK {
		return nil, 0, fmt.Errorf("Tube Archivist authenticated ping returned HTTP %d; inspect its backing services and logs", ping.Status)
	}
	var state struct {
		Response string `json:"response"`
		User     *int64 `json:"user"`
		Version  string `json:"version"`
	}
	if ping.Decode(&state) != nil || state.Response != "pong" || state.User == nil || *state.User != *identity.ID {
		return nil, 0, &helpers.ManualError{Hint: accountHint}
	}
	if !helpers.VersionCompatible(state.Version, "0.6.0") {
		return nil, 0, &helpers.ManualError{Hint: "Tube Archivist did not report a compatible v0.6.0-or-newer 0.x API. Review the selected image and its migration requirements before retrying Setup; preserve existing data and do not substitute or upgrade images automatically."}
	}
	return session, *identity.ID, nil
}

func (App) StandaloneGuide() string {
	return "Before starting, verify Elasticsearch's Docker host or VM has vm.max_map_count of at least 1048576, a nonroot user with writable persistent folders, and memory above its 1 GiB Java heap. Review automatic schedules and outbound activity before allowing startup. Set TA_HOST to the intended browser origins. Initial administrator environment settings do not reset an existing account. Keep cache and both database folders; archived media needs a separate backup."
}
