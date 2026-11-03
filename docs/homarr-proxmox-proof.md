# Proxmox external connection: source contract and safety hold

## Outcome

The explicit external-connection path is implemented. **Proxmox native Setup is
Manual before every provider request, credential forwarding or integration
creation.** The inspected Homarr Proxmox client follows redirects; a successful
CLI no-redirect probe cannot establish native confinement. The hold has no input,
environment or version-number bypass. This is not a working native integration
or runtime acceptance claim.

Normal Catalog App Links remain independent. Proxmox is not installed, a Catalog
App, an Unmanaged app, a synthetic Compose service or a target helper. Only
explicit local configuration and recorded owned Homarr bindings are managed.

## Private configuration and recovery

Privately review an owner-only JSON file identifying the exact trusted HTTPS
origin, dedicated read-only token and existing-node/cluster selection. Example
structure with placeholders, not usable credentials:

```json
{
  "endpoint": "https://proxmox.example.invalid:8006",
  "credentials": [
    { "kind": "username", "value": "homarr" },
    { "kind": "tokenId", "value": "monitoring" },
    { "kind": "apiKey", "value": "<private-lowercase-UUID-token-secret>" },
    { "kind": "realm", "value": "pve" }
  ],
  "selection": { "scope": "cluster", "node": "<existing-node>" }
}
```

The supported credential alternative is exactly `username`, `tokenId`, `apiKey`,
`realm`; there is no password, empty or `personalAccessToken` alternative. The
first source path supports the documented dedicated `pve` realm. The native SDK
requires a lowercase UUID token secret. The documented read-only configuration
is `PVEAuditor` at `/` with propagation for both the user and a separated token;
their effective permissions are the intersection. No credentials are issued,
retrieved from databases, replaced automatically or revoked by self-host.

```sh
self-host connections add proxmox --file /absolute/owner-only-input.json --yes
self-host connections status --json
self-host connections update <opaque-id> --file /absolute/owner-only-input.json --yes
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

`--file -` accepts at most 64 KiB of stdin. Regular files must belong to the
Project owner and have no group/other permissions; final-component symlinks and
special files are rejected. Credentials and endpoints are not command arguments
or public status fields. Private records are in the Project's existing `0600`
`.env`; individual fields, not just serialized JSON, are registered for masking.
Homarr-scoped Backup/Restore includes that private record, not external node data.

The returned random identity is stable across compatible credential/selection
updates. Kind and endpoint are immutable: disconnect, then add a new identity to
change either. A same-address user integration never becomes owned. Removal
persists intent first and uses only recorded Homarr IDs; credentials, selection,
Proxmox availability and the native safety hold do not gate cleanup. If Homarr's
API is unavailable, restore it and retry normal Homarr Setup. Unforwarded local
configuration can be removed without Homarr. User-used integrations survive;
released IDs are not reclaimed on reconnect.

The selected node is an identity anchor, not a node filter. Homarr fetches the
token-visible cluster even when the widget displays only node sections. Do not
claim a single-node data boundary or interpret ACL-filtered guest arrays as an
empty cluster. Unknown shapes/fields and missing selections are rejected.

## Inspected upstream contracts

Research on 2026-11-02 resolved published `ghcr.io/homarr-labs/homarr:latest` to
index `sha256:2844201f11ce28b98183728dea2261de2b4fd8ed5480a4eef0b7977cc1a08dc0`,
linux/arm64 manifest
`sha256:2a7af0b6c04ad4b45b7f35b6d6ed91e02b05e6fe1176b371b04f32d35e750e55`,
and OCI revision `df8d03b13452bb50c55a424187e1fa188ab510d2` (OCI version `main`).
That source's package version is 2.10.0. Its relevant Proxmox adapter, credential
definitions, widget map, Stats provider, native health API/options and trusted
fetch matched the filing baseline v2.10.1. This is not an authenticated runtime
API observation or a permanent release allowlist.

- [Homarr Proxmox guide](https://homarr.dev/docs/integrations/proxmox/).
- [Exact credentials](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/definitions/src/integration.ts).
- [Adapter](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/proxmox/proxmox-integration.ts):
  `PVEAPIToken=USER@REALM!TOKENID=UUID`, node-index connection test and cluster
  resources. It derives the SDK host from the origin and drops proxy/API paths.
- [Widget/API contract](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/api/src/router/widgets/health-monitoring.ts):
  `widget.healthMonitoring.getClusterHealthStatus({integrationId})` returns
  `nodes`, `vms`, `lxcs`, `storages`. Its native result omits cache timestamp.
- [Widget support](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/definitions/src/widget-integration-map.ts)
  and [options](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/widgets/src/health-monitoring/index.ts):
  nodes-only `healthMonitoring`, cluster tab; not `systemResources`/`systemDisks`.
- [Stats](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/integrations/src/stats/existing.ts):
  `nodes`, `vms`, `lxcs` are array counts, unit `count`. Only `nodes` is selected.
- [Exact locked SDK](https://unpkg.com/proxmox-api@1.1.1/esm/ProxmoxEngine.js)
  and [Homarr fetch](https://github.com/homarr-labs/homarr/blob/df8d03b13452bb50c55a424187e1fa188ab510d2/packages/core/src/infrastructure/http/request.ts):
  no enforced native no-redirect policy. An upstream audited fix/capability is
  required; no upstream edits or credential-forwarding proxy were made here.
- [Proxmox API](https://pve.proxmox.com/wiki/Proxmox_VE_API) and
  [token privileges](https://github.com/proxmox/pve-docs/blob/master/pveum.adoc).

## Evidence and remaining acceptance

Focused Go fakes/contracts establish private input/identity, additive Setup
scheduling, durable removal, recorded-ID ownership, rerun/retention and no
requests under the Proxmox hold. Pure leaf tests cover native node identity,
CPU fraction, integral cores/bytes/uptime, explicit legitimate zeros, count units
and Stats retry/clock semantics. They are not real Proxmox measurements.

The descriptors specify nodes-only health and shared `nodes` Stats, but the
strict preflight intentionally prevents their creation. The native validator
alone cannot distinguish upstream zero defaults from raw Proxmox observations;
it is not a replacement for the remaining raw endpoint/effective-permission
checks. No raw Proxmox API probe is performed while the transport is held.

Still required before native acceptance:

- audited fail-closed native redirect and trusted-TLS confinement;
- authenticated actual Homarr API/image and Proxmox versions;
- fresh read-only token/effective permissions, raw node/resource fields and
  selected-data identity (without broadening privileges);
- invalid-credential/TLS/redirect observations and one advancing valid Stats
  refresh, distinguishing missing/defaulted data from legitimate zeros;
- rendered meaningful node data, real rerun/remove/reconnect preservation and
  a useful unrelated cross-kind survivor in the centrally approved fixture.

Native, rendered and real lifecycle acceptance are **not run**. No Docker,
browser, installer, provider/account operation or remote Proxmox mutation was
performed. Optional VM/LXC/storage panels, guest lifecycle, cluster enrollment,
storage changes, automatic CA enrollment and external backups are excluded.
