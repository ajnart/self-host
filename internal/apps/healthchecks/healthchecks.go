package healthchecks

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/names"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "healthchecks", Name: "Healthchecks", Category: "Monitoring",
		Description: "Know when scheduled jobs stop running or finish late",
		Image:       "healthchecks/healthchecks:latest",
		Docs:        "https://healthchecks.io/docs/self_hosted_docker/",
		Port:        8000, ContainerPort: 8000, Service: "healthchecks",
	}
}

func (App) AdminEmail(username string) string {
	if username == "" || strings.Contains(username, "@") {
		return username
	}
	return username + "@selfhost.local"
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "project_id", Prompt: "Healthchecks project ID", Validate: validateProject,
			Help: "Choose the exact project UUID from its checks page. Setup verifies that the Admin login owns this project; it never selects another project automatically."},
		{Key: "site_root", Prompt: "Healthchecks browser address", Validate: validateOrigin,
			Help: "Optional http or https origin without a path; defaults to the selected home or local Address. Existing ping addresses are not migrated when Access changes."},
		{Key: "ping_endpoint", Prompt: "Healthchecks ping address", Validate: validatePingEndpoint,
			Help: "Optional absolute http or https address ending in a slash; defaults to the browser address plus /ping/. This changes displayed ping addresses, not incoming routing."},
		{Key: "homarr_api_key", Prompt: "Existing Healthchecks project read-only API key", Private: true, RecoveryOnly: true,
			Help: "Supply only the chosen project's known read-only key through a private file. Empty input preserves a saved key; review existing Compose before any reinstall used for private-input recovery."},
		{Key: "homarr_key_ownership", Prompt: "Saved Healthchecks read-only key ownership", Private: true, RecoveryOnly: true,
			Help: "Restore only the matching saved ownership record with its known key and exact project. Never fabricate ownership, clear pending issuance or change projects to obtain another key."},
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "db_password", Length: 48}, {Name: "secret_key", Length: 64}}
}

func (App) Compose(rc app.RenderContext) string {
	root, host := siteAddress(rc)
	hosts := []string{"healthchecks", "127.0.0.1", "localhost"}
	for _, candidate := range []string{host, rc.Settings["LAN_IP"]} {
		if strings.Contains(candidate, ":") {
			candidate = "[" + candidate + "]"
		}
		if candidate != "" && !slices.Contains(hosts, candidate) {
			hosts = append(hosts, candidate)
		}
	}
	ping := rc.Inputs["ping_endpoint"]
	if ping == "" {
		ping = root + "/ping/"
	}
	return `services:
  healthchecks:
    image: healthchecks/healthchecks:latest
    restart: unless-stopped
    environment:
      DB: postgres
      DB_HOST: healthchecks-db
      DB_PORT: "5432"
      DB_NAME: healthchecks
      DB_USER: healthchecks
      DB_PASSWORD: ${HEALTHCHECKS_DB_PASSWORD}
      SECRET_KEY: ${HEALTHCHECKS_SECRET_KEY}
      DEBUG: "False"
      REGISTRATION_OPEN: "False"
      INTEGRATIONS_ALLOW_PRIVATE_IPS: "False"
      GUNICORN_WORKERS: "2"
      SITE_ROOT: ` + strconv.Quote(root) + `
      PING_ENDPOINT: ` + strconv.Quote(ping) + `
      ALLOWED_HOSTS: ` + strconv.Quote(strings.Join(hosts, ",")) + `
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8000
    depends_on:
      healthchecks-db:
        condition: service_healthy
  healthchecks-db:
    image: postgres:16
    restart: unless-stopped
    environment:
      POSTGRES_DB: healthchecks
      POSTGRES_USER: healthchecks
      POSTGRES_PASSWORD: ${HEALTHCHECKS_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U healthchecks -d healthchecks"]
      interval: 5s
      timeout: 5s
      retries: 12
      start_period: 10s
`
}

func siteAddress(rc app.RenderContext) (string, string) {
	if root := rc.Inputs["site_root"]; root != "" {
		u, err := url.Parse(root)
		if err == nil {
			return strings.TrimRight(root, "/"), u.Hostname()
		}
	}
	host := rc.Settings["LAN_IP"]
	if host == "" {
		host = "${LAN_IP}"
	}
	if rc.Settings["EXPOSURE"] == "local" {
		host = "127.0.0.1"
	}
	if rc.Settings["EXPOSURE"] != "local" && rc.Settings["EASY_NAMES"] == "on" {
		if name, ok := names.Host("healthchecks", host); ok {
			return "http://" + name, name
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(rc.Port)), host
}

func (App) Preflight(_ context.Context, h *helpers.H) error {
	for _, input := range (App{}).Inputs() {
		if input.Validate != nil {
			if err := input.Validate(h.Inputs[input.Key]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (App) StandaloneGuide() string {
	return "After starting both services, run the image's interactive createsuperuser management command in the healthchecks service, then sign in. Registration is closed. Preserve the database and SECRET_KEY. Choose a project and create a read-only API key in Project Settings. Outbound email, notification delivery and real job pings require separate configuration; Setup never sends test pings."
}

var projectPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

func validateProject(value string) error {
	if value != "" && !projectPattern.MatchString(value) {
		return errors.New("Healthchecks project_id must be the exact project UUID from its checks page")
	}
	return nil
}

func validateAddress(value string) (*url.URL, error) {
	u, ok := helpers.HTTPAddress(value)
	if !ok {
		return nil, errors.New("Healthchecks addresses must be absolute http or https addresses without credentials, query or fragment")
	}
	if net.ParseIP(u.Hostname()) == nil && !hostPattern.MatchString(u.Hostname()) {
		return nil, errors.New("Healthchecks address has an invalid host")
	}
	return u, nil
}

func validateOrigin(value string) error {
	if value == "" {
		return nil
	}
	u, err := validateAddress(value)
	if err != nil {
		return err
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("Healthchecks site_root must be an origin without a path")
	}
	return nil
}

func validatePingEndpoint(value string) error {
	if value == "" {
		return nil
	}
	if _, err := validateAddress(value); err != nil {
		return err
	}
	if !strings.HasSuffix(value, "/") {
		return errors.New("Healthchecks ping_endpoint must end in a slash")
	}
	return nil
}
