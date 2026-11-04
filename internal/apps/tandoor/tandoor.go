package tandoor

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/names"
)

type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "tandoor", Name: "Tandoor", Category: "Productivity",
		Description: "Keep recipes organized and share them with family and friends",
		Image:       "docker.io/vabene1111/recipes:latest",
		Docs:        "https://docs.tandoor.dev/install/docker/",
		Port:        8080, ContainerPort: 80, Service: "tandoor",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{
		Key: "allowed_hosts", Prompt: "Additional Tandoor host names",
		Help:     "Optional comma-separated host names for your existing Access. Local addresses and Easy names are included automatically. No wildcards, schemes or ports.",
		Validate: validateHosts,
	}}
}

func validateHosts(value string) error {
	if value == "" {
		return nil
	}
	for _, raw := range strings.Split(value, ",") {
		host := strings.TrimSpace(raw)
		if host == "" {
			return errors.New("Tandoor host names must not contain empty entries")
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			continue
		}
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && strings.Contains(host, ":") {
			if net.ParseIP(host[1:len(host)-1]) != nil {
				continue
			}
		}
		for _, r := range host {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
				continue
			}
			return errors.New("Tandoor host names must not contain wildcards, schemes or ports")
		}
		if strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
			return errors.New("Tandoor host names must name individual hosts")
		}
	}
	return nil
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "secret_key", Length: 64},
		{Name: "db_password", Length: 48},
	}
}

func (App) Compose(rc app.RenderContext) string {
	hosts := "localhost,127.0.0.1,[::1],tandoor,${LAN_IP}"
	if rc.Settings["EASY_NAMES"] == "on" {
		if host, ok := names.Host("tandoor", rc.Settings["LAN_IP"]); ok {
			hosts += "," + host
		}
	}
	if extra := strings.TrimSpace(rc.Inputs["allowed_hosts"]); extra != "" {
		hosts += "," + extra
	}
	return `services:
  tandoor:
    image: docker.io/vabene1111/recipes:latest
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      SECRET_KEY: ${TANDOOR_SECRET_KEY}
      DB_ENGINE: django.db.backends.postgresql
      POSTGRES_HOST: tandoor-db
      POSTGRES_PORT: "5432"
      POSTGRES_DB: tandoor
      POSTGRES_USER: tandoor
      POSTGRES_PASSWORD: ${TANDOOR_DB_PASSWORD}
      ALLOWED_HOSTS: ` + strconv.Quote(hosts) + `
      ENABLE_SIGNUP: "0"
      ENABLE_METRICS: "0"
      DISABLE_EXTERNAL_CONNECTORS: "1"
      SPACE_AI_ENABLED: "0"
    volumes:
      - ./mediafiles:/opt/recipes/mediafiles
      - ./staticfiles:/opt/recipes/staticfiles
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:80
    depends_on:
      tandoor-db:
        condition: service_healthy
  tandoor-db:
    image: docker.io/library/postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: tandoor
      POSTGRES_USER: tandoor
      POSTGRES_PASSWORD: ${TANDOOR_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U tandoor -d tandoor"]
      interval: 5s
      timeout: 5s
      retries: 20
`
}

func (App) StandaloneGuide() string {
	return "After the first start, create the first administrator in Tandoor's setup page only for a known new installation. Preserve existing accounts and Spaces; Setup never creates or resets them automatically. Registration, external connectors, metrics and new-Space AI features are disabled by default. Review ALLOWED_HOSTS for your selected Access. Managed Homarr linking remains held because supported APIs cannot prove the exact saved token's identity, rights and expiry. Do not change upstream credentials to bypass that hold."
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/server-settings/current/", helpers.WantStatus(200), helpers.Timeout(5*time.Minute)); err != nil {
		return err
	}
	if err := h.Step(ctx, "owner", "verify the existing Tandoor administrator and active Space", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := verifyOwner(ctx, h)
			return err == nil, err
		},
		Do:   func(ctx context.Context) error { return verifyOwner(ctx, h) },
		Hint: ownerRecoveryHint, Credential: "admin",
	}); err != nil {
		return err
	}
	return h.Step(ctx, "homarr-key", "verify Tandoor's dedicated read-only Homarr token", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyHomarr(ctx, h)
			return err == nil, err
		},
		Do:   func(ctx context.Context) error { return ensureHomarrToken(ctx, h) },
		Hint: HomarrRecoveryHint,
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if err := verifyOwner(ctx, h); err != nil {
		return err
	}
	return VerifyHomarr(ctx, h)
}
