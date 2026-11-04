# Homarr OpenMediaVault external connection: proof

Scope: an existing, explicitly configured external OpenMediaVault endpoint through the external-connection seam. Exact Homarr kind `openmediavault`, credentials `username` + `password`. No installer, Catalog App, Compose service or NAS change.

## Source and fake-backed evidence

- `internal/apps/homarr/target_openmediavault.go` validates a trusted-HTTPS origin, exactly one nonempty username and password, and no selection.
- Surfaces: `systemResources` (`cpu`, `memory`) and `stats` keys `cpu` (percent), `memoryUsed` (bytes), `memoryAvailable` (bytes), `uptime` (seconds).
- Native validation reads `widget.healthMonitoring.getSystemHealthStatus` for the owned integration only and rejects wrong or duplicate IDs, null `healthInfo`, error, stale data and invalid metrics. A genuine zero is accepted; load average is not a percentage; filesystem text and SMART health are not used.
- Stats validation requires exact units, `retryAt` 0 and a valid, advancing clock.
- `internal/apps/homarr/target_openmediavault_test.go` covers the contract, the held transport, local disconnect without remote access, and the semantics above. Shared endpoint validation is `externalHTTPSOrigin`, also used by Proxmox.

## Manual and held

- Native transport hold: the inspected Homarr adapter (`packages/integrations/src/openmediavault/openmediavault-integration.ts`) uses an ordinary trusted-certificate fetch without explicit manual redirects, so redirect confinement is unproved. Setup stops with a Manual hint before credential forwarding or integration creation. Unblocking decision: an audited adapter with fail-closed redirect and trusted-TLS behavior.
- Monitoring needs the OpenMediaVault administrator role; there is no read-only scope. MFA and challenges are unsupported. The optional CPU-temperature plugin is never installed.
- Upstream: published Homarr OCI image `df8d03b…` reports 2.10.0; source tag v2.10.1 `0f0987d…`; OpenMediaVault source `68d5fefd…` (8.6.1-1).

## Not run

Native, rendered and lifecycle acceptance (connect, loss, recovery, endpoint change, disconnect) and a live observed acceptance value: no controlled endpoint is available. No Docker, browser or real endpoint was used.
