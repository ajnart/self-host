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

## Partial implementation boundary

Automatic first-account bootstrap, dedicated key issuance and the Homarr Link
are not part of this initial slice. No pending-key journal or token is issued
while the shared issuance helper is awaiting review. Existing API keys are
preserved untouched. The eventual read-only Homarr key needs exactly
`users:read`, not the guide's loose bookmark-read wording or full access.

The native kind is `karakeep`; its supported surface is shared Stats with
`bookmarks`, `favorites`, `archived`, `highlights`, `lists` and `tags`, all in
counts for the selected account. Stats verification must consume the reviewed
shared count/freshness helpers in the connector continuation. Catalog
registration, generated exports and native/rendered/lifecycle acceptance
remain pending; focused fake-backed App checks are not those proofs.

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
- [Resolved Homarr Karakeep provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/karakeep.ts)
