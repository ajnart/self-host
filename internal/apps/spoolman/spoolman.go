package spoolman

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/release"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "spoolman", Name: "Spoolman", Category: "Productivity",
		Description:   "Keep track of your 3D-printer filament spools and remaining material",
		Image:         "ghcr.io/donkie/spoolman:latest",
		Docs:          "https://github.com/Donkie/Spoolman/wiki/Installation",
		Port:          7912,
		ContainerPort: 8000,
		Service:       "spoolman",
		WebSockets:    true,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{Key: "db_password", Prompt: "Matching PostgreSQL password", Private: true, RecoveryOnly: true,
		Help: "Recover the original database password privately; never supply a replacement for retained database data. Automatic first-password generation is unavailable without proof of new storage."}}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "db_password", Length: 48}}
}

func (App) Preflight(_ context.Context, h *helpers.H) error {
	if h.Secrets == nil {
		return nil
	}
	return databaseCredential(h)
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  spoolman:
    image: ghcr.io/donkie/spoolman:latest
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      SPOOLMAN_DB_TYPE: postgres
      SPOOLMAN_DB_HOST: spoolman-db
      SPOOLMAN_DB_PORT: "5432"
      SPOOLMAN_DB_NAME: spoolman
      SPOOLMAN_DB_USERNAME: spoolman
      SPOOLMAN_DB_PASSWORD: ${SPOOLMAN_DB_PASSWORD}
      SPOOLMAN_AUTOMATIC_BACKUP: "FALSE"
      SPOOLMAN_UPDATE_CHECK: "FALSE"
      SPOOLMAN_METRICS_ENABLED: "FALSE"
      EXTERNAL_DB_URL: ""
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8000
    depends_on:
      spoolman-db:
        condition: service_healthy
  spoolman-db:
    image: docker.io/library/postgres:18-alpine
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      POSTGRES_DB: spoolman
      POSTGRES_USER: spoolman
      POSTGRES_PASSWORD: ${SPOOLMAN_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "spoolman", "-d", "spoolman"]
      interval: 5s
      timeout: 5s
      retries: 12
      start_period: 20s
`
}

const recoveryHint = "Read self-host logs spoolman and retry self-host setup spoolman. Preserve the existing database, credentials, data paths and authentication; Setup only reads inventory and never changes filament amounts, printers or consumption history."

const authenticationHint = "Spoolman's API is protected by existing authentication or a redirect. Keep that authentication enabled and preserve the database, credentials, data paths and Access. This App's read-only verification cannot negotiate that protection; review the private connection before retrying self-host setup spoolman. Do not publish another unauthenticated port."

const databaseCredentialHint = "Automatic database-password generation is unavailable because new storage cannot be established safely. Preserve the database and its authentication; restore the original matching SPOOLMAN_DB_PASSWORD with self-host install spoolman --yes --set-file spoolman.db_password=PATH using an owner-only credential file, then retry self-host setup spoolman. Only supply an initial password after independently verifying a new database. Never replace a retained database's password, initialize another database or migrate inventory."

var readiness = firstadmin.NoAccount{Probe: firstadmin.Probe{
	Path: "/api/v1/health",
	Request: func(*helpers.H) helpers.Request {
		return helpers.Request{Path: "/api/v1/health", NoRedirect: true}
	},
	Want: []int{200, 301, 302, 303, 307, 308, 401, 403},
}}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	h.Info("Spoolman has no built-in login. Keep it private or behind existing authenticated Access. Its own PostgreSQL database stores inventory; external catalog downloads, update checks and metrics are disabled in the starter definition.")
	if err := h.Step(ctx, "database-credential", "verify the private database credential is retained", helpers.StepSpec{
		Check: func(context.Context) (bool, error) {
			err := databaseCredential(h)
			return err == nil, err
		},
		Hint: databaseCredentialHint,
	}); err != nil {
		return err
	}
	if err := readiness.Setup(ctx, h); err != nil {
		return err
	}
	return h.Step(ctx, "inventory", "verify Spoolman identity and read-only inventory", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyInventory(ctx, h)
			return err == nil, err
		},
		Do:   func(ctx context.Context) error { return VerifyInventory(ctx, h) },
		Hint: recoveryHint,
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return VerifyInventory(ctx, h)
}

func VerifyInventory(ctx context.Context, h *helpers.H) error {
	if err := databaseCredential(h); err != nil {
		return err
	}
	health, err := read(ctx, h, "/api/v1/health")
	if err != nil {
		return err
	}
	var status struct{ Status string }
	if health.Decode(&status) != nil || status.Status != "healthy" {
		return errors.New("Spoolman did not return a healthy API response; " + recoveryHint)
	}
	response, err := read(ctx, h, "/api/v1/info")
	if err != nil {
		return err
	}
	var info struct {
		Version          string
		DatabaseType     string `json:"db_type"`
		DebugMode        *bool  `json:"debug_mode"`
		AutomaticBackups *bool  `json:"automatic_backups"`
		DataDir          string `json:"data_dir"`
		LogsDir          string `json:"logs_dir"`
		BackupsDir       string `json:"backups_dir"`
	}
	if response.Decode(&info) != nil || !release.IsRelease(info.Version) || info.DebugMode == nil || info.AutomaticBackups == nil || info.DataDir == "" || info.LogsDir == "" || info.BackupsDir == "" {
		return errors.New("Spoolman did not return its API identity; " + recoveryHint)
	}
	switch info.DatabaseType {
	case "postgres", "DatabaseType.POSTGRES":
	case "sqlite", "mysql", "cockroachdb", "DatabaseType.SQLITE", "DatabaseType.MYSQL", "DatabaseType.COCKROACHDB":
		return &helpers.ManualError{Hint: "Spoolman reports a different database backend from this App's PostgreSQL deployment. Preserve the selected backend, credentials and data paths; review persistent storage before retrying self-host setup spoolman. Never switch databases or migrate existing inventory automatically."}
	default:
		return errors.New("Spoolman returned an unknown database type; preserve its configuration and review the installed API before retrying self-host setup spoolman")
	}
	response, err = read(ctx, h, "/api/v1/spool")
	if err != nil {
		return err
	}
	var spools []struct {
		ID              *int64
		Archived        *bool
		Filament        struct{ ID *int64 }
		UsedWeight      *float64 `json:"used_weight"`
		UsedLength      *float64 `json:"used_length"`
		RemainingWeight *float64 `json:"remaining_weight"`
	}
	if response.Decode(&spools) != nil || spools == nil {
		return errors.New("Spoolman did not return an inventory array; " + recoveryHint)
	}
	seen := map[int64]bool{}
	for _, spool := range spools {
		if spool.ID == nil || *spool.ID < 0 || spool.Archived == nil || spool.Filament.ID == nil || *spool.Filament.ID < 0 || spool.UsedWeight == nil || spool.UsedLength == nil {
			return errors.New("Spoolman returned incomplete inventory identities or usage; " + recoveryHint)
		}
		if seen[*spool.ID] {
			return errors.New("Spoolman returned duplicate spool identities; " + recoveryHint)
		}
		seen[*spool.ID] = true
		for _, value := range []*float64{spool.UsedWeight, spool.UsedLength, spool.RemainingWeight} {
			if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
				return errors.New("Spoolman returned invalid inventory amounts; " + recoveryHint)
			}
		}
	}
	return nil
}

func databaseCredential(h *helpers.H) error {
	if h.Secret(h.Key("db_password")) == "" {
		return &helpers.ManualError{Hint: databaseCredentialHint}
	}
	return nil
}

func read(ctx context.Context, h *helpers.H, path string) (helpers.Response, error) {
	response, err := h.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true})
	if err != nil {
		return helpers.Response{}, errors.New("Spoolman API is unavailable; " + recoveryHint)
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status >= 300 && response.Status < 400 {
		return helpers.Response{}, &helpers.ManualError{Hint: authenticationHint}
	}
	if response.Status != http.StatusOK {
		return helpers.Response{}, fmt.Errorf("Spoolman API returned HTTP %d; %s", response.Status, recoveryHint)
	}
	return response, nil
}

func (App) StandaloneGuide() string {
	return "Spoolman has no built-in login: keep it private or behind an authenticated proxy. Inventory and settings are stored in the included PostgreSQL database; preserve its database folder and password and back them up together. The Spoolman image keeps its own non-root user; do not forward Host user IDs or change existing data ownership. External catalog downloads, update checks and metrics are disabled by default. No printer connection is required."
}
