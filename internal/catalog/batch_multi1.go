package catalog

import (
	"github.com/ajnart/self-host/internal/apps/authentik"
	"github.com/ajnart/self-host/internal/apps/bookstack"
	"github.com/ajnart/self-host/internal/apps/docmost"
	"github.com/ajnart/self-host/internal/apps/fireflyiii"
	"github.com/ajnart/self-host/internal/apps/healthchecks"
	"github.com/ajnart/self-host/internal/apps/immich"
	"github.com/ajnart/self-host/internal/apps/jellystat"
	"github.com/ajnart/self-host/internal/apps/keycloak"
	"github.com/ajnart/self-host/internal/apps/linkwarden"
	"github.com/ajnart/self-host/internal/apps/mealie"
	"github.com/ajnart/self-host/internal/apps/romm"
)

func init() {
	register(
		authentik.App{},
		bookstack.App{},
		docmost.App{},
		fireflyiii.App{},
		healthchecks.App{},
		immich.App{},
		jellystat.App{},
		keycloak.App{},
		linkwarden.App{},
		mealie.App{},
		romm.App{},
	)
}
