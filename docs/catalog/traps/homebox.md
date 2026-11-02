# Homebox traps

## Current scope

This is a partial App-only implementation for #156, based on official Homebox
0.27.1 source revision `1fd28822634a3d5935c002d569559ef46530d4a0`.
The App is not yet registered in the Catalog. Automatic first-account creation,
dedicated key issuance and native Homarr wiring remain unimplemented pending
reviewed shared contracts. Source/fake checks are not runtime, rendered-widget,
Backup/Restore or lifecycle acceptance.

## Deployment and preservation

- The maintained official image is `ghcr.io/sysadminsmedia/homebox:latest`, not
  the historical `hay-kot` image. It embeds the web interface and uses SQLite;
  no database container or external account is required. The host port defaults
  to 3100 and maps to container port 7745, respecting the selected bind address.
- Keep the complete App-relative `./data:/data` bind. It includes the SQLite
  database and journal files, local attachments and optional configuration.
  Restart, update and non-purge removal must retain it; no Setup step imports,
  edits or deletes inventory, locations or attachments.
- Keep `HOMEBOX_API_KEY_PEPPER` with the data in private backups. Homebox requires
  at least 32 bytes. Its credential-aware prerequisite runs before Install or
  fragment refresh can generate secrets, and again before startup. Missing or
  short pepper remains Manual, including an apparently new or empty directory:
  this context does not independently prove safe fresh storage. Automatic
  Service-side initial pepper generation is therefore explicitly not accepted
  in this slice. For a separately approved new inventory, provision one strong
  private pepper; for existing data, restore only the original through private
  `homebox.api_key_pepper` / `install --set-file` input from an owner-only file.
  Empty input reuses a saved pepper. Replacing it invalidates every API key;
  never rotate it as credential recovery or adopt data without its pepper.
  The existing secret declaration still supplies standalone export metadata;
  it is not authority to replace missing keys for retained data.
- This uses the regular image's default user. Rootless/hardened variants need
  their documented UID 65532 writable-storage preparation; this App does not
  change ownership recursively, inject unsupported PUID/PGID values or silently
  substitute an image variant.
- Registration, demo seeding, Homebox analytics, release-check requests and
  OpenTelemetry export start disabled through supported environment settings.
  Existing manual Compose choices remain Project-owned. SMTP, OIDC, cloud
  storage, barcode providers, printers and external search are not configured.

## Existing login and inventory selection

- Setup uses only an existing login: the shared Admin password and its email
  form (the Admin username at `selfhost.local`, or the username itself if it is
  already an email). Successful form login returns a token already prefixed
  with `Bearer `; it must not receive another prefix. Setup and Check reuse
  that invocation's successful login through the existing helper.
- For a different intended existing account, restore its independently verified
  private `homebox.account_identity` with `accountId`, `email` and `groupId`.
  Its email selects the existing login without rewriting the shared Admin user
  or the upstream account. Never create a new household to replace a rejected
  login, change an account's password or switch its default inventory to pass a
  check.
- An optional existing raw `homebox.api_key` works without local login,
  including OIDC-only accounts, but requires the matching identity record.
  Supply both through approved `install --set-file` ingestion from owner-only
  files. Empty private input preserves saved values. Reinstall replaces Compose:
  first preserve/review authentication, mounts, ports and manual edits and
  separately approve replacement; do not blindly use `install --yes` for
  recovery.
- Verification reads the credential owner's wrapped `users/self` response,
  checks email, account UUID, default-group UUID and membership, then reads the
  effective group and its currency plus authenticated inventory statistics.
  No tenant override is sent. The first successful Admin login records the
  positively verified account/group selection only after every read succeeds;
  this is not fresh-storage authority or proof that the account was created by
  self-host. Check never writes that record. A different account UUID, default
  group or effective group remains Manual even when counts match.
- Owner here means the authenticated credential owner, not an asserted
  group-administrator role: the read-only self/group API does not expose that
  role. No account/group administration or key issuance is performed or claimed.
- Statistics require explicit nonnegative integral item/location/warranty/user
  counts, at least one member, tags or legacy labels, and a nonnegative value;
  warranty counts cannot exceed item counts. Empty inventory is valid only
  with positive authenticated identity and explicit zero item data. Missing,
  null, malformed or unauthorized responses do not become zeros or success.
- API keys inherit their owning account's write privileges; Homebox has no
  read-only key scopes. Existing-key verification does not issue, rotate,
  refresh, revoke or adopt a key by name, and never falls back to a password
  after key rejection. Default newly issued keys expire after 30 days; that
  future issuance/recovery policy remains pending #150's reviewed helper.

## Manual bootstrap and remaining automatic criteria

Homebox exposes ordinary registration, not an atomic first-user-only endpoint.
Registration being enabled or login rejection does not prove an empty instance.
This App intentionally never calls registration. Missing/ambiguous login is an
actionable Manual step, not completion of the ticket's automatic Setup criteria.
For a genuinely new intended inventory only, explicitly approve temporarily
enabling registration in the existing Compose file, register the intended
account with the known Admin login, then disable registration again. For
existing data, restore its known login or matching private key/identity instead.
Preserve data, pepper and the selected household throughout.

Homarr's later exact `homebox` integration supports username/password or API key
and only the Stats widget. Initial candidate metrics are `items`, `locations`
and `users`; labels, warranty count and currency-formatted total value are
optional. The provider fetches all six values even when fewer are displayed.
Dedicated credential issuance, saved-integration identity, central advancing
Stats verification, both installation orders and ownership-preserving
removal/reconnect must be implemented and accepted separately. No native
coverage is claimed by this App package.

## Primary sources

- [Official installation and images](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/docs/src/content/docs/en/quick-start/install.mdx)
- [Dockerfile, database, storage and healthcheck](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/Dockerfile)
- [Supported environment options](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/docs/src/content/docs/en/quick-start/configure/index.mdx)
- [Required stable pepper](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/app/api/main.go)
- [Local storage](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/docs/src/content/docs/en/quick-start/configure/storage.mdx)
- [Login token and local-login boundary](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/app/api/handlers/v1/v1_ctrl_auth.go)
- [Registration and wrapped self response](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/app/api/handlers/v1/v1_ctrl_user.go)
- [Default tenant and credential ownership](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/app/api/middleware.go)
- [Group fields, membership-based counts and preservation-sensitive operations](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/internal/data/repo/repo_group.go)
- [Unscoped one-time API-key fields](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/internal/data/repo/repo_api_keys.go)
- [Registration side effects and key expiry](https://github.com/sysadminsmedia/homebox/blob/1fd28822634a3d5935c002d569559ef46530d4a0/backend/internal/core/services/service_user.go)
- [Inspected Homarr image revision's Homebox Stats provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/homebox.ts)
