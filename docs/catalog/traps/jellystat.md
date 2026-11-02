# Jellystat traps

## Deployment and persistence

- The official `cyfershepard/jellystat:latest` image listens on container port 3000. Changing the host port does not change the internal Address.
- The App owns `jellystat-db`, an unpublished PostgreSQL 18 service, and waits for its health check. PostgreSQL 18 persists under `/var/lib/postgresql`, with its default data directory at `/var/lib/postgresql/18/docker`; do not substitute the older `/var/lib/postgresql/data` mount.
- App-relative `database/` and `backup-data/` binds retain the database, existing connection, history and Jellystat backup files under the App's Backup boundary. Never attach an older PostgreSQL database, migrate its major, adopt another database or change existing mounts automatically. Restore matching database and JWT secrets with the data; do not rotate them to recover a failed connection.
- Only the App receives its database password and JWT secret. The database has no published port. No media mount, Docker socket, privileged mode, PUID/PGID mapping, master-login override or TLS bypass is supplied.

## Read-only Setup slice (#160)

This initial slice verifies an existing instance; automatic account bootstrap,
backend configuration and API-key issuance are not implemented. Fresh or
ambiguous accounts remain Manual until the reviewed shared issuance/bootstrap
contract is available. Configuration state 1 means an account already exists;
upstream `/auth/createuser` can overwrite it. Setup never calls that endpoint,
changes credentials, disables login, or writes configuration or databases.

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

An optionally restored private `homarr_api_key` is checked read-only against the
same authenticated configuration. It is not claimed as managed ownership.
Jellystat keys are unscoped: a dedicated key can authorize writes even when its
consumer only reads. Never describe such a key as technically read-only or pass
it in a query string. No key list, key creation, deletion, rotation or independent
issuance journal is used here. Missing automatic issuance and the Homarr leaf
remain pending the reviewed #150 and #147 contracts.

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
Homarr refresh. This App-only slice does not implement or claim that native,
rendered, scheduler, lifecycle, installation-order or live persistence acceptance.
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
