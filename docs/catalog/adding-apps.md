# Adding an App

Use this checklist for a compiled Catalog App, not a DCM-only Container.
Start with [CONTEXT.md](../../CONTEXT.md) and the [edit-catalog skill](../../.claude/skills/edit-catalog/SKILL.md).
For media paths and user choices, read [Media Setup](media-setup.md).

## Find the existing boundary

- Search `internal/apps/`, `internal/catalog/`, `dcm/src/tools/` and DCM's `ALIASES` before adding a product.
- Verify image, ports, mounts and authentication against primary upstream sources; retain those URLs in `docs/catalog/traps/<id>.md`.
- Implement the existing [`app.App` contract](../../internal/app/app.go): `Info`, `Inputs`, `Compose`, `Setup`, `Check`, `Links`.
- Reuse a comparable App and [`internal/helpers/`](../../internal/helpers/); add no parallel Setup framework.
- Servarr consumers reuse [`QBittorrentLink` and `ApplicationLink`](../../internal/apps/servarr/links.go); pass the actual API prefix and category rather than copying a downloader implementation.
- `tools/appgen` is a historical importer, not a safe scaffold: it references removed `internal/onboard` and overwrites App sources and registration. Do not run it to add an App.

## Compose and optional interfaces

- Declare Host settings through Compose; set `UsesMedia` when mounting media/download folders.
- Use the contract's container-port rules for internal Addresses, separately from browser Addresses.
- Consult optional interfaces in [`app.go`](../../internal/app/app.go) only as needed:
  `SecretDeclarer` for first-start secrets, `FileDeclarer` for configuration and `File.Keep` for person/App-owned files;
  `StandaloneGuide` for plain Compose instructions, `PreflightChecker` for read-only prerequisites, `DeferredStarter` for delayed services.
- A standalone guide may name only variables/folders its exported Compose actually supplies.

## Setup, verification and Links

- Use `h.Step` with an observable check before mutation and verification afterward; reruns reuse accepted credentials and existing resources.
- Keep secrets behind `h.Secret`, `h.Key`, `h.SetSecret` and secret-safe helpers; never print credentials or rotate them to hide uncertainty.
- Use `helpers.ErrManual` with specific recovery instructions for person-owned choices, unavailable credentials or ambiguous ownership.
- `Check` must verify useful App behavior, including authenticated data when authentication exists; container liveness or HTTP 200 alone is insufficient.
- Declare each Link on its consuming App, separate from local Setup. The service schedules it after both Apps finish Setup.
- Use [`app.Link` and its lifecycle helpers](../../internal/app/link.go), recording validated returned resource IDs before treating remote objects as owned.
- Endpoint/name equality identifies a possible collision, not ownership. Preserve matching user resources; uncertain legacy ownership needs Manual recovery.
- Before updating/removing a recorded resource, verify its identity and expected target; unlink removes only recorded owned resources.
- `NewOwnedLink` does not supply identity persistence: its `Owned.Find` must enforce that boundary. Do not copy endpoint-matching ownership as a new pattern.

## Homarr branch

- Read [the native integration workflow](../agents/homarr-integrations.md) and [Link contract](../homarr-integration-contract.md) before wiring Homarr.
- The peer's Setup owns credentials; Homarr owns the consuming Link, connection and widgets.
- Use `internal/apps/homarr/target_<app>.go` and the existing `target` seam; coordinate common registry changes with its owner.
- Resolve native kinds, credential alternatives, selectors and useful data from the resolved upstream source; reuse existing verification/Stats helpers.
- Registration is not acceptance. Keep compatibility, credential-safety, fresh native/Stats data, rendered widgets, both installation orders and ownership-preserving removal/reinstall gates separate.
- Runtime proof needs explicit disposable-Project approval and the containment limits in the workflow; source-only work cannot claim those gates passed.

## Registration and delivery

- Follow edit-catalog for `internal/catalog/batch_*.go`, Recipes, descriptions and DCM collisions; leave generated registration alone.
- Declare icon/repository metadata in `tools/sitegen/icons.go` and `tools/sitegen/stars.go`.
- Follow [generated-data ownership and counts](../agents/generated-data.md) for site/DCM generation, aliases, hand-kept counts and drift checks; never hand-edit generated output.
- Use the applicable [repository gate](../../AGENTS.md); report focused verification separately from deferred runtime acceptance.
