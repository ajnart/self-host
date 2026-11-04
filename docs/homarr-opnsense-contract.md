# OPNsense external connection: source contract

## Outcome

`opnsense` is an exact Homarr integration kind added as an external connection
on the existing external-connection seam. It is not a Catalog App, Compose
service or Unmanaged app. Setup never changes firewall, interface, VPN,
certificate or DNS configuration, and removal leaves OPNsense untouched.

Source pins: Homarr v2.10.1 (`0f0987d2ed013c93ce30c1c00786983d29f4b956`),
OPNsense production reference `26.7.8` (`96978001`).

## Private configuration

An owner-only JSON file with an authorized HTTPS origin (no userinfo, path,
query or fragment) and exactly one `opnsenseApiKey` and one `opnsenseApiSecret`.
No selection. The secret is shown once by OPNsense and stored hashed upstream;
a lost or rejected pair is a Manual recovery: create a new pair, then
`self-host connections update <id> --file PATH --yes` and
`self-host setup homarr`. No scraping and no blind rotation.

## Read-only surface

- Identity: `GET /api/diagnostics/system/system_information` with HTTP Basic
  (key as user, secret as password), no redirects. Requires a JSON object with a
  nonempty `name` and a nonempty first `versions` entry; nothing is printed.
  401 means rejected or missing auth, 403 means the user lacks the privilege.
- Homarr binds one `stats` CPU metric (`cpu`, unit `percent`). Native readback
  is `widget.firewall.getFirewallCpuStatus` for the owned integration ID:
  one result, no error, `summary.total` finite in `[0,100]`. Zero is valid;
  missing, null, errored, unowned or stale readings are rejected.
- Stats: unit `percent`, finite `cpu` in `[0,100]`, `retryAt` 0, valid
  timestamp not before the refresh start and at most 30 seconds past its end.

## Prerequisites

- Trusted HTTPS certificate in both the CLI and Homarr; no TLS bypass.
- Dedicated enabled, unexpired, non-administrator API user with **Lobby:
  Dashboard** only. The optional firewall widget additionally needs
  **Reporting: Traffic**; it is documented, not installed.
- Working diagnostics CPU stream (`/api/diagnostics/cpu_usage/stream`), read
  only by Homarr's native consumer, never by the CLI.

## Not run

No live OPNsense, Homarr image digest, authenticated product version, rendered
dashboard, SSE transport or lifecycle (connect, loss/recovery, endpoint change,
disconnect) acceptance was run. Evidence is source plus fake-backed tests.
