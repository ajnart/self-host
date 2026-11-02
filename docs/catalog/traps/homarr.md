# Homarr traps

## Pitfalls

- A generic link is not evidence that a dedicated integration or widget works.
- Setup owns one shared `stats` widget and one shared `calendar` widget on Home.
  Ownership remains recorded per integration: each participating App records the
  surviving shared item ID. Removing an App removes only its integration and
  stats entries; delete the shared item only after its last integration is gone.
- Adopt legacy per-App widgets only from complete, unique generated sets with
  deterministic stats entries and empty plain options. Preserve unowned widgets;
  ambiguous ownership needs review before mutation (`{}` preserves existing items).
  Resize owned items within each responsive grid and relocate them around user
  items and containers; reruns preserve placements and larger user-selected sizes.
- Malformed or null App/widget ownership blocks Setup and Link changes before
  mutation. Review the named record through the Project secret boundary; never
  clear ownership just to bypass recovery. Missing legacy records and explicit
  empty objects retain their existing conservative migration behavior.
- New owned Beszel System Stats widgets record their managed `systemId` separately
  from widget ownership. Re-enrollment repairs only a selection matching that prior
  binding, preserving the widget ID, layouts, time period and every chart option.
  Existing correct selections still work without new evidence; ambiguous changes
  require Manual review and are never reset solely because the widget is owned.
- Repair Immich's dedicated read-only statistics key through `self-host setup
immich`, then retry Homarr. Restore matching secret/ID ownership when needed;
  never delete a key solely by name or create a duplicate Homarr integration.
- Uptime Kuma requires an explicitly selected existing status page. Follow its
  Setup's approval-gated input recovery without publishing private monitors.
  A changed upstream native widget/API contract needs a reviewed adapter update,
  not another integration or a metrics API key; this change does not update it.
- The assistant widget is optional. Its absence does not make the Notebook
  outdated. An explicit unknown-kind or kind-enum rejection may be skipped;
  authentication, other validation and server failures still fail Setup.

## Security

- Keep integration credentials in Homarr encrypted storage and protect API keys.

## From docs/homarr.md

### Traps

- Integration URLs are base URLs only; never append settings/API paths.
- Homarr's Docker URLs are not host-browser routes. Use the run-scoped host URL
  plus the table suffix when retrieving credentials with agent-browser.
- Re-snapshot after dialogs and menus; stale refs and overlays are common.
- Never pass the derived password in an agent-browser or shell argument; use
  only the bounded browser-password helper with fresh refs.
- During setup, never invoke, print, or persist authenticated agent-browser
  diagnostics such as `console`, raw `network requests`, or HAR output inside
  or outside the run; use only page-scoped approved fixed summaries or fixed
  harness summaries.
- A green integration list without visible generated apps/widgets is incomplete.
- Never fall back to post-onboarding manual placement to claim auto-generation.
- Never screenshot the one-time API key or put it in a client fragment.
- Never invoke media request/download tools as part of verification.

Sources: [Docker installation](https://homarr.dev/docs/getting-started/installation/docker/),
[integration management](https://homarr.dev/docs/management/integrations/),
[API](https://homarr.dev/docs/management/api/),
[MCP](https://homarr.dev/docs/management/mcp/), and
[Seerr integration](https://homarr.dev/docs/integrations/seerr/). Exact helper
paths come from Homarr's current
[`integration.ts`](https://github.com/homarr-labs/homarr/blob/dev/packages/definitions/src/integration.ts).

Beszel selector and option preservation follow the inspected Homarr 2.5.2
[`selection.ts`](https://github.com/homarr-labs/homarr/blob/7eb9a417a60d759adce5f7ac15ceb594aefad471/packages/widgets/src/beszel-system-stats/selection.ts)
and [widget options](https://github.com/homarr-labs/homarr/blob/7eb9a417a60d759adce5f7ac15ceb594aefad471/packages/widgets/src/beszel-system-stats/index.ts).
These source contracts do not establish native/rendered runtime acceptance.
