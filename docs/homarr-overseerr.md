# Overseerr external connection: source contract and safety hold

Refs #170. Source and fake-backed tests only: not a native integration or runtime acceptance claim.

## Outcome

Homarr's exact `overseerr` kind connects to an existing, authorized Overseerr endpoint through the external-connection seam. It exposes one read-only Stats widget (`pending`, `approved`, `available`, `processing`, unit `count`) from `GET /api/v1/request/count`. There is no Catalog installer, no Seerr or Jellyseerr alias, no remote lifecycle and no request actions.

It reuses the provider-neutral Seerr-family validation in `internal/apps/homarr/seerr_family.go` with its own kind, name and root API marker (`Overseerr API`, verified in upstream v1.35.0 `server/routes/index.ts`). Overseerr additionally requires `processing + available == approved`, in both the peer response and Homarr's Stats; Jellyseerr does not.

## Private configuration

```json
{
  "endpoint": "https://overseerr.example.invalid:5055",
  "credentials": [{ "kind": "apiKey", "value": "<private-api-key>" }]
}
```

Exactly one nonempty `apiKey` and no selection. The key is the instance's single administrator key: upstream resolves it to user ID 1 and offers no scoped alternative. It is never read-only. self-host does not issue, retrieve or rotate it; supply it only through the owner-only private input and accept its privileges explicitly.

```sh
self-host connections add overseerr --file /absolute/owner-only-input.json --yes
self-host connections status --json
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

Removal releases only recorded Homarr bindings; Overseerr is never changed. The endpoint is immutable for a connection: a different endpoint is a new connection.

## Safety hold

Setup probes identity with no redirects and trusted TLS (`/api/v1` marker, `/status` version, `/settings/public` initialized, `/auth/me` numeric id), then stops with a Manual hold before any integration is created, because Homarr's native fetch sets no redirect override. Unblocking requires an audited adapter with fail-closed redirect and trusted-TLS behavior. No proxy, no disabled TLS verification.

## Not established

- No authenticated Homarr runtime version observed; source 2.10.1 only.
- Overseerr v1.35.0 is archived upstream; migration to Seerr is not performed here.
- Native, rendered and lifecycle acceptance need an approved isolated endpoint and runtime slot.
- `dcm/src/lib/selfhost.ts` maps `overseerr` to the installed Seerr App; that false-product alias is for the DCM custodian.
