# Jellystat traps

## Deployment and persistence

- The official `cyfershepard/jellystat:latest` image listens on container port 3000. Changing the host port does not change the internal Address.
- The App owns `jellystat-db`, an unpublished PostgreSQL 18 service, and waits for its health check. PostgreSQL 18 persists under `/var/lib/postgresql`, with its default data directory at `/var/lib/postgresql/18/docker`; do not substitute the older `/var/lib/postgresql/data` mount.
- App-relative `database/` and `backup-data/` binds retain the database, existing connection, history and Jellystat backup files under the App's Backup boundary. Never attach an older PostgreSQL database, migrate its major, adopt another database or change existing mounts automatically. Restore matching database and JWT secrets with the data; do not rotate them to recover a failed connection.
- Only the App receives its database password and JWT secret. The database has no published port. No media mount, Docker socket, privileged mode, PUID/PGID mapping, master-login override or TLS bypass is supplied.

## Existing-instance Setup and owned keys (#160)

Setup verifies an existing instance; automatic account bootstrap and backend
configuration are not implemented. Fresh or ambiguous accounts remain Manual;
the accepted credential journal does not supply first-account authority.
Configuration state 1 means an account already exists;
upstream `/auth/createuser` can overwrite it. Setup never calls that endpoint,
replaces existing credentials, disables login, edits the selected connection or
directly writes databases. Its only provider mutation is dedicated-key issuance.

The existing shared Admin login is checked through `/auth/login`, followed by
authenticated `/api/getconfig`. CryptoJS calls its legacy Keccak-512 digest
`SHA3`; standardized SHA3-512 is incompatible. The already-present
`golang.org/x/crypto/sha3.NewLegacyKeccak512` supplies the compatible digest.
A token alone is insufficient because upstream's disabled-login mode can accept
arbitrary usernames. The returned App username must match and login must remain
explicitly enabled. Rejected or missing credentials stay Manual; preserve the
account and restore known credentials through the private Project boundary,
then retry `self-host setup jellystat`.

Explicitly select `media_server` (`jellyfin`, the initial default, or `emby`),
private `server_address`, and private `server_admin_id`. The Address must include
any existing API base path, such as Emby's `/emby`, without user information,
query credentials or a fragment. These inputs express the intended existing
backend; they do not configure, choose automatically, replace, or claim it.
Review any existing Compose edits before approving installation or input changes;
reinstalling replaces Compose. The Emby choice renders the documented
`IS_EMBY_API` flag; Setup never changes a running instance's mode.

Authenticated configuration must match the selected Address and backend kind.
Jellystat's `/proxy/getAdminUsers` then performs a fresh read through its existing
media-server credential; the supplied known backend administrator ID must occur
exactly once with its administrator policy. Upstream returns an empty list for
some connection/auth failures, so an empty response is a Manual prerequisite,
not an empty working statistics instance. The proxy itself is public upstream;
its result is used only after authenticated account and configuration matching,
and is not a substitute for Jellystat authentication or a globally unique
server-ID proof. Existing libraries, exclusions, preferred user and task settings
are never changed. Missing positive backend identity remains Manual.

When Homarr is installed, or a saved key/ownership/journal exists, Setup prepares
one dedicated key named `self-host Homarr statistics`. Standalone Setup without
those conditions issues no key. Jellystat keys are unscoped and grant full API
access even though this Link only reads; never describe the credential as
technically read-only or pass it in a query string.

The accepted `CredentialIssuance` helper owns Pending/Begin/Capture/Commit.
Before POSTing `/api/keys`, Setup verifies the account and backend, reads the
authenticated nonempty inventory, refuses an existing unowned creation name and
records a durable intent. An empty `[]` is also upstream's configuration-read
failure fallback, not authoritative absence: it remains Manual before Begin or
POST, even on an otherwise verified instance. A genuinely keyless instance is
therefore not automatically provisioned by this slice.

The response contains every key. A provider-owned incremental JSON projection
retains only the unique new selected-name entry with every prior name/key pair
preserved. It is bounded to 1 MiB, 1024 records and 32 fields per record. A
recoverable selected key survives malformed trailing array/record data; capture
also records nonsecret incomplete, duplicate and error flags. Such evidence stays
held on retries and cannot commit or authorize another issuance. If the selected
key is not uniquely recoverable within those bounds, only the hold is saved.
Every decodable raw key is registered in memory for redaction, even on rejected
responses. The journal retains only the selected new secret, its status and
pinned identity, and aggregate pre-inventory count/digest; other users' raw keys,
names and records are never persisted. Capture precedes HTTP-success and
credential-acceptance checks. Raw bodies and transport errors never become
public diagnostics.

Only an originally successful, unambiguous capture can commit after read-only
key authentication, exact inventory membership and pinned-instance verification.
Ownership binds the key fingerprint, App account/internal Address and selected
backend Address/kind/administrator ID. Rejected or uncertain outcomes, malformed
journals, lost ownership and changed identity remain Manual. Safe shared-journal
storage errors pass through unchanged as failed steps, without their raw private
causes; repair private storage before retrying the same evidence. Reruns may
finish the same verified captured key but never issue a
replacement, adopt a matching name or rotate a key. Preserve and privately
reconcile `homarr_api_key`, `homarr_ownership` and `homarr_pending`; clearing a
journal to cause another POST is not recovery. Upstream has no compare-and-set
issuance API; concurrent independent credential administration is not proven.

In both Check and Apply, the Homarr preflight proves any existing recorded
connection's ID/kind and single private `apiKey` alternative, then uses supported
saved-connection GETs for `/api/getconfig` and `/api/keys`, before any common
credential/settings writes. Homarr redacts only its configured
secret: the unique selected key must be exactly `[REDACTED]`, while a different
masked key is rejected. Thus another valid key to the same backend cannot pass
as the issued key. Failed or unavailable proof preserves the other key, user
Address edits and fingerprint drift, with no Stats request. Positive proof of
the same pinned instance and issued key permits common drift repair; verification
then requires the current internal Address. A stale recorded ID that cannot be
read also remains Manual rather than being blindly replaced. New/unowned-resource
guards and removal without peer access remain unchanged.

No caller-supplied authentication override is sent. These
read-only generic requests require explicitly granted full integration access;
denied or unsupported access remains Manual, never bypassed.

## Activity and acceptance boundary

Connecting a backend starts Jellystat's own session monitoring and scheduled
library synchronization; the first-run UI also starts a full sync. There is no
verified supported configuration that disables every such action. Runtime is
held for a separately approved disposable empty backend, without a playback
reporting plugin, real users' histories or external accounts. Do not connect a
real backend merely to validate this slice, trigger sync/import jobs, restore
viewing/listening history, or manufacture playback activity.

Homarr's `jellystat` kind supports shared Stats only: `songs`, `movies`,
`episodes`, `other`, all `count`, mapped from Audio/Movie/Series/Other playback
views in the last 30 days. They are not media inventory or durations. Legitimate
zeros require positive intended-instance identity and a genuinely advancing
Homarr refresh. The leaf reuses shared count validation and obtains exactly one
central catalog/snapshot/forced-refresh sample per verification invocation;
missing/null, fractional/negative/nonfinite counts, wrong units, errors, stale
or nonadvancing data and out-of-attempt timestamps are rejected. Native identity
checks do not acquire another Stats sample. Source/fake contracts do not claim
native runtime, rendered, scheduler, lifecycle, installation-order or live persistence acceptance.
Catalog registration, metadata and generated data are coordinator-owned and
still required before the App is published.

## Primary sources

Checked 2026-11-02 against Jellystat 1.1.12 source
`10678476792bc88700c574b97d09f300f47f7291`:

- [Official Compose](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/docker-compose.yml), [image publishing](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/.github/workflows/docker-latest.yml), [environment and master overrides](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/README.md).
- [Authentication and account overwrite](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/routes/auth.js), [configuration/key API](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/routes/api.js), [configuration states](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/classes/config.js), [key authentication](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/server.js).
- [Backend proxy](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/routes/proxy.js), [fresh Jellyfin administrator read](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/classes/jellyfin-api.js), [Emby administrator read](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/classes/emby-api.js).
- [Scheduler](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/classes/task-scheduler.js), [distinct history import](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/backend/routes/sync.js), [first-run UI sync](https://github.com/CyferShepard/Jellystat/blob/10678476792bc88700c574b97d09f300f47f7291/src/pages/setup.jsx).
- [PostgreSQL 18 storage](https://github.com/docker-library/postgres/blob/master/18/bookworm/Dockerfile), [CryptoJS padding](https://github.com/brix/crypto-js/blob/develop/src/sha3.js), [upstream Keccak vector](https://github.com/brix/crypto-js/blob/develop/test/sha3-test.js), [Go v0.58.0 compatibility API](https://pkg.go.dev/golang.org/x/crypto@v0.58.0/sha3#NewLegacyKeccak512).
- [Homarr Jellystat Stats provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/jellystat.ts).
- [Saved-connection request and credential-specific response redaction](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/api/src/router/integration/integration-request.ts), [private integration-secret metadata](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/api/src/router/integration/integration-router.ts). The saved-request/redaction mechanism also exists in the historical resolved 2.4.1 source `3d4150e59f600876b7e308f857b2f21572b75348`; actual compatibility and permissions are still checked rather than assumed from a tag.
