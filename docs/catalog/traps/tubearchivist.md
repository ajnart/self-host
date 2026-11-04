# Tube Archivist traps

## Scope and current hold

This is the App-only first slice for #184, based on accepted self-host
`d0961208fa4b768685f75c8df356a2d330473c16`. It defines the real three-container
deployment and verifies an intended existing administrator, but is not registered
in the Catalog yet. Token issuance waits for #150; Homarr's native Stats leaf and
pair admission wait for #147. No automatic token, native, rendered or lifecycle
acceptance is claimed.

`Preflight` deliberately returns an actionable Manual step before installation,
Restore or an update can start these containers. There is no acknowledgement flag
that bypasses the hold. The missing gate is verified Docker-host/VM kernel and
storage prerequisites, scoped resource admission, and containment of upstream
scheduled activity. A working login on an already running instance is a narrower
observation and does not remove that hold.

## Deployment and persistent data

- The project-supported main image is `bbilly1/tubearchivist:latest`. Research
  resolved it and `:v0.6.0` to the same multi-architecture index. This is not a
  running version observation. Setup requires the v0.6.0-or-newer 0.x API contract.
- v0.6.0 removes nginx and rejects `HOST_UID`, `HOST_GID` and `TA_BACKEND_PORT`.
  Arbitrary nonroot Compose `user:` is supported. The container port remains 8000
  when the host port changes. Keep manual image and identity changes intact.
- Required dedicated backing services are `tubearchivist-redis` (`redis:8`, port 6379) and `tubearchivist-es` (official Elasticsearch 9.5.5, port 9200). Neither
  backing service publishes a host port. Do not share another App's Redis. The
  project convenience ES image is amd64-only; the official ES image supports
  amd64 and arm64. Upstream describes arm64 builds as essentially untested.
- The install guide still says Elasticsearch 8. The v0.6.0 release and Compose
  recommend 9.5.5; existing ES 8 data must not be silently upgraded. Review the
  supported 8.19.x-to-9.x migration separately.
- `cache` binds `/cache` and is persistent, despite its name: it contains SQLite
  accounts/tokens, schedules, artwork and backups. Never delete its database to
  reset a login. `redis` binds `/data` with append-only persistence; `elasticsearch`
  binds `/usr/share/elasticsearch/data`, including the configured snapshot path.
- The separate `${MEDIA_ROOT}/tubearchivist` folder binds `/youtube`. Do not adopt
  another archive automatically or change the mount to manufacture statistics.
  Media is outside configuration Backups and needs its own backup. Retain cache
  and both metadata stores; an online filesystem copy is not proof of a
  consistent Elasticsearch backup. No snapshot or history jobs are run by Setup.
- Secret files are mode 0600 and `Keep: true`. Existing administrator and ES
  password files are not overwritten by later shared-credential changes. Review
  a mismatch explicitly rather than replacing a working account's password.
- Main and Redis use the selected shared user/group; ES uses that UID with its
  required group-0 access. ES must never run as UID 0. Rootless mappings,
  writable directories, ownership, and file access remain admission prerequisites;
  no recursive ownership repair is performed. Optional separate media identities
  are not claimed for this image.
- `TA_HOST` supplies explicit origins for the allocated loopback port, internal
  Compose Address and selected Home network/Easy names. It does not broaden port
  publication. Preserve user-added origins, authentication and proxy choices;
  additional Access requires reviewed origin configuration, never wildcard hosts,
  static-auth disablement or TLS verification bypass.

## Credentials and useful checks

- Upstream startup creates a superuser from `TA_USERNAME` and `TA_PASSWORD_FILE`
  only when no superuser exists. Those settings do not reset an existing account.
  This slice does not invoke that bootstrap or infer safe absence from an empty
  folder, a boolean, health response, or login failure.
- Setup uses the existing origin-bound session helper to send the intended Admin
  login to `POST /api/user/login/`, requiring status 204 and a session cookie.
  It then requires `/api/user/account/` to report a positive integer ID, the
  intended name, and explicit staff/superuser privileges. `/api/ping/` must return
  `pong`, that same ID and a compatible version. Account ID 1 is not assumed.
- Setup and Check only authenticate and read those routes; they do not change
  account permissions, reset passwords, query SQLite, read an API token or change
  archive/subscription/settings data. Missing, rejected or ambiguous identity is
  Manual. Restore the intended known credentials through the Project boundary
  and official account recovery, then retry `self-host setup tubearchivist`.
- `/api/health/` only returns unauthenticated `"OK"`; it is readiness, not proof
  of credentials, data integrity or native Homarr acceptance.
- The future Homarr kind is exactly `tubearchivist`, with the `apiKey` alternative
  only. Its header is `Authorization: Token ...`, not Bearer. The supported token
  endpoint is admin-only and may create a token on GET. There are no scoped keys
  in the inspected contract: do not describe an administrator token as read-only,
  issue after uncertainty, or revoke a shared token during Link removal.

## Runtime admission still required

- Upstream estimates about 2 GB available memory for a small test and 4 GB for a
  larger installation. ES's configured 1 GiB Java heap requires additional memory;
  the SDK's default 1 GiB container cap is not adequate admission evidence.
- Request the concrete scoped exception before any runtime: main 2 GiB/2 CPUs,
  ES 3 GiB/2 CPUs, Redis 0.5 GiB/0.5 CPU, Homarr 1 GiB/1 CPU and a real Gatus
  survivor 0.5 GiB/0.5 CPU: five containers, 7 GiB, six CPUs total. This proposal
  is not authorization. Count every helper/dependency, inspect actual caps, keep
  the whole-attempt deadline, and preserve ordinary SDK guards.
- Current Elastic guidance requires the Docker host/VM `vm.max_map_count` to be
  at least 1048576. The older Tube Archivist README value 262144 is not the
  selected image's current production guidance. No host sysctl or VM change is
  performed; host inspection belongs to the existing Host boundary.
- Upstream startup initializes its ES schema and installs reindex, thumbnail and
  version schedules; restarts may maintain metadata and version checks. That
  activity is not assumed passive. Runtime remains held until the coordinator
  has approved containment. Preserve existing user schedules and subscriptions.
  Do not run video downloads, ingestion, subscription rescans, content indexing,
  filesystem scans, history actions or bandwidth jobs to create acceptance data.

## Primary sources

- [Release and migration](https://github.com/tubearchivist/tubearchivist/releases/tag/v0.6.0)
- [Official Compose](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/docker-compose.yml)
- [Install and resource requirements](https://docs.tubearchivist.com/installation/docker-compose/)
- [Environment and secret files](https://docs.tubearchivist.com/installation/env-vars/)
- [Initial-account checks](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/config/management/commands/ta_envcheck.py)
- [Login/account API](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/user/views.py)
- [Account serializer](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/user/serializers.py)
- [Ping and health API](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/common/views.py)
- [Allowed-origin parser](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/common/src/helper.py)
- [Startup and schedules](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/config/management/commands/ta_startup.py)
- [Token acquisition](https://github.com/tubearchivist/tubearchivist/blob/5a18613136122dd8fe83d43d95ea1b8af00d79eb/backend/appsettings/views.py)
- [Elasticsearch image prerequisites](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-elasticsearch-docker-prod)
- [Homarr Tube Archivist provider](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/providers/tubearchivist.ts)
