# Tandoor traps

## Scope

This is an App-only source implementation for #180. It supplies the deployment
definition and checks an existing administrator and its owned active Space.
Automatic first-account creation, managed token issuance and native Homarr wiring
are not implemented. Catalog registration, DCM promotion and generated metadata
remain coordinator-owned. No native, rendered, persistence or lifecycle runtime
acceptance has been performed.

## Deployment and persistence

- The maintained official image is `docker.io/vabene1111/recipes:latest`.
  Source research resolved it to Tandoor 2.7.2, revision
  `d0a587832523a3e561932ce649c018b535fd43b5`. Resolve floating tags again for an
  actual runtime attempt; this is not an observed running version.
- Tandoor 2 bundles nginx and serves port **80**. Port 8080 is the default host
  port, allocated normally when busy. Internal consumers must use the `tandoor`
  service and container port 80, never the old DCM snippet's 8080 container port.
- The required backing service is App-owned `tandoor-db`, PostgreSQL 16 Alpine,
  with no published database port. Its health check gates Tandoor startup; the
  official entrypoint also waits for PostgreSQL, migrates and collects static
  files. An early nginx response is not completed Setup.
- Preserve `./database` at `/var/lib/postgresql/data`, `./mediafiles` at
  `/opt/recipes/mediafiles` and `./staticfiles` at `/opt/recipes/staticfiles`.
  The scoped staticfiles bind replaces upstream's named static volume so the
  App's folders remain within the existing Backup boundary. Recipes, users,
  Spaces and imports depend on the database and media; static assets are
  regenerated. Ordinary non-purge removal keeps data. Database downgrade or
  adoption of an existing DCM installation requires a separate migration plan.
- `TANDOOR_SECRET_KEY` and `TANDOOR_DB_PASSWORD` are generated once through the
  existing private Project secret boundary. Existing values are retained. The
  database receives only its own database password, not the shared Admin login
  or Homarr credentials.
- Do not add PUID/PGID or recursive ownership fixes. The image's own boot script
  recursively sets media permissions to 755. Review this upstream behavior before
  choosing existing mounts; Setup adds no filesystem migration.
- Host checking permits local addresses, Compose service DNS, the selected LAN
  address and configured Easy names. `tandoor.allowed_hosts` adds individual host
  names for other selected Access; it never enables public Access or wildcard
  host checks. Reverse proxies and trusted origins need deliberate configuration
  for the actual chosen Address, not a TLS or authentication bypass.
- New definitions disable signup, public metrics, external connectors and AI
  defaults for newly created Spaces using supported upstream settings. Existing
  user-edited Compose remains authoritative; Setup does not rewrite it, change
  existing Space settings or invoke remote providers. This default intentionally
  leaves external connectors unavailable until a person explicitly reviews them.

## Existing administrator and Space verification

Setup waits for the backend, then signs in using the shared Admin login through
`POST /_allauth/app/v1/auth/login`. It requires a positive authenticated envelope,
the expected username, a real account ID and a session token. The temporary token
is registered for redaction and used only in `X-Session-Token` headers, never as a
Homarr API key or persisted credential. A successful temporary session is reused
only within the existing helpers operation using `h.Remember`; failed sign-ins
are not remembered. Setup and Check still reread identity and Space metadata on
every verification. A new operation authenticates afresh. This avoids exhausting
the pinned allauth login quota of three attempts per minute per address, including
successful logins. HTTP 429 requests a wait and Setup retry, not account repair.

Read-only `GET /api/user-space/all_personal/` verifies that every returned
membership belongs to that authenticated, active superuser and that exactly one
Space is active with administrator membership. `GET /api/space/current/` must
identify the same active Space and that account as its owner. There is no assumed
account/Space ID, instance-wide count, account-creation marker or new authority
journal. Existing shared Spaces and unrelated memberships are not changed; this
slice deliberately holds a selected Space owned by another account as Manual.

Missing credentials, an uninitialized installation, rejected login, MFA/email
verification, missing/ambiguous membership or an unexpected active Space remains
an actionable `helpers.ManualError`. For a positively known new installation, the
person may create the first administrator in Tandoor's setup page. Otherwise they
must recover the existing account explicitly. Sign in using the intended Admin
login, select an existing Space it owns and retry `self-host setup tandoor`.
Setup never posts to the bootstrap form, resets accounts, switches Spaces or
scrapes the database. The ordinary sign-in itself creates an upstream session;
it does not create a recipe account, API token or Space.

The API requests do not use browser session cookies: upstream's cookie-authenticated
middleware can activate a first Space when none is active. Header-token requests
instead require an already-active membership. Redirects are not followed. Failure
messages contain no raw response bodies or private recipe/keyword data. No recipe,
meal-plan, import, website-scraping or provider-action endpoint is requested.

## Deferred native Homarr work

Tandoor's advertised kind is `tandoor`, with only `apiKey` authentication. Homarr
consumes a Bearer token with the exact `read` scope. Tandoor tokens have no managed
name and are masked shortly after creation. Issuance/recovery must wait for the
reviewed #150 journal; this App does not copy an issuance state machine, adopt a
matching token, rotate a lost/rejected credential or read token values from a
database. Existing token secrets and ownership records are untouched.

The later #147-backed connector must bind real Stats metrics `users`, `recipes`
and `keywords`, each in `count` units. Users/recipes describe the first accessible
Space; keywords describe the token's active Space. These identities can differ.
An empty Space list is not a valid zero sample. A fresh controlled first account
can legitimately report 1 user, 0 recipes and 0 keywords without manufacturing
recipes or changing meal plans. That is a planned expectation, not runtime proof.
Saved-integration account/Space identity, genuine advancing refresh, rendering,
both installation orders, invalid-key recovery and ownership-preserving removal
remain unimplemented or unrun as applicable.

## Official sources

- [Maintained Docker installation](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/docs/install/docker.md)
- [Official Compose and PostgreSQL major](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/docs/install/docker/plain/docker-compose.yml)
- [Entrypoint and persistence behavior](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/boot.sh)
- [Supported environment settings](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/recipes/settings.py)
- [Headless authentication](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/docs/features/authentication.md)
- [Membership/current-Space APIs](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/cookbook/views/api.py) and [serializers](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/cookbook/serializer.py)
- [Scope middleware](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/cookbook/helper/scope_middleware.py)
- [Homarr Tandoor Stats provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/tandoor.ts)
- [License terms](https://github.com/TandoorRecipes/recipes/blob/d0a587832523a3e561932ce649c018b535fd43b5/README.md#license): upstream describes AGPL v3 with a Commons Clause selling exception, not unrestricted AGPL.
