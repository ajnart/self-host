# Synology DiskStation external connection: source contract and safety hold

## Outcome

The exact `synology` external provider is implemented in source. **Native Setup is
Manual before every provider request, credential forwarding or integration
creation.** The audited Homarr Synology client follows redirects and is not
confined by a guarded fetch, so a no-redirect CLI probe cannot establish native
confinement. The hold has no input, environment or version bypass. This is not a
working native integration or runtime acceptance claim; no DSM was contacted.

Synology is not a Catalog App, Compose service, image or installer target. Only
explicit local configuration and recorded owned Homarr bindings are managed.

## Private configuration

```json
{
  "endpoint": "https://nas.example.invalid:5001",
  "credentials": [
    { "kind": "username", "value": "<dedicated-dsm-user>" },
    { "kind": "password", "value": "<private-password>" }
  ]
}
```

The only credential alternative is `username` plus `password`. The endpoint is a
trusted-HTTPS DSM origin with no userinfo, path, query or fragment. No selection
is accepted.

```sh
self-host connections add synology --file /absolute/owner-only-input.json --yes
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

Kind and endpoint are immutable; remove and add a new connection to change either.
Removal never contacts or changes the NAS.

## Manual prerequisites

- An authorized existing DSM HTTPS endpoint with a certificate trusted by Homarr.
- A dedicated account DSM and NAS policy permit to read system utilization.
  Homarr's reads may need Administrators-group access, which is not read-only
  isolation; do not weaken MFA, Adaptive MFA or Auto Block.
- Review first-login terms and application restrictions without changing
  packages, shares, disks or accounts.
- Upstream caches DSM API discovery by integration ID only, and its optional
  upgrade check may reach vendor networks; both stay unresolved holds.

## Surface

When the hold is lifted: System Resources (CPU, memory) and Health widgets plus
exact Stats `cpu` (percent), `memoryUsed` and `memoryAvailable` (bytes) and
`uptime` (seconds). Missing, null, stale or non-advancing values are rejected,
never treated as zero. Disks and SMART widgets are deferred.

## Recovery hints

- Transport: preserve the connection and credentials; retry `self-host setup homarr`
  only after an audited adapter confines redirects and trusted TLS.
- Data: check TLS trust, MFA-free sign-in for the dedicated account and its policy
  permission, then retry.
- Stats: retry after the hold is resolved; do not accept cached or no-op data.
