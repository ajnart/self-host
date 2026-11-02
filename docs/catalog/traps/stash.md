# Stash

## Deployment and private storage

- The first-party image is `stashapp/stash:latest`, with HTTP on container port
  9999 and configuration at `/root/.stash/config.yml`. It contains ffmpeg and
  uses SQLite; no backing database, cache service, external account or hardware
  is required for an empty instance.
- `./config` holds configuration and the default `stash-go.sqlite` database.
  Separate App-local `./metadata`, `./cache`, `./blobs` and `./generated` binds
  persist their corresponding container paths. A shell entrypoint sets umask
  `077` before executing the image's `stash` binary and forwarding arguments.
  Stash writes configuration with requested mode `0640`, narrowed to `0600` for
  a new file by this umask. Existing files, modes and ownership are not rewritten
  by Setup. The App seeds no files and invokes no ownership-recovery workflow.
- First-visit configuration can select these mounted storage paths. Otherwise,
  Stash's defaults remain under the persistent config directory. Setup never
  changes an existing database, path, library selection or profile. It supplies
  no `STASH_STASH` or other selection override.
- Only the Host's narrow `adult` media subtree is mounted at `/data`, read-only.
  External media is not part of App Backup. Installation does not select a
  library, scan media, scrape metadata, generate thumbnails or write media.
- Preserve the selected bind address and host port. No host networking, Docker
  socket, privileged mode, GPU, DLNA or optional plugin is configured. The
  first-party Dockerfile does not establish Hotio-style PUID/PGID/UMASK support;
  those settings and separate media users are not advertised for this image.
- DCM already has a Hotio-based Stash Container. Its `/config` layout is not
  automatically adopted or migrated to the first-party `/root/.stash` layout.
  Any such migration needs its own explicit storage/authentication plan.

## Current App-only Setup boundary

This is a partial App implementation, not completed automatic bootstrap or native
Homarr acceptance. It verifies an existing protected account with its existing
instance API key. It creates no account, changes no password, and never calls
`setup`, `configureGeneral`, `generateAPIKey`, migration, scan or scraper actions.
Missing or ambiguous first-account ownership remains an actionable Manual step;
there is no empty-directory or boolean authority substitute.

Stash has **one unscoped instance API key**, not a collection of dedicated scoped
keys. Generating another key replaces the key other consumers use. There is no
supported read-only role or separate Homarr key in the inspected contract.
Preserve the working key and never regenerate or clear it merely to repair
self-host or Homarr. The verification queries are read-only; the credential
itself has broader account capability.

Use the private `stash.api_key` recovery input for the selected instance's
existing key. `stash.account_username` privately selects its existing username;
when absent, verification expects the shared Admin login username. These inputs
do not rename the account or change the shared Admin login. Do not paste a key
into chat, a public setting, a command argument or a screenshot. Empty private
input preserves saved values. Reinstall replaces Compose, so preserve and review
manual mounts, paths, images and Access and obtain separate approval before
using an install-based private recovery path. Then run `self-host setup stash`.

Verification first requires anonymous `POST /graphql` to return 401. A separate
request using the existing key in the `ApiKey` header must return a successful
GraphQL envelope, the selected configured username, `systemStatus.status: OK`,
and nonempty version/build identity. It does not read the API key, password,
library paths, scene titles, thumbnails or media through the API. Missing/null
identity data, GraphQL errors, rejected credentials, redirects and unknown
readiness are not success. Responses and transport causes are not copied into
diagnostics. TLS verification is not bypassed.

`/login` answering is readiness only: an unprotected Stash can accept a login
without establishing a configured account. Fresh or unprotected instances stay
Manual. Review ownership before completing the first visit, enable authentication,
leave library paths unselected, and do not scan or scrape. If no instance key
exists, creation still needs a separately reviewed issuance path or an explicitly
approved person-owned action; Setup will not create one. An existing database
that needs migration requires Backup and explicit approval, not automatic
migration or reinitialization.

## Pending integration and acceptance

Automatic first-account bootstrap and non-idempotent key issuance remain pending
the reviewed shared ownership/issuance work; no local journal is added here.
The native `stash` Homarr leaf, shared count/envelope checks, Catalog registration,
site/DCM generation and counts belong to later coordinated integration.
Homarr must consume the App-owned existing key, use the supported Stats surface,
and preserve recorded resource ownership and unrelated bindings.

The later Stats proof can use a protected empty library with explicit zero
counts, bytes and duration, positive account identity and a genuinely advancing
refresh. No real library or media ingestion is needed. Source and fake checks
do not establish native, rendered, lifecycle or image/API runtime acceptance.
Startup outbound behavior has not been runtime-verified; no container has been
started under this App-only dispatch. Runtime remains centrally scheduled and
held to the approved disposable-Project scope and budgets.

## Sources

Inspected Stash v0.32.0 source:
`9e9ad972643320e737c711884f10e4285f0f3a88`.

- [Official Compose: image, ports and persistence](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/docker/production/docker-compose.yml)
- [Official image: config path, ffmpeg and identity behavior](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/docker/build/x86_64/Dockerfile)
- [Setup writes and database/path defaults](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/internal/manager/manager.go)
- [Configuration write mode](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/internal/manager/config/config.go#L539-L549)
- [Singleton API-key authentication](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/pkg/session/session.go)
- [Authentication enforcement](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/internal/api/authentication.go)
- [Configuration/account fields](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/graphql/schema/types/config.graphql)
- [Setup/key-generation mutations](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/internal/api/resolver_mutation_configure.go)
- [Status schema](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/graphql/schema/types/metadata.graphql#L346-L363)
- [Version schema](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/graphql/schema/types/version.graphql)
- [Version query: build identity, not an instance/library UUID](https://github.com/stashapp/stash/blob/9e9ad972643320e737c711884f10e4285f0f3a88/internal/api/resolver.go#L303-L310)
- [Resolved Homarr Stash Stats provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/stash.ts)
