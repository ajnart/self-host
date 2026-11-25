# Omarchy friction opportunities

Checked 21 November 2026 against current primary sources and the local self-host implementation. The released baseline remains [Omarchy 4.5.0](https://github.com/omacom/omarchy/releases/tag/v4.5.0); newer `quattro` plans and source are proposals or branch behavior, not release acceptance.

Use a repeatable discovery loop: observe a person getting stuck, check what already exists, state one question, build the smallest runnable experiment, verify a useful task, then propose the smallest change that resolved the friction. The existing twelve ideas are candidates, not twelve equally justified products.

## Phone connection diagnosis

The [community firewall plugin](https://github.com/illegalstudio/omarchy-firewall#readme) describes reaching development servers from a phone as friction and distinguishes normal UFW rules from Docker forwarding. Released [Omarchy firewall configuration](https://github.com/omacom/omarchy/blob/v4.5.0/install/config/firewall.sh) installs `ufw-docker`. A universal “Docker bypasses UFW; allow this broad subnet” hint is therefore inadequate for this platform. The local [firewall observation](../../internal/host/network.go) is a bounded host check, not an external-device acceptance test.

Build a short diagnostic over existing Status and Host observations. Distinguish This computer only, stale LAN Address, non-answering App, unavailable inspection and unknown external firewall reachability. Confirm the known saved record from a real second device before claiming phone Access works. Reject another firewall manager if clearer existing Doctor output and a guide solve the journey.

## Recovery after losing the original machine

The [Restore my Omarchy discussion](https://github.com/omacom/omarchy/discussions/7534) asks about regaining a useful environment after replacement. The [snapshot manual](https://github.com/omacom/omarchy/blob/quattro/manual/47-system-snapshots.md) excludes the Home folder; the newer [Backup plan](https://github.com/omacom/omarchy/blob/quattro/plans/backup.md) describes restic-based off-site storage and limits around active container data. This is a plan, not proof that an App-consistency integration ships.

Self-host already makes [consistent App Backups](../../internal/service/backup.go) by stopping/restarting the selected running App. Its default archive remains on the original machine, and selected-App archives contain Project secrets; media is excluded. The experiment should retain an encrypted copy outside the source PC, Restore in a fresh isolated Home folder, then verify the known saved record and source App. A VM on the original PC proves a fresh software environment, not independent hardware survival. Integrate with upstream Backup hooks if they already supply the required consistency boundary instead of maintaining parallel backup policy.

## Reuse the model owner already present

Released [AI documentation](https://github.com/omacom/omarchy/blob/v4.5.0/manual/17-ai.md) already offers native AI routes. The current official [Omarchy local AI engine](https://github.com/omacom/omarchy-local-ai) and [engine contract](https://github.com/omacom/omarchy-local-ai/blob/master/docs/engine.md) own model lifecycle, rootless Podman and resource admission in newer work. A self-host [Local AI Recipe](../../internal/catalog/recipes.go) currently installs another Ollama/Open WebUI/Beszel combination; [Open WebUI's endpoint](../../internal/apps/openwebui/openwebui.go) is not yet a general external-provider choice.

A purpose chooser should distinguish local coding from a persistent browser chat App, inspect an existing provider through public capability observations, and route to its owner when that meets the task. The experiment must not download a second engine just to produce a green result. Resource figures are observations, not fitted model or Recipe guarantees: [disk admission](../../internal/host/extra.go) currently uses generic requirements. Reject a new integration if the existing Omarchy route already meets the purpose or requires overly broad host access.

## Desktop status: an experiment-generated question

The actual scoped Waybar trial showed Spoolman responding, observed it stopped, and returned to responding after CLI Start with the saved 875 g record intact. Current [public AppStatus](../../internal/service/status.go) exposes observed state and Setup, not desired running/stopped intent. Thus a consumer cannot infer whether every stopped App was paused deliberately. The prototype reports “stopped · inspect”; the accompanying in-memory state model explores the additional intent and freshness information needed for a quieter policy. This is a question discovered by running the prototype, not a demand for another daemon.

## Built experiments and the proposal boundary

The private proposal workshop collected twelve earlier experiments. The [portable native evidence](omarchy-evidence/README.md) covers the implemented Omarchy desktop path and the [handoff](omarchy-handoff.md) records its current release boundary.

| Experiment | Actual result | Remaining boundary |
| --- | --- | --- |
| Native companion | Real Catalog selection/review/Cancel; physical launcher; ordinary-user GTK → native terminal sudo on 4.5; Restart preserves 875 g; Prometheus install and native `up = 1` query | Optional package/maintenance decision, complete new-user first-App journey, native Restore review and full accessibility acceptance |
| Desktop entries | Fresh Address resolution and actual browser Open; 4.5 first sync/Open without sudo; partial sync creates confirmed managed entries while holding deletions; edited/foreign files protected | Lifecycle hook and real Address migration; Address-change/removal failures were exercised in fixtures |
| Waybar | Actual responding → stopped → recovered observations; 875 g preserved; runnable intended-state model | No desired-state fact in current Status; full quiet mode/click acceptance and production observer deferred |
| Connection diagnostic | Actual bounded host observation plus five replayed failure cases; two corrected firewall guidance strings | No effective-rule inspection or real-phone acceptance |
| Private phone link | Actual cross-peer App/API and browser proof through a fixed-target tailnet relay; saved 875 g | Real iPhone pending; experimental transport outside managed per-App Tailscale/HTTPS Access; no reboot promise |
| Fresh recovery | Consistent encrypted Backup retained on a separate computer, fresh 4.5 guest Restore, record/comment/ownership preserved; source resumed; guest disposed | Replacement VM shared physical source hardware; independently powered recovery, media and durable retention unproven |
| Purpose/capability preflight | Actual resource snapshot and three recorded runs; reuses an existing native owner in labelled controlled cases | External Open WebUI provider wiring is not implemented; no actual engine install/model download/inference claimed |
| Appearance and community handoff | Actual light/dark/larger-text companion captures; real terminal handoff from Catalog/share, with no message sent | Global theme-following and another person's useful-task acceptance remain open |

Prioritize the existing CLI repairs and an optional entrypoint/guide, then package the working native companion if its maintenance cost is acceptable. Reuse the current Catalog, Onboarding, Setup and Access boundaries. Keep a single install/operation implementation in the CLI rather than promoting each experiment into its own manager.

The combined candidate receipt pins all five modified Go files. It built successfully; the two existing firewall/macOS checks passed, formatting and diff checks were clean. The physical deployment check verified scoped Status/Doctor and the saved 875 g record. Earlier functional trials retain their actual earlier binary hashes; the current local-Access Doctor path does not exercise the firewall hint.

All exploration used disposable selected Projects. Own temporary windows, registries, Waybar process and recovery VM were cleaned. The registered physical companion and fixed-target private phone relay intentionally remain available. No upstream issue, release, proposal or community message was published. Research was delegated read-only; the parent captured these findings.
