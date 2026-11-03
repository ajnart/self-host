# Homarr external Coolify contract

Connects an existing, authorized Coolify instance to Homarr through the external-connection seam (kind `coolify`). No Catalog App, installer, Docker socket, deployment, server change or remote lifecycle.

## Private input

Supply through the owner-only private input file:

- `endpoint`: trusted-HTTPS root origin only (no `/api` or `/api/v1` path, query, fragment or userinfo).
- `credentials`: exactly one `apiKey`, a dedicated Coolify Bearer token with `read` permission only (never `read:sensitive`, `write`, `deploy` or `root`).
- No `selection`.

## Prerequisites

- API access enabled in Coolify (Settings > Advanced > API access) and this host's IP allowed. Nothing toggles them automatically.
- The token must cover the whole intended team: Homarr always reads servers, and project-scoped Coolify tokens cannot read servers or team routes. Confirm the team-wide read disclosure is acceptable before connecting.
- Trusted TLS in both the CLI and Homarr. Never disable verification.
- A lost or rejected token is Manual recovery: update the connection's private input, then run `self-host setup homarr`.

## Surfaces

- Native widget `coolify` (options `showServers`, `showApplications`, `showServices`, all true), read through `widget.coolify.getInstancesInfo` for this connection's owned integration.
- Stats `applications`, `services`, `servers`, unit `count`: inventory totals, not running or healthy counts. Genuine zeros are valid only with an exact integration identity and a real version.

Native data is accepted only with the exact integration ID and URL, no error, a nonempty version, explicit arrays with unique nonempty `uuid` and `name`, and an `updatedAt` at most 30 s old and 5 s ahead. Stats need one advancing forced refresh with valid retry metadata.

Disconnect removes only Homarr-owned bindings and the local private connection; Coolify is never contacted.

## Acceptance status

- Source and fake-backed tests: implemented.
- Native transport: HELD. The audited Homarr Coolify client's redirect and trusted-TLS behavior is not established, so Setup stops with a Manual hint before forwarding the token or creating an integration. Unblocking decision: an audited adapter that fails closed on redirects and untrusted TLS.
- Runtime and rendered evidence: not run (needs a controlled endpoint; planned value one reachable `fixture-server`, Servers 1 / Applications 0 / Services 0).
- Upstream pins: Homarr `v2.10.1` (`0f0987d2ed013c93ce30c1c00786983d29f4b956`); no Coolify version observed.
