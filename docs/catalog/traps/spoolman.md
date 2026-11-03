# Spoolman

## Deployment and persistence

The App uses the official `ghcr.io/donkie/spoolman:latest` image, host port
7912 and container port 8000. Its internal Address is `http://spoolman:8000`,
not the host mapping. The App includes its own `spoolman-db` PostgreSQL 18
container, without a published database port. Database files persist under
`apps/spoolman/database`, mounted at PostgreSQL 18's `/var/lib/postgresql`.
Its schema, inventory, filaments, vendors, settings and usage fields belong
to that database. `SPOOLMAN_DB_PASSWORD` is an App-local database password stored
through the Project's private secret boundary; it is not a Spoolman web login or
a Homarr API key. Existing secrets are reused, not rotated.

The App's credential-aware Preflight checks the authoritative `H.Secrets` store
before Install or a changed-definition refresh can generate secrets, and before
startup. The earlier public-only Preflight has no secret store and defers this
check to that locked credential-aware call; public inputs are not authority.
Missing passwords remain Manual before generation, pull or up, including retained
databases, missing App/Project state and non-purge remove/reinstall. Setup and
Check retain the same Manual prerequisite before API reads.

**Automatic first-password generation is partial/unavailable.** Existing
facilities do not prove that storage is new: an absent or empty directory and
missing state are not permission to initialize or replace database credentials.
The existing secret declaration stays guarded; no new issuance ledger, private
store reader or password generator is introduced. An independently verified new
database needs an explicitly chosen initial password supplied privately. Retained
data requires its original matching password, never a newly chosen replacement.

Recover through the existing private-input boundary:
`self-host install spoolman --yes --set-file spoolman.db_password=PATH`, using an
owner-only file containing the original password. The existing in-memory private
input API and `--set-file spoolman.db_password=-` are also supported. Plain
`--set` is rejected for this field; credentials stay out of public App inputs,
events and Compose.
After recovery, retry `self-host setup spoolman`. The presence check does not
independently authenticate the supplied password against PostgreSQL; recovery
must provide the matching original, and successful inventory reads additionally
require the running App to have a working database connection.

The official Spoolman entrypoint rejects `PUID=0`, whereas rootless Docker
Projects use that Host setting. The App therefore uses Spoolman's supported
PostgreSQL backend on every platform and leaves Spoolman's image-owned
UID/GID 1000 and entrypoint unchanged. No host bind hides its writable home
directory. PostgreSQL uses its own official initialization and privilege
dropping. No Host account, global PUID/PGID, entrypoint replacement, ad-hoc
recursive ownership migration, device, socket or privileged grant is added.
This is a deployment contract verified from source and fakes, not observed
rootless runtime acceptance.

PostgreSQL's ordinary initialization prepares only this App's configured
database storage; its existing cluster is not reseeded by Setup. Never point
the new App at an existing off-Catalog database or change a SQLite deployment
to PostgreSQL as an implicit migration. The Project boundary preserves manual
Compose settings, selected images, database configuration and bind paths.
Read-only Check accepts PostgreSQL's recognized upstream database-type
representations. Another selected backend remains Manual for persistence
review, rather than silently accepting an ephemeral SQLite fallback or
replacing a person's selected backend. A missing or rejected
database password requires recovery of the matching secret, not a new cluster,
authentication disablement, data deletion or password rotation.

Spoolman's optional file logs and catalog cache are container-local in this
PostgreSQL deployment; do not treat them as a SQLite inventory backup.
Spoolman's automatic SQLite backups do not cover PostgreSQL and are disabled
in the starter definition. self-host Backup stops the App's running services
for a consistent copy of its App directory, then restarts them. Keep the
database folder, canonical Compose and private secrets together. Non-purge
removal retains database files. Existing external databases, paths and
Prometheus histories require their own backups; this App does not adopt them.

## Setup and authentication

Spoolman has no built-in authentication. Anyone who can reach its API can
modify inventory. Keep it private or behind an existing authenticated proxy;
public Access needs its own explicit approval and protection. Do not open an
unauthenticated alternate port, disable TLS verification or weaken a person's
authentication to make Setup or Homarr succeed.

Setup waits through the existing bounded readiness helper, then reads
`GET /api/v1/health`, `GET /api/v1/info` and `GET /api/v1/spool`. Check uses the
same verifier. A healthy status is not sufficient: the verifier requires a
real version and API identity, a recognized database type, and an explicit
inventory array with valid spool/filament identities, archive flags and usage
amounts. It does not expose the returned private paths or raw error bodies.
Authentication rejection or redirects remain an actionable Manual step via
`helpers.ManualError`; no login cookies, password replacements or access-policy
changes are attempted. Retry with `self-host setup spoolman` after reviewing
the private connection; use `self-host logs spoolman` for startup failures.

An explicit `[]` inventory is valid. Missing/null arrays and malformed
identities are not empty inventory. A spool's missing/null `remaining_weight`
is legitimately unknown and is never converted to zero or written back.
Numeric weights and usage must be finite and nonnegative. Setup never creates
demo spools or filaments, changes consumption, measures a spool, connects
printers, scans tags, or triggers the backup API. Reruns only repeat safe GETs.

The starter definition disables external filament catalog synchronization
with `EXTERNAL_DB_URL: ""`, GitHub update checks with
`SPOOLMAN_UPDATE_CHECK: "FALSE"`, and Prometheus metrics with
`SPOOLMAN_METRICS_ENABLED: "FALSE"`. Existing reviewed Compose choices are not
rewritten by Setup. Prometheus/Grafana, Moonraker, OctoPrint and printer
hardware are optional outside this App slice and are never configured here.

## Homarr boundary

Homarr's exact `spoolman` kind advertises only the empty-secret alternative.
Its Stats provider reads `/api/v1/spool`, filters archived rows, and exposes
`spools` (`count`) and `remainingWeight` (`grams`). Empty inventory produces
real numeric zero values; any active spool with unknown weight makes the
aggregate weight null. The descriptor selects only the spool count,
without forcing unknown weights into zeros or modifying inventory.

The Homarr descriptor reuses the App's read-only identity/inventory verifier
before mutation and during data verification. It uses the reviewed shared
count validator and the central single catalog/snapshot/forced-refresh sample,
requiring a nonnegative integral `spools` value with unit `count` and successful
retry metadata. Unselected unknown weight remains null; no extra Stats request
or independently cached count comparison is added. Existing ownership and
shared-widget reconciliation own stable IDs, user collisions and removal.

The descriptor is not yet assembled into the central registry. Catalog/site
generation and independent App/connector review remain separate work. Source
and fake tests are not native/rendered/lifecycle acceptance: those need their
own approved isolated attempt. No printer hardware is required for that proof.

## Primary sources

- [Official installation and images](https://github.com/Donkie/Spoolman/wiki/Installation)
- [Official configuration and supported databases](https://github.com/Donkie/Spoolman/wiki/Configuration)
- [Spoolman v0.27.1 release](https://github.com/Donkie/Spoolman/releases/tag/v0.27.1)
- [Release Dockerfile](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/Dockerfile)
- [Release entrypoint and PUID rejection](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/entrypoint.sh)
- [Upstream PostgreSQL deployment](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/tests_integration/docker-compose-postgres.yml)
- [API health and identity](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/spoolman/api/v1/router.py)
- [Inventory responses and unknown weights](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/spoolman/api/v1/models.py)
- [External catalog synchronization guard](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/spoolman/externaldb.py)
- [Update-check guard](https://github.com/Donkie/Spoolman/blob/ff8efd517b76a4b2783f29c89e9b9fbcd8cd167b/spoolman/update_check.py)
- [Security and reverse-proxy authentication](https://github.com/Donkie/Spoolman/wiki/Security)
- [PostgreSQL official image, initialization, credentials and versioned storage](https://github.com/docker-library/docs/tree/master/postgres)
- [PostgreSQL 18 Alpine image](https://github.com/docker-library/postgres/blob/e00e1bd34ec5c8a8e7ad89b273b3d42efaf6d5bc/18/alpine3.24/Dockerfile)
- [Homarr's published-image Spoolman Stats provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/spoolman.ts)
