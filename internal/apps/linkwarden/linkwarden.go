// Package linkwarden is the Linkwarden bookmark manager: a private archive of
// saved pages backed by PostgreSQL and a Meilisearch index.
package linkwarden

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Linkwarden. Traps: docs/catalog/traps/linkwarden.md.
type App struct{ app.Defaults }

const service = "linkwarden"

func (App) Info() app.Info {
	return app.Info{
		ID: "linkwarden", Name: "Linkwarden", Category: "Productivity",
		Description:   "Save web pages you care about and search them later",
		Image:         "ghcr.io/linkwarden/linkwarden:latest",
		Docs:          "https://docs.linkwarden.app/self-hosting/setup",
		Port:          3000,
		ContainerPort: 3000,
		Service:       service,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "homarr_api_key", Prompt: "Saved self-host Homarr access token", Private: true, RecoveryOnly: true,
			Help: "Optional restoration of the known token with its matching ownership record, never a different account's token. Empty private inputs preserve saved values. Reinstall replaces Compose: preserve and review existing authentication, mounts and ports and separately approve replacement before using install --set-file."},
		{Key: "homarr_token_ownership", Prompt: "Saved self-host Homarr token ownership", Private: true, RecoveryOnly: true,
			Help: "Restore the matching JSON record with id, accountId, expires and fingerprint, not a guessed token ID. A pending issued token blocks reissuance until its secret is restored or its recorded ID is absent from the authenticated token inventory. Empty private inputs preserve saved values."},
	}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "auth_secret", Length: 48},
		{Name: "db_password", Length: 48},
		{Name: "search_key", Length: 48},
	}
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  linkwarden:
    image: ghcr.io/linkwarden/linkwarden:latest
    restart: unless-stopped
    environment:
      MEILI_HOST: http://linkwarden-meilisearch:7700
      NEXT_PUBLIC_DISABLE_REGISTRATION: "false"
      NEXTAUTH_URL: http://${LAN_IP}:` + port + `/api/v1/auth
      NEXTAUTH_SECRET: ${LINKWARDEN_AUTH_SECRET}
      MEILI_MASTER_KEY: ${LINKWARDEN_SEARCH_KEY}
      DATABASE_URL: postgresql://postgres:${LINKWARDEN_DB_PASSWORD}@linkwarden-postgres:5432/postgres
    volumes:
      - ./archives:/data/data
    ports:
      - ${BIND_IP}:` + port + `:3000
    depends_on:
      - linkwarden-postgres
      - linkwarden-meilisearch
  linkwarden-postgres:
    image: docker.io/library/postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_PASSWORD: ${LINKWARDEN_DB_PASSWORD}
    volumes:
      - ./database:/var/lib/postgresql/data
  linkwarden-meilisearch:
    image: docker.io/getmeili/meilisearch:v1.13.3
    restart: unless-stopped
    environment:
      MEILI_MASTER_KEY: ${LINKWARDEN_SEARCH_KEY}
    volumes:
      - ./search:/meili_data
`
}

var setup = setupspec.Spec{
	Kind: setupspec.KindFirstAdmin,
	Name: "Linkwarden",
	// v2.16.3 has no health route. The protected current-user route proves
	// API routing before signup; Login separately verifies authenticated identity.
	Probe: setupspec.Probe{Path: "/api/v1/users/me", Want: []int{401}, Contains: `"response":"You must be logged in."`},
	Create: []setupspec.Call{{
		Path: "/api/v1/users",
		JSON: map[string]any{"username": "{user}", "password": "{password}"},
		Ok:   []int{201},
	}},
	Login: []setupspec.Call{
		{
			Path:    "/api/v1/session",
			JSON:    map[string]any{"username": "{user}", "password": "{password}"},
			Denied:  []int{400, 401},
			Ok:      []int{200},
			Capture: map[string]string{"token": "response.token"},
		},
		{
			Path:    "/api/v1/users/me",
			Header:  map[string]string{"Authorization": "Bearer {token}"},
			Ok:      []int{200},
			Lenient: true,
			Assert: []setupspec.Assert{
				{Path: "response.username", Equals: "{user}"},
				{Path: "response.id", Equals: "1"},
			},
		},
	},
	Manual: "Linkwarden signs up its first account on the private instance.",
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := setup.Setup(ctx, h); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		h.Skip("homarr-token", "Homarr is not installed; no Linkwarden token is needed")
		return nil
	}
	return h.Step(ctx, "homarr-token", "prepare Linkwarden statistics access", helpers.StepSpec{
		Check:      func(ctx context.Context) (bool, error) { return homarrTokenWorks(ctx, h) },
		Do:         func(ctx context.Context) error { return createHomarrToken(ctx, h) },
		Hint:       HomarrRecoveryHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }

func (App) StandaloneGuide() string {
	return "After the first start, sign up the first account on the first visit, then set NEXT_PUBLIC_DISABLE_REGISTRATION to \"true\" and run docker compose up -d again; registration stays open until you do."
}
