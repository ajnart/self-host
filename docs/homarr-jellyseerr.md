# Jellyseerr external connection: source contract and safety hold

Refs #159. Foundation only: not a working native integration or runtime acceptance claim.

## Outcome

Homarr's exact `jellyseerr` kind connects to an existing, authorized Jellyseerr endpoint through the external-connection seam. It exposes one read-only Stats widget (`pending`, `approved`, `available`, `processing`, unit `count`) from `GET /api/v1/request/count`. There is no Catalog installer, no Seerr alias, no remote lifecycle, no request actions, and no change to the endpoint's media-server choice, libraries or requests.

The shared Seerr-family validation lives in `internal/apps/homarr/seerr_family.go`, provider-neutral: a kind, a display name and the root API marker (`Jellyseerr API`). An external Overseerr leaf (#170) supplies its own marker and reuses the endpoint, credential, identity and count validation unchanged.

## Private configuration

```json
{
  "endpoint": "https://jellyseerr.example.invalid:5055",
  "credentials": [{ "kind": "apiKey", "value": "<private-api-key>" }]
}
```

Exactly one nonempty `apiKey` and no selection. The endpoint is a trusted-HTTPS origin without userinfo, path, query or fragment. Upstream's only credential is the global administrator-capable key; there is no least-privilege or empty alternative, and none is issued, retrieved or rotated by self-host.

```sh
self-host connections add jellyseerr --file /absolute/owner-only-input.json --yes
self-host connections status --json
self-host connections update <opaque-id> --file /absolute/owner-only-input.json --yes
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

Removal releases only recorded Homarr bindings; the Jellyseerr endpoint is never changed.

## Identity and data checks

Setup probes with no redirects and trusted TLS: `GET /api/v1` marker `Jellyseerr API`, `/api/v1/status` version, `/api/v1/settings/public` initialized with a known media server type (preserved, never changed), and `/api/v1/auth/me` with the key returning a positive integer account id. The key is sent only to `auth/me` and `request/count`. An uninitialized endpoint is a Manual prerequisite and is never initialized.

`request/count` must carry all eight counts (`total`, `movie`, `tv`, `pending`, `approved`, `declined`, `processing`, `available`) as non-negative integers; missing, null, string, negative or fractional values are rejected, zeros are valid. Homarr's Stats use one forced refresh, count units, `retryAt` 0 and an advancing `updatedAt`. Counts are request counts, not distinct titles; `available` and `processing` are subsets of approved.

## Safety hold

Homarr's native fetch sets no redirect override, so a CLI no-redirect probe cannot show that the key stays on the approved origin. After the identity probe succeeds, Setup stops with a Manual hold before any integration is created. Unblocking requires an audited adapter with fail-closed redirect and trusted-TLS behavior. There is no bypass; do not use a proxy or disable TLS verification.

## Not established

- No authenticated Homarr runtime API version observed; source 2.10.0 / image revision `df8d03b` only.
- Legacy Jellyseerr 2.7.3 inspected from source, not a live endpoint.
- Native, rendered and lifecycle acceptance need an assigned isolated endpoint and runtime slot.
- The broad administrator key must be explicitly accepted by its owner.
