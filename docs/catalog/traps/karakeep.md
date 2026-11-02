# Karakeep

## Deployment and persistence

The App uses the official `ghcr.io/karakeep-app/karakeep:release` all-in-one
image and the upstream-supported minimal SQLite deployment. Its service is
`karakeep`, container port 3000; the host port is allocated independently. The
Project's selected bind address applies to that published port. No database,
search or browser companion is required for the minimal deployment.

`./data:/data` retains the SQLite database, its sidecars, bookmarks, tags,
accounts and default asset storage. Preserve that whole directory, the private
signing secret and recorded account identity during updates, removal and
Restore. Ordinary removal must not delete data. Custom asset mounts or an
existing S3 backend are separate storage choices: do not switch or adopt them
through Setup, and do not assume a Project Backup includes external storage.
The image performs its own database migrations on startup; review upstream
updates and keep a Backup before upgrading existing data.

`NEXTAUTH_SECRET` comes from the retained private `KARAKEEP_AUTH_SECRET`.
`NEXTAUTH_URL` initially uses the Home network or This computer only Address.
Preserve any reviewed proxy Address, authentication policy, trusted origins and
manual Compose edits. Do not add arbitrary PUID/PGID, a forced container user,
recursive ownership changes, a Docker socket or another App's environment.

## Contained minimal defaults

Signups and release checks start disabled. The supported worker allowlist
selects only `backup`, and the supported disabled-worker list disables that
same worker. No background worker is enabled, including new names introduced
by later images, both crawler queues, asset preprocessing, imports, backups and
maintenance. Automatic tagging,
summarization, embeddings and OCR are also disabled. This prevents an existing
queue from turning verification into crawling, model downloads, webhook/feed
requests, automatic data maintenance or paid provider work. The App can store
and read manually saved bookmarks, notes and files, but background processing
is intentionally unavailable until a person separately configures it.

This is more restrictive than upstream's minimal example: omitting Chrome
alone still permits plain HTTP crawling. Search needs an explicitly selected
Meilisearch service; screenshots/JavaScript crawling need a browser; AI and OCR
need their separately approved configuration. No optional service, key, cookie
profile, model or demo data is added by Setup. Enabling background features is
a deliberate configuration change, not a way to make an empty Stats widget
look populated. Recheck the enable/disable contract when reviewing a different
image version; the floating `release` tag does not prove unchanged behavior.

## Existing administrator verification

This initial App slice does **not** create the first account. A failed login
does not prove an empty database, and the inspected API has no public safe
first-account existence probe. A new empty instance remains Manual. Keep it
private; separately approve first-account creation, briefly enable signups in
the existing Compose definition, create the intended first administrator, then
disable signups and recreate the container. Never create another user or reset
someone's password to resolve rejected credentials.

Setup uses the shared Admin password and defaults to the Admin username at
`selfhost.local`. The optional private `karakeep.admin_email` input selects an
existing local administrator instead. `self-host credentials` identifies the
selected login without putting a password in a command. Preserve SSO-only,
email verification and CAPTCHA requirements as Manual boundaries; do not
disable them or bypass certificate/origin validation.

The `owner` step signs in through `/api/auth/sign-in/email`, verifies the
unexpired authenticated `/api/auth/get-session` administrator and checks the
same local account through `/api/v1/users/me`. A health response, login token
or HTTP 200 alone is not acceptance. Temporary verification sessions are signed
out; an unconfirmed sign-out fails verification rather than silently leaving
successful Setup. Responses and session credentials are not printed.

Only after positive verification does Setup retain `KARAKEEP_ACCOUNT_ID` and
the selected private `KARAKEEP_ADMIN_EMAIL`. Check does not write them. Reruns
must match the recorded ID even when another account has the same email.
For missing/mismatched identity, restore the original account and matching
private credential record from a Backup, or independently review the recorded
identity before using the optional private recovery input
`karakeep.account_id`. Never clear the ID to accept a replacement account.
Empty private recovery inputs preserve saved values. Reinstall replaces
Compose: preserve and review manual definitions and separately approve any
replacement before using `install --set-file`. Retry `self-host setup karakeep`
after resolving the reported Manual prerequisite.

## Dedicated read-only credential

After the existing owner is verified, Setup prepares one key named
`self-host Homarr statistics` with exactly `users:read`, not the guide's loose
bookmark-read wording, write access or full access. It is prepared even when
Homarr is absent, so later installation does not require Homarr to issue a
credential on Karakeep's behalf. Key creation and listing use the positively
verified owner's session-only tRPC procedures; password-to-key exchange is
never used as a login or discovery check.

The selected account, returned resource ID, token's separate `keyId`, exact
name/scope and secret fingerprint form the retained private ownership tuple.
A valid tuple is read back and reused. An existing same-name key without its
known matching private record is preserved as an unowned collision; it is not
adopted, deleted or replaced. A rejected key or changed metadata stays Manual.

Issuance uses the reviewed shared `helpers.CredentialIssuance` journal's four
operations: Pending, Begin, Capture and Commit. A complete authenticated
preinventory is recorded before the one non-idempotent request. Its returned
one-time secret and complete private response are captured before accepting
status, scopes, identifiers or bearer account identity. Verification then
requires the new resource/keyId to be absent from the preinventory, present in
a fresh session-owned inventory with exact metadata, and authenticated as the
recorded account. A timeout, lost capture, malformed response, storage failure
or unresolved outcome never permits a second issuance. Captured, fully
verifiable evidence can finish the same issuance after private storage is
repaired; uncertainty remains Manual.

Keep `KARAKEEP_HOMARR_API_KEY`, `KARAKEEP_HOMARR_KEY_OWNERSHIP` and
`KARAKEEP_HOMARR_KEY_PENDING` private. The corresponding optional private
recovery inputs restore only independently confirmed matching records; empty
inputs preserve saved values. Preserve the journal even when active records
were partly saved. Do not clear a hold, regenerate, revoke or issue a new key
to turn it into success. Review only the recorded credential in Settings >
API Keys, resolve its known private evidence, then retry
`self-host setup karakeep` followed by `self-host setup homarr`.

## Native Homarr connector and remaining boundary

Automatic first-account bootstrap remains Manual; no fresh-account provenance
was added. Catalog/global registration and generated exports are centrally
assembled, not performed by this peer package.

The native kind is `karakeep`; its supported surface is shared Stats with
`bookmarks`, `favorites`, `archived`, `highlights`, `lists` and `tags`, all in
counts for the selected account. Homarr consumes the peer-owned API key at the
internal `http://karakeep:3000` Address, separately from the browser Address.
Its leaf freshly verifies credentials before mutation, then verifies the
saved Homarr connection identifies the same account; equal zero counts alone
do not prove that identity.

The central Stats pipeline obtains one catalog and one advancing forced
refresh per verification invocation. The leaf uses shared `countValue` and
`validCountStats`, exact count units, integral nonnegative values, successful
retry metadata and bounded clock policy. Explicit zeros are valid; missing,
null, negative, fractional, nonfinite or wrong-unit values are not zeros.
Favorites and archived counts may overlap, but each cannot exceed bookmarks;
tags/lists/highlights need not be bounded by bookmark count. The leaf does not
acquire another Stats sample or compare independently cached counters.

Shared reconciliation preserves owned IDs, user widgets/options and unrelated
cross-kind entries. Retained user connections are released, never reclaimed on
reconnect; removal does not need the peer's credential or a Stats refresh.
Focused fake-backed checks are not native/rendered/runtime/lifecycle proof;
both real installation orders, actual values and survivor behavior still need
their centrally approved isolated attempt.

## Sources

- [Official minimal deployment](https://github.com/karakeep-app/karakeep/blob/v0.34.1/docs/docs/02-installation/07-minimal-install.md)
- [Official full Compose and optional companions](https://github.com/karakeep-app/karakeep/blob/v0.34.1/docker/docker-compose.yml)
- [Official image and startup migrations](https://github.com/karakeep-app/karakeep/blob/v0.34.1/docker/Dockerfile)
- [Configuration, storage and supported disable flags](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/shared/config.ts)
- [Actual worker registry and enable/disable semantics](https://github.com/karakeep-app/karakeep/blob/v0.34.1/apps/workers/index.ts)
- [SQLite database path](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/db/drizzle.config.ts)
- [Auth endpoints, first-user role and policy checks](https://github.com/karakeep-app/karakeep/blob/v0.34.1/apps/web/server/auth.ts)
- [Auth client and same-origin tests](https://github.com/karakeep-app/karakeep/blob/v0.34.1/apps/web/lib/auth/client.ts), [tests](https://github.com/karakeep-app/karakeep/blob/v0.34.1/apps/web/server/auth.test.ts)
- [Current account REST routes](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/api/routes/users.ts), [account model](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/trpc/models/users.ts)
- [Scoped user procedures](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/trpc/routers/users.ts), [scope enforcement](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/trpc/index.ts)
- [Session-only issuance/list and explicit scope default](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/trpc/routers/apiKeys.ts), [separate keyId/resource ID and key format](https://github.com/karakeep-app/karakeep/blob/v0.34.1/packages/trpc/auth.ts)
- [Resolved Homarr Karakeep provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/karakeep.ts)
