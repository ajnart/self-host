# Unraid external connection: source contract and safety hold

## Outcome

The explicit external-connection path accepts the exact `unraid` kind with the
single `apiKey` credential alternative and one read-only surface: CPU and memory
health plus Stats (`cpu`, `memoryUsed`, `memoryAvailable`, `uptime`).
**Unraid native Setup is Manual before every provider request, credential
forwarding or integration creation.** Homarr's Unraid client sends the key to
`POST /graphql` and its redirect/TLS confinement is not audited, so a CLI probe
cannot establish native confinement. The hold has no input, environment or
version bypass. This is not a working native integration or acceptance claim.

Unraid is not installed, a Catalog App, an Unmanaged app or a Compose service.
self-host never installs the Unraid API plugin, reboots, or touches the array,
shares, containers or VMs. Removal changes only local configuration and recorded
owned Homarr bindings; Unraid is never contacted.

## Private configuration and recovery

Use an owner-only JSON file with the exact trusted HTTPS origin (no path, query,
fragment or userinfo) and one dedicated key. There is no selection:

```json
{
  "endpoint": "https://unraid.example.invalid",
  "credentials": [{ "kind": "apiKey", "value": "<private-api-key>" }]
}
```

```sh
self-host connections add unraid --file /absolute/owner-only-input.json --yes
self-host connections status --json
self-host connections update <opaque-id> --file /absolute/owner-only-input.json --yes
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

Prerequisites you provide: Unraid 7.2 or newer, where the Unraid API is native
(an older server needs its plugin, installed by you), and a dedicated key with
`READ_ANY` on `INFO` and `ARRAY`. An `INFO`-only key passes Homarr's connection
test but fails real data. Do not use ADMIN or VIEWER presets; a key that reads
successfully is not proof it lacks write grants. Keys are never issued,
retrieved or rotated by self-host.

## Native contract

- Homarr kind `unraid`, only alternative `apiKey`, header `x-api-key`; adapter
  queries `POST <origin>/graphql` and rejects non-2xx and any `errors` member.
- Native check: `widget.healthMonitoring.getSystemHealthStatus` with the owned
  integration ID. Exactly one matching result with no error, non-null
  `healthInfo`, CPU 0..100, integral nonnegative memory bytes with a positive
  total, and finite nonnegative uptime. Genuine zeros are accepted; an empty CPU
  array, null or defaulted data is rejected. Homarr's `version` is `info.os.release`
  and is not validated as an Unraid version.
- Stats require units percent/bytes/bytes/seconds and one genuinely advancing
  refresh with valid clock and retry metadata. The refresh is a Homarr cache
  write, not an Unraid mutation. Network, GPU and temperature are not provided
  and are not shown as zeros.

## Evidence and remaining acceptance

Focused Go fakes cover private input validation, the Manual hold with zero
provider or native requests, native and Stats validation including legitimate
zeros, and removal without any request. They are not Unraid measurements.

Still required: audited fail-closed native redirect and trusted-TLS confinement;
authenticated Homarr and Unraid versions; effective key permissions and raw
fields; one advancing Stats refresh; rendered data and real rerun/remove/
reconnect preservation in the approved fixture. Native, rendered and lifecycle
acceptance are **not run**.
