# Homarr and external NetAlertX

NetAlertX is connected only as an external connection of kind `netalertx` with one `apiKey` credential. There is no NetAlertX Catalog App, installer, discovery, scan or device adoption.

## Endpoint and credential

- The endpoint is the NetAlertX backend API origin (default port 20212), not the web UI (20211). It must be an `http` or `https` origin without embedded credentials, path, query or fragment.
- The credential is NetAlertX's global `API_TOKEN`, supplied only through the owner-only private input. NetAlertX has no read-only token scope, so the token grants more than Homarr uses. Approve it explicitly; no scope is claimed.
- The connection takes no selection: the Stats are instance-wide.

## Data

Homarr's Stats provider calls `GET /devices/totals` with `Authorization: Bearer <API_TOKEN>` and reads indices 0, 1, 3 and 4 as `total`, `connected`, `newDevices` and `downAlerts`, all unit `count`. Setup verifies the same call through Homarr's `integration.request` and requires nonnegative integers at those indices.

- `connected <= total` is not required (archived devices can break it).
- `newDevices` and `downAlerts` can overlap; they are never summed or partitioned.
- Zeros are accepted. Missing, null, negative, fractional, text, errored or non-advancing refreshes are rejected.

## Recovery

A rejected or lost token, a wrong port or an unreachable backend is a Manual step that preserves the connection. Fix the private input with `self-host connections update <id> --file PATH --yes`, then run `self-host setup homarr`. An invalid token may raise an alert inside NetAlertX; only a controlled endpoint should be used to test it.

## Removal

Removing the connection releases only the recorded Homarr integration and widget bindings. NetAlertX is never contacted or changed.

## Evidence

Source and fake-backed tests cover validation, totals, Stats and removal. Runtime evidence (connect, invalid credential, native data, rerun, removal and reconnect against a controlled NetAlertX) and the peer's product version remain unobserved.
