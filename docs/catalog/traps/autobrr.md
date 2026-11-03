# Autobrr

## Deployment and persistence

- **Supported directory preparation:** the App declares its actual `config` directory with private mode `0700` and the selected decimal container `PUID`/`PGID`. The reviewed shared action resolves those IDs and admits the entire directory batch before creating missing storage during Install or an accepted generated-definition refresh. Compatible existing ownership, modes, data and old markers are unchanged. There is no sentinel declaration, App-local filesystem helper or root fallback.
- Official image: `ghcr.io/autobrr/autobrr:latest`; service `autobrr`, container TCP port 7474. The allocated host port binds only the selected `${BIND_IP}`. Homarr uses the Compose Address `http://autobrr:7474`, independently of the browser Address.
- Persist the whole App-local `./config:/config`: `config.toml`, SQLite `autobrr.db`, sidecars and migration backups. SQLite is the upstream default and needs no backing container. Preserve an existing PostgreSQL/backend choice, custom paths and manual Compose edits; Setup does not select or migrate a database.
- The official image supports `user: UID:GID`, so the definition uses `${PUID}:${PGID}`, not unsupported LinuxServer identity variables. `TZ` is supported. `AUTOBRR__HOST=0.0.0.0` selects the container listener; it does not broaden the published Access. No Docker socket, media/download mount, tracker account or downloader is required for statistics.
- The image includes curl. `/api/healthz/readiness` is an unauthenticated readiness observation, not proof of Setup or authentication. Keep the configuration/database on ordinary removal and preserve the Project's bounded logging policy.

Directory preparation supports only the reviewed local Linux filesystem/ACL, trusted-parent, held-Project-lock and verified runtime-mapping boundary. Unsupported native filesystems/OSes, present or unknown ACLs, unverified/remote mappings, incompatible ownership/modes and insufficient creation permissions remain actionable Manual before startup, without recursive ownership repair or cleanup of partial paths. A fake Linux daemon description does not establish native Linux filesystem semantics on macOS. Preparation is not a universal Start/restart/Restore repair and never authorizes account creation or proves account freshness.

The `/config` mount uses long bind syntax with `create_host_path:false`; Docker cannot silently create a root-owned missing source on a later start. Do not bypass a failed Install/refresh with another startup path or replace this policy with short bind syntax. Review preserved storage/mappings and retry the approved Install instead. Customized Compose definitions stay outside automatic generated refresh. Standalone Compose users must prepare their selected bind source privately for the configured non-root user themselves; no filesystem or container-runtime acceptance is claimed from source/fake checks.

Sources: [official Docker installation](https://autobrr.com/installation/docker/), [resolved v1.90.0 Dockerfile](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/Dockerfile), [configuration defaults](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/config/config.go), [database selection](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/database/database.go).

Long-bind policy: [Compose volumes specification](https://docs.docker.com/reference/compose-file/services/#volumes), [Compose missing-source bind test](https://github.com/docker/compose/blob/main/pkg/compose/create_test.go).

## Administrator and credential Setup

Setup verifies the intended shared Admin login through a cookie session and `/api/auth/validate`, including its username and password authentication method. First-account creation is allowed only after `/api/auth/onboard` returns 204; the POST endpoint independently rejects an existing user or enabled OIDC. Unknown eligibility, an existing rejected login or stored key records remain Manual. Setup never resets an account, enables a disabled authentication method or guesses that a rejected login means empty storage.

When Homarr is present, Autobrr owns one dedicated key named `self-host Homarr statistics`. **Autobrr stores scopes but does not enforce them: this key is full-access, not technically read-only.** The Link makes only read-only requests. Missing Homarr skips issuance; the existing service scheduler reruns installed targets' Setup when Homarr arrives.

Private values:

- `AUTOBRR_HOMARR_API_KEY`: the issued secret.
- `AUTOBRR_HOMARR_KEY_OWNERSHIP`: the intended administrator, creation name/time, exact empty scopes and secret fingerprint.
- `AUTOBRR_HOMARR_KEY_PENDING`: the shared versioned issuance journal and only the selected returned secret/evidence.

The App first checks logging safety through the intended administrator's password session, then authenticates the saved key against `/api/config` before reading `/api/keys`. Successful key authentication populates that key's upstream validation-cache entry; the following list can verify its positive identity and recorded metadata without falsely rejecting it because another key warmed the cache first. Valid saved credentials are reused; missing, rejected, conflicting or ambiguous records remain Manual, never blind rotation or name-only adoption. Returned raw keys are masked in memory and never persisted wholesale.

**Discovery limit:** at the pinned upstream revision, `/api/keys` returns only validation-cache entries whenever that cache is nonempty; only an empty cache reads all stored keys. A nonempty response is not exhaustive even when no reserved name is visible. Setup holds before intent/POST on every nonempty list, including a visible reserved-name collision. Only an explicit empty array can authorize the first key issuance. No supported exhaustive nonempty discovery route was established. Keep this uncertainty Manual; do not clear caches, restart to manufacture discovery, query a private database or infer ownership/permission from a name. The UI uses the same listing boundary and is not independent proof of absence.

The shared `helpers.CredentialIssuance` writes intent before POST, captures the recoverable secret before status/metadata acceptance, and saves verified ownership, then secret, then clears pending. Partial writes retain the original recovery evidence. An interrupted POST without captured evidence cannot be retried automatically. The guarantee is process-interruption recovery under the existing private writer, not exactly-once issuance, fsync/power-loss safety, rollback safety or a guarantee across Restore.

Sources: [first-user/login contracts](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/http/auth.go), [key routes](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/http/apikey.go), [key issuance](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/api/service.go), [authentication middleware without scope enforcement](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/http/middleware.go).

## Manual recovery and logging

Run `self-host setup autobrr`, follow its specific persisted Manual hint, then `self-host setup homarr`. Review Autobrr Settings > API keys without exposing keys in output. Preserve the known private secret, matching ownership and pending evidence in a private recovery copy; reconcile only the exact recorded credential. Never delete/adopt a key by name or erase an uncertain journal to obtain another key.

The recovery-only private inputs are `autobrr.homarr_api_key`, `autobrr.homarr_key_ownership` and `autobrr.homarr_key_pending`. Empty input preserves saved values. The current CLI accepts private input files on Install, not Setup; reinstall replaces Compose, so preserve/review existing authentication, ports, mounts and edits and obtain separate approval before `install --set-file`. Never use secret-bearing `--set` arguments or blindly reinstall to clear a hold.

Autobrr TRACE logging includes raw API keys. Before issuance and key-authenticated probes, Setup reads logging configuration through the password session and holds as Manual on TRACE or unreadable configuration. It does not silently change the logging choice. Review and explicitly disable TRACE before continuing; do not collect raw credential-bearing logs. Keep TLS verification and selected Access intact.

## Native Homarr surface

Exact kind `autobrr`, only credential alternative `apiKey`. Homarr's declared authentication uses the `apikey` query parameter; direct App probes use the upstream-recommended `X-API-Token` header. Do not output query-authenticated requests. The peer consumes existing Homarr compatibility, ownership and shared Stats freshness mechanisms.

Only the shared `stats` widget is selected:

| Metric           | Unit    | Meaning                                                                                                                            |
| ---------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `approvedPushes` | `count` | Approved action-status rows from `/api/release/stats`.                                                                             |
| `rejectedPushes` | `count` | Rejected action-status rows from the same response.                                                                                |
| `filters`        | `count` | Explicit `/api/filters` array length, including disabled filters.                                                                  |
| `indexers`       | `count` | `/api/release/indexers` options: the union of historical release sources and configured identifiers, not strictly active trackers. |

Native verification first authenticates the exact owned peer key, making its cache entry visible before requesting `/api/keys` through the saved Homarr connection. That response must contain exactly one row matching the recorded name/time/empty scopes, with the key replaced by Homarr's `[REDACTED]` saved-credential marker, and must not return the expected secret in plaintext anywhere in its key rows. Multiple keys can share creation-second/name/scopes, so duplicate matching metadata is Manual in either row order, even when one row is redacted. Plaintext expected key plus a redacted different key is also Manual, including when the plaintext row's metadata differs. Metadata and a marker alone are not unique credential identity; no key is adopted from them. The preceding exact-key authentication, saved connection and private ownership tuple remain required.

One central advancing Stats refresh per verification checks the advertised catalog, units, error/stale flags, timestamps and non-null values. The leaf reuses the reviewed shared `validCountStats`/`countValue` helpers for generic finite nonnegative integral counts and `count` units, retaining its local maximum safe integer, explicit success `retryAt:0`, and age/future-skew policy. Legitimate zero counters and explicit empty arrays are valid; absent/null/error/stale/no-op data is not zero.

Setup never joins trackers, changes filters/download-client choices, processes announcements, retries actions, imports or downloads releases, runs cleanups or seeds positive counters. Homarr unlink removes only recorded Homarr bindings; peer account, keys and persistent data remain intact. Shared widget/layout preservation and retained integrations stay with the existing reconciler.

Sources: [resolved Homarr definition](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/definitions/src/integration.ts), [Autobrr Stats provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/autobrr.ts), [generic-request credential redaction](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/api/src/router/integration/integration-request.ts), [Stats refresh contract](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/request-handler/src/stats.ts), [empty release aggregates](https://github.com/autobrr/autobrr/blob/4c1bf93d3397055de8e78ce136cf1e239e568d92/internal/database/release.go#L913-L975).

## Evidence boundary

Source/contract tests are not native, rendered or lifecycle acceptance. The researched Homarr `latest` image's OCI revision differs from release tag v2.10.1; source package version 2.10.0 is not an authenticated runtime API observation. Resolve both actual images and authenticated versions in an explicitly approved isolated attempt. Planned useful empty acceptance is a positive intended administrator/key identity plus Approved pushes 0 / Rejected pushes 0 / Filters 0 / Indexers 0 with a genuinely advancing refresh. Both installation orders, real invalid-key recovery, rendered values, retained cross-kind survivors and removal/reinstall cycles still require central runtime scheduling.
