# Thingport

## Deployment

Thingport is the private 3D printing model library maintained at
[TautvydasDerzinskas/Thingport](https://github.com/TautvydasDerzinskas/Thingport),
not a printer controller or a model provider.

The App uses the official frontend and backend images and PostgreSQL 16. Only
the frontend's port 80 is published, on the selected bind address and allocated
Host port (default 8786). The official frontend proxies `/api` to `backend:8000`.
The backend receives that alias only on the App-owned `thingport-private`
network, not the shared Project network. Do not rename it to a generic global
`backend` service or publish the database/backend ports.

The App keeps the database under `./database` and its model files and pictures
under `./storage`, relative to the App folder. Project owns their rebasing into
the root Compose file. Both folders are part of the App's Backup; the model
library can be large. Keep them and the private signing/database credentials
together during Restore. No existing library, consume folder, external media
path or named volume is adopted. Ordinary removal does not delete these files.

The backend uses its supported PUID/PGID entrypoint and `CHOWN_MODE=minimal`;
do not enable recursive ownership repair to make existing storage work. It runs
upstream database migrations before becoming ready. Fresh generated database
and signing secrets use the existing `SecretDeclarer` boundary; retries retain
them. Only the backend and database receive their required secret values.

FlareSolverr is optional for provider imports, not required for login or library
statistics. This minimal App does not install it, set a fetcher connection,
connect a provider, import/download a model or start printing. Automatic
collection synchronization is disabled with `COLLECTION_SYNC_ENABLED=false`;
remote announcements and release-note fetches use empty
`SYSTEM_NOTIFICATIONS_URL` and `RELEASE_NOTES_URL`. These are fresh Compose
defaults, not API mutations of a person's saved provider preferences. Explicit
provider actions and any change to these defaults require their own approval.

Upstream startup also recovers interrupted jobs, resumes previously requested
storage/backfill work and applies category migrations. Those behaviors are not
permission to adopt existing data or claim read-only runtime. Future runtime
acceptance remains held for an approved disposable Project and a reviewed
startup/dashboard egress plan; this source slice has not executed the images.

## Owner Setup and recovery

Select the existing instance owner's plain email with the public `thingport.email`
install input. Thingport signs in with that email and the shared Admin login's
password shown by `self-host credentials`; the shared username is not its login
email. Setup authenticates through **POST `/api/login`** and reads
**GET `/api/admin/users`** using the returned session. It requires the selected
account's exact returned ID/email, role `ADMIN` and explicit `is_owner=true`,
with one unambiguous owner in the inventory. It records only that actual owner
ID privately. Subsequent Setup and Check refuse a different account ID and
never replace the recorded owner to silence a mismatch. Check does not bind an
unrecorded owner or persist a session.

First-owner creation is a **Manual step**. Public `/api/health` advertises open
registration both on fresh instances and on existing instances where members
may register; it cannot prove that no owner exists. Setup never sends a
registration request after a failed login. Open Thingport privately, create the
first owner only when the instance is actually yours and empty, and retry
`self-host setup thingport`. If the owner already exists, restore the selected
account's correct shared password through the Project credential boundary or
review the selection; do not recreate users, clear the recorded ID, reset their
passwords, bypass CAPTCHA/email verification/OIDC, or broaden Access.

The public health response is readiness only. Successful App verification means
an authenticated, selected and recorded instance owner was read back; it is not
library statistics, token identity or native Homarr acceptance.

## Single API token and deferred Homarr work

Thingport has **one API token per user**. Generating another immediately revokes
the old token used by other consumers. Its profile shows only a suffix and
metadata, not the recoverable secret or a trustworthy token-owner ID. A
Dashboard-only token cannot call a who-am-I route; no `/me` endpoint is invented
here. Library counts or equal empty results cannot establish account identity.

This App-only slice does not mint, revoke, regenerate, adopt, replace or test an
API token. Existing stored tokens are untouched. When Homarr is installed, a
separate `homarr-token` Manual step explains this boundary rather than reporting
success from an existing secret. Automatic issuance/uncertain-response recovery
await the reviewed #150 helper; count/media verification awaits #147. No local
journal, helper copy, Homarr leaf or native widget is included.

The later token owner must use the supported least privilege
`mcp_tools: []`, `rest_scopes: ["dashboard"]` when safe issuance is authorized.
Defaults are broader, including some MCP writes. An existing shared token's
permissions must not be silently narrowed or broadened. Missing/rejected keys
need explicit private recovery, not blind regeneration. Dashboard scope also
permits model metadata in its summary; do not print raw responses. Content
widgets, thumbnails, model-file previews and activity are not enabled by this
App. No models, downloads, jobs, print actions or IDs are fabricated.

Catalog registration, generated site/DCM data, counts and native Homarr wiring
are coordinator-owned follow-up work. This is partial source implementation,
not completed coverage for #181. Native/rendered/lifecycle, invalid-token and
both-installation-order acceptance have not run.

## Primary sources

Source revision inspected for the resolved frontend/backend images:
`e694e3e17925d763127e5d7831f56336cd7ca8a6`. Floating image tags must be resolved
again for runtime acceptance; image labels are not an authenticated API version.

- [Official prebuilt deployment](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/docker-compose.deploy.yml)
- [Frontend proxy and upload contract](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/frontend/nginx.conf)
- [Backend entrypoint, ownership and migrations](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/docker-entrypoint.sh)
- [Backend port and supported environment defaults](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/config.ts)
- [Startup actions](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/server.ts)
- [Disabled synchronization behavior](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/services/collectionSyncSchedule.ts)
- [Announcement fetch configuration](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/services/systemNotificationService.ts)
- [Release-note fetch configuration](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/services/releaseNotesService.ts)
- [Optional FlareSolverr connection](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/services/settingsService.ts)
- [Direct API route mounts](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/app.ts)
- [Registration, login and token API](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/routes/auth.ts)
- [Instance-owner inventory](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/routes/admin.ts)
- [Public readiness contract](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/backend/src/routes/health.ts)
- [Single-token preservation and REST permissions](https://github.com/TautvydasDerzinskas/Thingport/blob/e694e3e17925d763127e5d7831f56336cd7ca8a6/docs/guides/api-token.md)
