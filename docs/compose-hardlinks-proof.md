# Compose and media storage verification

Verified on 2026-11-01 against `a86aff8f` plus the review fixes accompanying
this document. Three independent reviews covered Standards, specification
correctness and platform behavior. Findings were fixed and regression-tested.

## Local checks

All passed:

- `gofmt -l .` and `git diff --check`.
- `go build ./...`, `go vet ./...`, `go vet -tags e2e ./e2e/`.
- `go test ./...`.
- `go test -race ./internal/tui ./internal/activity ./internal/project ./internal/service ./internal/compose ./internal/helpers ./internal/host`.
- `bash -n install.sh`.
- Regeneration with `go run ./tools/sitegen`, with no generated-data drift.
- The web build, tests, lint, typecheck and `test:compose` gate using Bun 1.4.3.
  Compose validated every supported export individually, all self-host Apps
  together and all supported services together.

The final binary cross-built with `CGO_ENABLED=0` for Linux and macOS, both
amd64 and arm64. The native Linux amd64 binary executed `--version`, `--help`,
`apps list --json` and `skill` with an isolated Home folder.

## Docker runtime

Host: Linux amd64, rootless Docker 29.3.0, Compose 5.1.0. Each acceptance target
used unique temporary Compose Projects, isolated storage and loopback-only
ports. Cleanup was scoped to those Projects.

The acceptance command was:

```sh
E2E_TARGETS='media ntfy vaultwarden' \
E2E_LOG_DIR=/tmp/self-host-full-review/e2e-final \
GOFLAGS=-buildvcs=false GOCACHE=/tmp/selfhost-gocache \
go test -tags e2e -run '^TestE2E$' -v -timeout 30m ./e2e/
```

The harness now selects shared media storage with `--data-root`; it observes
and cleans up canonical Projects using the private `.env`. An initial run
exposed the harness's obsolete mandatory `secrets.env` argument. That argument
was fixed, the retained temporary Project was recovered, and acceptance was
rerun from clean Projects.

| Target | Verified behavior |
| --- | --- |
| Media Recipe: Jellyfin, Prowlarr, qBittorrent, Radarr, Seerr, Sonarr | Install, Setup/checks, idempotency, restart, stop/start recovery, update, Backup, isolated Restore, restored checks and matching data sentinels. |
| ntfy | The same lifecycle, plus a real saved notification recovered from Backup. |
| Vaultwarden | Install, Setup/checks, idempotency, restart, stop/start recovery, update, Backup, isolated Restore, restored checks and matching data sentinels. |

Separate temporary probes inside Sonarr and Radarr, run as their `abc`
application user, created links from the torrent category to the library and
verified equal device/inode pairs. Probe files were removed. This establishes
hardlink capability on this storage, not that a real Arr import used a link.

Ordinary `docker compose config --services` and `docker compose ps` also found
an isolated Project from its directory without self-host, `-p` or `--env-file`.
The persisted name and service matched the running Project.

Sanitized acceptance evidence is in the local directory named above. Archives,
credentials and private logs are not committed.

## Review safeguards

Regression tests cover:

- Conflicting volumes, networks, configs and secrets shared with manual services,
  including implicit default networks: Restore refuses before replacing data.
- Include-local `.env` files: consolidation refuses before writes rather than
  dropping their interpolation scope.
- Environment-backed Compose secrets/configs, indirect interpolation inputs and
  shared assignments whose quoting changes their meaning.
- Supplementary Linux groups and existing media directory symlinks, while
  keeping separate-user configuration private.
- NSS-aware UID allocation and fresh Restore collision checks, with no automatic
  reassignment of archived file owners.
- Separate-user Restore refusal on rootless, remapped or non-Linux destinations.
- Scoped, application-user configuration reads for separate-user API key recovery.

## Platform limits

| System | Evidence from this run |
| --- | --- |
| Linux amd64, rootless Docker | Native tests, binary execution and Docker lifecycle acceptance above. |
| Linux arm64 | Release cross-build; native tests require its CI runner. |
| macOS amd64 and arm64 | Release cross-build; native tests require their CI runners. Docker Desktop/OrbStack storage was not exercised here. |
| WSL2 | Uses the Linux binary; no Windows/WSL2 runtime was available for this run. |
| Linux rootful, supplementary groups, user namespace remapping | Focused mocked mapping/identity tests and real local filesystem tests. A separate rootful Docker daemon was not exercised. |

The existing four-platform native CI matrix now runs the Project, Host, Compose,
Helpers and service tests as well as its binary/installer smoke checks. This
records added CI coverage, not a claim that those remote jobs ran locally.

Separate App users remain optional and off by default. Unsupported namespace
mappings require explicit ownership reconciliation. NAS permissions, separate
datasets, APFS/NTFS sharing and every Catalog App still need their own runtime
evaluation; this run does not establish those outcomes.
