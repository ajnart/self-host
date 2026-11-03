# Readarr external connection: source contract

## Outcome

An explicitly chosen, existing Readarr endpoint can be connected to Homarr through
the external-connection seam. The surface is the exact `readarr` kind, the `apiKey`
credential only, a read-only Calendar widget and Stats `books`, `monitored`,
`upcoming`. This is source plus fake-backed verification; it is not runtime,
native-rendering or live-endpoint acceptance.

Readarr is not a Catalog App, installer, Compose service, image default, downloader
or lifecycle target, and Bindery is a separate Homarr kind, never a substitute.
Readarr is retired (upstream archived 2025-06-27) and its LinuxServer image is
deprecated: self-host never installs, replaces, upgrades or changes it. Existing
Readarr paths, profiles and indexers are untouched; the peer only receives GET
requests.

## Private configuration and recovery

```json
{
  "endpoint": "http://readarr.example.invalid:8787",
  "credentials": [{ "kind": "apiKey", "value": "<private-readarr-api-key>" }]
}
```

```sh
self-host connections add readarr --file /absolute/owner-only-input.json --yes
self-host connections status --json
self-host connections update <opaque-id> --file /absolute/owner-only-input.json --yes
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

The key is in Readarr Settings > General > Security. Readarr has no scoped
read-only key; the key grants full API access, so keep it in the owner-only input.
Setup never issues, rotates or retrieves it. A lost, rejected or rotated key is
Manual: update the private input, then rerun `self-host setup homarr`. Kind and
endpoint are immutable; disconnect and add a new connection to change either.
Removal uses only recorded Homarr IDs and leaves Readarr untouched.

## Verification

- Identity: authenticated `GET /api/v1/system/status` requires `appName` `Readarr`
  and a nonempty `version`.
- Library: `GET /api/v1/book` must be an array whose items carry `monitored`;
  missing or null is invalid, never zero.
- Calendar: Homarr's `widget.calendar.findAllEvents` must return exactly one
  `readarr` result with titled, dated events; an error result is not empty.
- Stats: `books`, `monitored`, `upcoming` are finite nonnegative integral counts,
  and `monitored <= books`. `upcoming` may exceed `monitored` because it includes
  unmonitored books.
- Releases widget omitted: it tracks software versions, not books.

## Evidence categories

- Source and fake: implemented and tested in `internal/apps/homarr`.
- Not run: live Readarr peer version, trusted TLS, real library/calendar data,
  Homarr native rendering and live lifecycle (connect, loss, recovery, endpoint
  change, disconnect).
- Upstream identities are recorded in `reports/172-research.md`: Homarr
  `v2.10.1`, image revision `df8d03b13452bb50c55a424187e1fa188ab510d2`, Readarr
  source `0b79d3000d4e5f8425f499970b0190e2c421fceb`.
