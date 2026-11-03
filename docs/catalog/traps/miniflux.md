# Miniflux traps

## Source-only App and connector

This #166 slice supplies the real App and PostgreSQL definition, read-only
verification of an existing reader, App-owned Homarr credential issuance and a
native Homarr leaf that is explicitly held as Manual. Automatic account bootstrap
also remains Manual. The native exact-saved-key requirement is not implemented
by the inspected supported contract; this is partial coverage, not merely an
unperformed runtime check.
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

## Homarr credential and native data contract

- Homarr's exact `miniflux` kind requires `apiKey` and supports Stats only:
  `read` and `unread`, both unit `count`. These are stored read/unread entries
  belonging to the token's reader, not global or lifetime totals.
- Miniflux tokens use `X-Auth-Token` and inherit their account's full privileges;
  upstream has no read-only token scope. An explicitly intended non-admin reader
  can supply Stats, but creating a different empty account changes the data's
  meaning and is not a least-privilege substitute for the intended reader. This
  implementation deliberately selects the same recorded administrator reader.
  The App-owned token must verify that exact ID, username and explicit
  administrator authorization. This proves the local token's account, not
  which valid token Homarr actually saved for that account.
- The supported API can list/create application keys since Miniflux 2.2.9,
  using Basic authentication for the selected owner. When Homarr is installed,
  Setup inspects that reader's complete official API-key inventory before the
  only issuance POST. A case-insensitive `self-host Homarr statistics` description
  collision, invalid inventory, saved secret/ownership or any pending uncertainty
  prevents new issuance; a name match never grants adoption or deletion rights.
- Miniflux uses the shared `helpers.CredentialIssuance` four operations. `Begin`
  acknowledges an intent before the POST; `Capture` checkpoints an extractable
  returned token and selected status/ID/owner/description/creation evidence
  **before** semantic acceptance. This includes recoverable tokens in malformed
  or unsuccessful responses. No whole inventory or other user's keys are saved.
  `Pending` reads only, and `Commit` publishes exactly the verified ownership
  and token before clearing the original journal. No second journal or reset
  mechanism is added by the App.
- The private tuple is `MINIFLUX_HOMARR_API_KEY`,
  `MINIFLUX_HOMARR_TOKEN_OWNERSHIP` and `MINIFLUX_HOMARR_TOKEN_PENDING`.
  Ownership binds key ID, reader ID/username/administrator authorization,
  description, creation timestamp and the token fingerprint. Normal App-owned
  verification requires no pending journal, exact local token identity and exact
  official inventory membership. It does not attest Homarr's saved secret.
  Valid credentials are reused without writes. Rejected/lost keys,
  changed ownership, unknown status, intent-only interruption and incomplete
  evidence stay Manual; even an empty subsequent inventory cannot authorize
  reissuance. Captured recovery repeats the same write barrier and verifies the
  same credential before committing; it never posts again.
- Private recovery inputs `miniflux.homarr_api_key`,
  `miniflux.homarr_token_ownership` and `miniflux.homarr_token_pending` restore
  only an independently reconciled original tuple. Keep all evidence; do not
  clear a journal, rotate credentials, guess ownership by name or substitute a
  different reader. Empty input values preserve stored records. The same
  explicit Compose-replacement review/approval boundary described above applies.
- **Native exact-key hold:** Miniflux permits several valid keys for one user.
  Substituting key B into Homarr while locally owned key A remains valid can
  return identical `/v1/me` identity and identical Stats. Account checks, the
  local fingerprint, all-key inventory, timestamps or matching zero counts do
  not prove the exact credential Homarr used. The prior account-only acceptance
  path was removed, not relabeled as exact-key proof.
- The bounded current official contract inspection found no supported
  current-authenticated-key ID/attestation endpoint. Miniflux's middleware binds
  user identity, not key identity, to the request; `/v1/api-keys` lists all that
  user's keys. Homarr's private `apiKey` metadata exposes kind and update time
  with a null value, not a credential attestation. Generic response redaction
  markers are not an authentication contract. None of these is a substitute
  for supported native key-specific proof.
- Read-only preflight verifies the App-owned tuple, then returns a fixed
  Miniflux-specific `helpers.ManualError` **before any Homarr request or mutation**.
  The native verifier returns the same hold before Stats catalog/snapshot/refresh
  acquisition. No fresh or existing integration is admitted, even for identical
  valid same-account keys. Keep using Miniflux directly while preserving keys,
  ownership, issuance evidence and existing Homarr resources. Do not overwrite
  B, rotate/delete keys, inspect/decrypt private Homarr storage, invent a current-key
  route or add an override/fingerprint-as-proof to force admission. The exact-key
  criterion remains unmet until a separately reviewed supported contract can
  prove it; no upstream Homarr change is authorized by this slice.
- The unsafe native `apiKeyWrite`/`decodeProcedure` request path was removed
  entirely. Arbitrary Homarr message/body/transport errors cannot enter public
  output or a retained private cause through this leaf. The fixed exact-key
  hold retains only the public Manual sentinel, not an untrusted response or
  wrapped cause that future redaction would have to repair.
- Planned Stats still uses `read`/`unread` and the accepted pure shared count
  helper for finite, nonnegative integral numbers with `count` units, including
  legitimate zeros. The validator remains tested, but it cannot grant native
  admission and **no central Stats sample is acquired while this hold remains**.
  The existing shared ownership/removal mechanism is unchanged; no Miniflux
  account, feed or key deletion is implemented.
- Catalog/target assembly, both installation orders, actual resolved image/API,
  rendered values, Backup/Restore and ownership-preserving runtime lifecycle
  remain central/not run. Neither the source leaf, an App healthcheck nor an
  empty feed list establishes native runtime acceptance.

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
- [Supported user-scoped key API](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/api/api_key_handlers.go)
- [Exact supported routes; no current-authenticated-key endpoint](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/api/api.go)
- [Key inventory, token generation and creation metadata](https://github.com/miniflux/v2/blob/faecfa2fac61a91f4f69550bb1afd506c4b51749/internal/storage/api_key.go)
- [Homarr saved-integration request envelope](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/api/src/router/integration/integration-request.ts)
- [Homarr integration metadata hides private key values](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/api/src/router/integration/integration-router.ts#L166-L234)
- [Homarr apiKey is private](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/definitions/src/integration.ts#L11-L12)
- [Homarr Miniflux Stats provider](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/integrations/src/stats/providers/miniflux.ts)
