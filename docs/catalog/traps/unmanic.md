# Unmanic traps

## Pitfalls

- Pinned Unmanic 0.4.1 requests an external trial token and registers installation metadata on first start. Launch needs an explicit approval or a reviewed containment; this App adds neither and Setup makes no login, trial or registration call.
- All of `/config` matters: `.unmanic/config` (settings and SQLite state), plugins, userdata, logs and `.local`. `./cache` (`/tmp/unmanic`) is scratch. `./library` is an empty read-only App-local folder; no Host media is mounted, so choose and mount libraries deliberately after install.
- Install declares `config/.unmanic/config/settings.json` with `clear_pending_tasks_on_restart` false only when it does not exist. An existing file is never replaced, so queue and settings survive rerun, update and removal of the App's files.
- The image init (`10-init-user`, `20-permission-config`) remaps the user and recursively chowns `/config` and the cache with `chmod -R a+w`. Do not add another recursive chown, and do not expect a 0600 mode inside `/config` to survive.
- Do not call library or worker-group read endpoints from Setup: upstream can create default libraries or worker groups and migrate settings there.
- The pending-task read posts `length: 0` (unlimited) with no library filter. It counts a real queue without enqueuing, but it is not a cheap probe on a large queue.

## Security

- Mount no Docker socket, device, GPU, privileged mode or host networking; processing hardware is the owner's later choice.
- Processing configuration, libraries, workers, plugins and Access stay owner-controlled. Never disable Unmanic authentication to make monitoring pass.
- Setup stores one private `UNMANIC_INSTALLATION_UUID` as identity continuity, not ownership. A changed identity, a login redirect, or HTTP 401/403 is Manual recovery: restore the saved config from a Backup; never re-anchor a different instance.

## Homarr

- Native kind `unmanic`, internal `http://unmanic:8888`, no secrets, one `stats` widget with `activeWorkers` (workers not idle), `totalWorkers` and `recordsTotal` (pending tasks). Zero counts are valid.
- Link preflight and verification reuse the App's read-only version, session identity, worker and pending reads, then one central forced Stats sample. Counts must be integral, non-negative and `activeWorkers` at most `totalWorkers`.
- Runtime, native, rendered and lifecycle evidence, including the startup registration boundary, remain held; see issue #186.
