# Omarchy handoff

Continue the optional self-host integration from `feat/omarchy-integration` in `ajnart/self-host`. The branch preserves the implementation and targeted experimental fixes from the Omarchy prototype. It is based on `c8371baa`, the self-host v1.4.0 preparation commit. The finished native test used lab package revision 13.

## Start here

1. Read [AGENTS.md](../../AGENTS.md), [CONTEXT.md](../../CONTEXT.md), and the [package guide](../../packaging/omarchy/README.md). Keep one CLI and Dashboard in self-host. Let Omarchy own its existing navigation, terminal, authorization prompt, bar and default agent.
2. Review the [portable evidence](omarchy-evidence/README.md), [two-row menu proposal](omarchy-evidence/omarchy-v4.5.0-nested-menu.patch) and [review findings](omarchy-evidence/reviewed-actions-review.md). The [historical platform research](omarchy-support.md) and [friction experiments](omarchy-friction-opportunities.md) record earlier builds and alternative paths.
3. Start the next pass at the release boundary. Adapt the split package recipe to a checksum-pinned release asset accepted by the [Omarchy package repository](https://github.com/omacom/omarchy-pkgs#contribute-a-package). Verify installation and update on x86_64 and aarch64, including package ownership of updates and removal that keeps Project data. Keep the Omarchy OS patch at two entries. The branch push is the current delivery milestone. Upstream submission and publication remain separate decisions.

## Where to work

| Piece | Source |
| --- | --- |
| Pacman split package, launcher, UI adapter and authorization helper | [`packaging/omarchy/`](../../packaging/omarchy/) |
| Omarchy detection, Docker endpoint check and desktop-user process launch | [`internal/host/omarchy.go`](../../internal/host/omarchy.go), [`internal/host/docker_endpoint.go`](../../internal/host/docker_endpoint.go), [`internal/system/desktop.go`](../../internal/system/desktop.go) |
| Desktop Status, guarded Actions and shortcut cleanup | [`internal/cli/desktop.go`](../../internal/cli/desktop.go), [`internal/service/desktop_actions.go`](../../internal/service/desktop_actions.go), [`internal/service/desktop.go`](../../internal/service/desktop.go) |
| Onboarding and the optional Dashboard setting | [`internal/tui/onboarding.go`](../../internal/tui/onboarding.go), [`internal/tui/onboarding_view.go`](../../internal/tui/onboarding_view.go), [`internal/tui/settings.go`](../../internal/tui/settings.go) |
| Earlier recovery and firewall fixes | [`internal/service/backup.go`](../../internal/service/backup.go), [`internal/backup/backup.go`](../../internal/backup/backup.go), [`internal/host/network.go`](../../internal/host/network.go) |

## Verification and open decisions

- Preserve the first visit through Onboarding, the config-backed picker on return, the default-off bar choice, and generated desktop entry cleanup after confirmed Remove. Preserve edited entries.
- Keep the selected App and effect in the Action review. Attach only from the active desktop seat when testing the native prompt. Confirm both cancellations leave the App unchanged, test a stale or invalid Action, and verify the positive Actions using both container evidence and a useful App operation. Use a disposable peer container to catch Project-wide side effects. [Approval contract](../adr/0005-agents-load-the-skill-on-their-own.md)
- The x86_64 tests covered revision 11 Onboarding and revision 13 reviewed Actions, including Prometheus queries and the optional agent clipboard handoff. A signed lab repository and fixture guest were used. The disposable overlays and credentials were discarded. Create a fresh isolated guest and your own lab trust material for reproduction.
- A signed upstream package, aarch64 native verification, actual phone App Access, and an authenticated live agent conversation remain open. The earlier Docker reboot experiment also left the OS boot policy as an explicit product choice.

Use focused tests and the normal entrypoints for the seam under review. The test families are `TestDesktop*`, `TestOnboardingOmarchyShortcutIsOptIn`, `TestRemoveCleansShortcutOnlyForCommittedApp`, and `TestStatusJSONKeepsLegacyStrings`. The package guide describes the native build.

## Suggested skills

- **find-docs** for the current Omarchy menu, desktop authorization and package repository contracts.
- **code-review** for the release and upstream diffs after the packaging work lands.
- **pr** when preparing a maintainer-facing proposal, if submission is requested.
