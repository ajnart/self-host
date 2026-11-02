# Uptime Kuma traps

## Pitfalls

- Its data path requires POSIX locks and is not supported on NFS.

## Security

- Protect the admin UI and notification credentials.
- Creating an API key globally changes /metrics authentication from administrator Basic auth to API-key Basic auth. These keys do not authenticate the status-page API or the administrator's Socket.IO API. Setup creates and rotates no keys.

## Homarr Link

- The inspected native contracts are Homarr v2.10.1 (`0f0987d2ed013c93ce30c1c00786983d29f4b956`) and the older v2.4.1 dashboard contract. The compiled Catalog image remains `louislam/uptime-kuma:latest`; none of these source tags establishes the resolved runtime image, native or rendered compatibility.
- The descriptor is `uptimeKumaTarget()`, kind `uptimeKuma`, with internal Address `http://uptime-kuma:3001`, separate from the browser Address. Registry assembly belongs to the reference lane.
- Review an actual existing page and the monitors it already shares without a login. Its explicit `homarr_status_page` input belongs to Uptime Kuma Setup; no default slug is assumed or saved automatically by Homarr. Any recovery that changes Project configuration, authentication, mounts, ports or restarts the App requires a separately reviewed and approved plan that preserves the canonical root Compose file and private credentials.
- Selection grants read-only use, not ownership: the Link never creates, changes, publishes, adopts or deletes a Kuma page, group, monitor, account or key. It never changes Access. Creating a new public monitor group needs separate explicit approval; do not publish private monitors to satisfy Homarr.
- The selected credential alternative is only `slug`. Homarr declares empty, slug, API-key and slug-plus-key alternatives, but its tagged native adapter reads `/api/status-page/<slug>` and `/api/status-page/heartbeat/<slug>` without using the key. An API key is not a privacy boundary for those endpoints. Kuma's `published` flag is not relied on as access control.
- The first administrator is created and the saved Admin login verified over Socket.IO (`setup` and `login` events, driven by the shared Engine.IO long-polling client in `internal/helpers/socketio.go`). An existing administrator whose login does not match fails Setup with a hint; it is never replaced. API keys and page management also use Socket.IO and remain out of scope.
- Admission runs read-only before Link mutation. After authenticated compatibility, one deadline permits the modern empty-integration `widget.uptimeKuma.getStatusPages` probe and at most one legacy `getDashboard` probe. Only the exact numeric tRPC missing-procedure envelope permits that fallback; authentication, transport, malformed, unrelated 404 and native data failures never do. No fixed release allowlist is added.
- An empty probe establishes route/input availability, not a populated response schema. A first install, missing explicit widget ownership, credential fingerprint drift or unavailable recorded integration therefore stops as an actionable Manual step before writes. There is no automatic first-install claim, adoption of a user integration, credential replacement or duplicate-integration workaround. An existing recorded owned integration must verify its exact selected page/monitor contract read-only before changes are admitted.
- Modern verification sends the selected slug and owned integration ID to `getStatusPages`, checks the returned integration Address and selected `/status/<slug>` page path, and reads `statusPage.groups[].monitors[]`. It validates exact monitor identity/multiplicity, consistent duplicates, required nullable fields, heartbeat order and within-response status metadata. Explicitly empty pages are valid but give no monitor-health or rendered proof.
- Modern status labels are up, down, pending, maintenance, stale and unknown, not paused. Heartbeat codes 0/1/2/3 map to down/up/pending/maintenance; other codes map to unknown. Missing observations are unknown, not healthy. Stale is based on heartbeat age; the public page does not expose administrator pause state. A stale monitor can still have a numeric 24-hour uptime. `uptime24h` is already a percentage, bounded to 0–100, or null when unavailable; zero is valid. The old dashboard parser retains its distinct up/down/paused categories, flat monitors, nullable `uptimePercent24h`, aggregate counts and numeric average semantics only when its exact capability is present.
- Modern owned widgets use `statusPageSlug`, summary/group/filter/sort options, heartbeat bars, uptime, response-time and certificate switches from the current definition; legacy widgets retain their six sourced switches. Migration changes only explicitly managed option keys on recorded owned, exclusively bound widget IDs, preserving IDs and other options. User widgets are not changed. An edited cross-source native widget stays Manual instead of overwriting another binding's selection.
- Shared Stats retains `totalMonitors`, `upCount`, `downCount`, `averageUptimePercent`. The same central catalog, prior snapshot and one forced genuinely advancing refresh feed count/percent unit and semantic checks. Only the admitted modern family permits an explicit null average, including a real empty page or no active numeric uptime; an absent key still fails, and legacy Kuma and every non-opted-in peer remain non-null strict. Modern empty totals require null average, not a fabricated zero. Counts remain finite nonnegative integers and cannot exceed total. Native and Stats are independently cached observations and are not compared for equality; no leaf refresh loop is added.
- Focused HTTP contracts cover both families, exact fallback admission, pre-mutation Manual holds, selected-page privacy, native identity/status/uptime, nullable/missing/zero distinctions, advancing single-sample Stats, owned option migration, stable rerun IDs and user/cross-kind preservation. Removal does not run peer/native capability or Stats probes and retains user-referenced integrations. These are source/fake observations only: resolved images, native/rendered values, both install orders and actual lifecycle acceptance remain separately approval-gated. No Docker, browser, notification or monitoring runtime was used.

## Registration, 13 October 2026

The registry owner registered `uptimeKumaTarget()` at
`10e8bedf3b3718aaaede3bfcbbad39369d87f1f6`. This is source registration only;
live native data, rendered widgets and installation/restart/removal/reinstallation
lifecycle acceptance remain unproved. Earlier peer-lane source evidence does not
establish runtime acceptance.

## Sources

- [Current native router](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/api/src/router/widgets/uptime-kuma.ts)
- [Current adapter and uptime semantics](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/integrations/src/uptime-kuma/uptime-kuma-integration.ts)
- [Current summary and unique-monitor semantics](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/integrations/src/uptime-kuma/uptime-kuma-summary.ts)
- [Current widget options](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/widgets/src/uptime-kuma/index.ts)
- [Current Stats mapping](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/integrations/src/stats/existing.ts#L315-L339)
- [Homarr v2.4.1 adapter](https://github.com/homarr-labs/homarr/blob/v2.4.1/packages/integrations/src/uptime-kuma/uptime-kuma-integration.ts)
- [Homarr credentials](https://github.com/homarr-labs/homarr/blob/v2.4.1/packages/definitions/src/integration.ts)
- [Homarr widget options](https://github.com/homarr-labs/homarr/blob/v2.4.1/packages/widgets/src/uptime-kuma/index.ts)
- [Homarr native Stats fields](https://github.com/homarr-labs/homarr/blob/v2.4.1/packages/integrations/src/stats/existing.ts)
- [Kuma 2.5.5 authentication](https://github.com/louislam/uptime-kuma/blob/2.5.5/server/auth.js)
- [Kuma status-page reads](https://github.com/louislam/uptime-kuma/blob/2.5.5/server/routers/status-page-router.js)
- [Kuma administrator setup](https://github.com/louislam/uptime-kuma/blob/2.5.5/server/server.js)
- [Kuma page management](https://github.com/louislam/uptime-kuma/blob/2.5.5/server/socket-handlers/status-page-socket-handler.js)
- [Kuma key management](https://github.com/louislam/uptime-kuma/blob/2.5.5/server/socket-handlers/api-key-socket-handler.js)

SDK migration source checked read-only at the resolved OCI revision
`3d4150e59f600876b7e308f857b2f21572b75348`:
[Stats mapping](https://github.com/homarr-labs/homarr/blob/3d4150e59f600876b7e308f857b2f21572b75348/packages/integrations/src/stats/existing.ts),
[Uptime Kuma adapter](https://github.com/homarr-labs/homarr/blob/3d4150e59f600876b7e308f857b2f21572b75348/packages/integrations/src/uptime-kuma/uptime-kuma-integration.ts).
