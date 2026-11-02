# Miniflux traps

## Source-only App slice

This initial #166 slice supplies the real App and PostgreSQL definition plus
read-only verification of an existing reader. It does not implement automatic
bootstrap, Homarr token issuance, a native Homarr Link or runtime acceptance.
Catalog registration, generated site/DCM data and shared metadata remain with
the coordinating integration lane.

## Deployment and data

- The official `docker.io/miniflux/miniflux:latest` image listens on container
  port 8080 and runs as UID 65534. Keep the selected Access bind address; do not
  add LinuxServer identity variables, a Docker socket or media mounts.
- `miniflux-postgres` uses the official PostgreSQL 18 image, an App-specific
  generated database password and no host-published database port. Miniflux
  waits for PostgreSQL's healthcheck; its own `/healthcheck` checks connectivity,
  not account readiness.
- PostgreSQL 18 persists below `/var/lib/postgresql/18/docker`; the App-local
  `./database:/var/lib/postgresql` bind includes the whole database in the
  existing App-directory Backup boundary. Do not substitute an anonymous or
  unarchived named volume, reinterpret an existing path or change database major
  versions without an explicitly reviewed migration. Removal without purge
  preserves data. Backup/Restore runtime behavior remains unproved for this App.
- Database schema migrations run using the documented `RUN_MIGRATIONS` option.
  User-edited Compose settings and image references remain Project-owned.
- `DISABLE_SCHEDULER_SERVICE=1` contains startup feed polling and automatic
  cleanup. Setup never reenables it. Automatic feed refresh therefore remains
  deliberately disabled in this partial slice. Reenabling the scheduler needs
  review and approval of external requests and cleanup policy; it is not an
  acceptance step. No subscriptions, feed discovery/import/refresh, article
  fetching, entry read-state updates or history actions occur in Setup/Check.

## Manual bootstrap and reader identity

- `CREATE_ADMIN` is explicitly disabled. Upstream skips creation only if the
  configured username exists, not if any administrator exists. A permanently
  enabled flag could create another administrator after the original is renamed.
  An empty folder, fresh flag, failed login or missing local record does not
  authorize account creation.
- The supported API cannot establish safe unauthenticated account absence for
  an automatic bootstrap in this slice. A failed or unavailable Admin login is
  an actionable Manual step, not a trigger for account creation, password reset,
  privilege changes, SQL access or a non-TTY workaround.
- A person must inspect the existing instance first. Only after independently
  confirming there is no account may they use the documented interactive
  `docker compose exec miniflux /usr/bin/miniflux -create-admin` from the Project
  directory in a real terminal, with the shared Admin login available through
  `self-host credentials`. If a reader already exists, reconcile its known
  credentials instead; do not create a different empty reader for convenient
  statistics. Retry `self-host setup miniflux` afterward.
- This first slice selects the existing shared Admin login as its reader.
  `/v1/me` must return its exact username, a positive numeric ID and explicit
  administrator status. `/v1/feeds` must return an explicit array whose feed
  IDs are unique and positive and whose owner matches that reader. Empty arrays
  are valid only alongside the authenticated identity.
- After both reads succeed, Setup stores `MINIFLUX_READER_ID` through the
  private Project secret boundary. It is an account-continuity check, not a
  freshness flag, bootstrap authority or credential-issuance journal. Reruns
  retain it; a malformed record or different authenticated ID stays Manual.
  Check never writes it, resets it or silently selects another account. A missing
  record requires Setup to verify and record the existing positively authenticated
  reader. Restore only the independently confirmed original record through the
  private `miniflux.reader_id` recovery input when resolving identity drift,
  preserving evidence and all existing data. Empty inputs preserve saved values.
  Private `install --set-file` recovery also replaces Compose: preserve existing
  authentication, mounts, scheduler settings and ports, review that replacement
  and obtain explicit approval before using it. Never change the record to adopt
  an unexpected reader.
- Authentication reads update upstream login/use timestamps, not feed contents
  or entry read/unread status. Redirects are refused for authenticated requests.
  Raw response bodies and credentials are not included in diagnostics.

## Pending Homarr connector

- Homarr's exact `miniflux` kind requires `apiKey` and supports Stats only:
  `read` and `unread`, both unit `count`. These are stored read/unread entries
  belonging to the token's reader, not global or lifetime totals.
- Miniflux tokens use `X-Auth-Token` and inherit their account's full privileges;
  upstream has no read-only token scope. An explicitly intended non-admin reader
  can supply Stats, but creating a different empty account changes the data's
  meaning and is not a least-privilege substitute for the intended reader.
- The supported API can list/create application keys since Miniflux 2.2.9,
  but this slice does not issue, adopt, list or rotate them. Connector work waits
  for #150's accepted non-idempotent issuance journal and #147's count/envelope
  helpers. Do not add local copies, pending flags or SQL/key scraping.
- Native identity through Homarr's saved integration, one genuinely advancing
  Stats sample, units/count semantics, both installation orders, rendered values
  and ownership-preserving lifecycle are still unimplemented/unproved. Neither
  this App's healthcheck nor an empty feed list establishes native acceptance.

## Primary sources

Research baseline: Miniflux 2.3.5, source
`faecfa2fac61a91f4f69550bb1afd506c4b51749`; Homarr v2.10.1, source
`0f0987d2ed013c93ce30c1c00786983d29f4b956`. These are source identities,
not resolved runtime image/API evidence.

- [Official Docker guide](https://miniflux.app/docs/docker.html)
- [Configuration and scheduler containment](https://miniflux.app/docs/configuration.html)
- [API authentication, current user and feeds](https://miniflux.app/docs/api.html)
- [Official image and UID](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/packaging/docker/alpine/Dockerfile)
- [Admin creation guard](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/cli/create_admin.go)
- [Supported interactive command](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/cli/cli.go)
- [Interactive terminal requirement](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/cli/ask_credentials.go)
- [Scheduler admission](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/cli/daemon.go)
- [Feed polling and cleanup share the scheduler](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/cli/scheduler.go)
- [API authentication and timestamp updates](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/api/middleware.go)
- [PostgreSQL image persistence](https://github.com/docker-library/docs/blob/master/postgres/content.md#pgdata)
- [Token fields and absence of scopes](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/model/api_key.go)
- [Homarr Miniflux Stats provider](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/integrations/src/stats/providers/miniflux.ts)
