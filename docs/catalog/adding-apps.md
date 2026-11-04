# Adding an App

Use this checklist for a compiled Catalog App, not a DCM-only Container.
Start with [CONTEXT.md](../../CONTEXT.md) and the [edit-catalog skill](../../.claude/skills/edit-catalog/SKILL.md).
For media paths and user choices, read [Media Setup](media-setup.md).

## Find the existing boundary

- Search `internal/apps/`, `internal/catalog/`, `dcm/src/tools/` and DCM's `ALIASES` before adding a product.
- Verify image, ports, mounts and authentication against primary upstream sources; retain those URLs in `docs/catalog/traps/<id>.md`.
- Declare the App as a [YAML spec](#declarative-spec) when its Setup only proves the web page answers; otherwise implement the existing [`app.App` contract](../../internal/app/app.go): `Info`, `Inputs`, `Compose`, `Setup`, `Check`, `Links`.
- Reuse a comparable App and [`internal/helpers/`](../../internal/helpers/); add no parallel Setup framework.
- Servarr consumers reuse [`DownloadClientLinks`](../../internal/apps/servarr/downloadclients.go) for supported downloaders and [`ApplicationLink`](../../internal/apps/servarr/links.go) for consuming application Links. Pass the actual API prefix, category field and category rather than copying a downloader implementation; keep target-specific authentication and read-only prerequisites in the target owner.

## Declarative spec

An App whose Setup is the `no-account` probe is one file, `internal/catalog/specs/<id>.yaml`, plus its icon. [`internal/appspec`](../../internal/appspec/appspec.go) embeds and loads it, so there is no Go and no registration. Copy `specs/ollama.yaml`; `specs/loki.yaml` shows `files`, `specs/stirling-pdf.yaml` a `standalone_guide`.

- Required: `id name category description image docs port compose setup`. The file is named `<id>.yaml` and starts with `# Traps: docs/catalog/traps/<id>.md`.
- Optional: `container_port` (defaults to `port`), `service` (defaults to `id`), the other `app.Info` keys (`path no_web_ui home_address_only websockets long_timeouts uses_media container_port_is_port`), `inputs`, `secrets`, `files` (`path content mode keep`) and `standalone_guide`. Keys are the lowercase snake_case of the Go field.
- `setup` is a [`setupspec`](../../internal/setupspec/setupspec.go) block: `kind: no-account` with `probe: {path, contains}`, or `kind: first-admin` with its `exists`, `create`, `login`, `check_error` and step text.
- `compose` and each file's `content` are `text/template`: `{{.Image}}`, `{{.Port}}` (the host port), `{{.ContainerPort}}` and `{{.Inputs.KEY}}`; files also take `{{secret "name"}}`. Host settings stay literal (`${BIND_IP}`, `${PUID}`) and secrets are `${<ID>_<NAME>}`.
- Parsing is strict. An unknown key, a missing field, a file name that differs from the id, a template that does not render or a Compose without the `service` fails at startup, so any test that calls `catalog.Default()` fails first.

An App that needs more than that (extra Setup steps, a first administrator, Links, deferred services) stays Go.

## Setup kinds

A Setup that is plain data is a `setupspec.Spec`: `no-account` proves the App serves, and `first-admin` runs HTTP calls for `exists`, `create` and `login`, with `create_wait` for an App that seeds its own administrator and `create_error` for one that cannot. Both engines live in `internal/setupspec`; a Go App holds the same fields an embedded spec declares, so a new kind is one `kind_<name>.go` file and one entry in its `kinds` map.

Apps still hand-write their Setup because a kind does not express them: cookie or CSRF sessions, container `exec` CLI calls, an API-key or config-file flow, a multi-step wizard, or error text pinned by a test. Find the remaining `firstadmin.FirstAdmin` closures with `grep -l 'firstadmin.FirstAdmin' internal/apps/*/*.go`; convert one when a kind already covers it.

## Compose and optional interfaces

- `App.Compose` supplies a definition for the Project boundary to merge into the one editable root `compose.yml`. Installed definitions, including manual edits and ownership metadata, live there; per-App Compose files are legacy recovery snapshots, not active truth. Use `internal/project` and `internal/compose` rather than writing YAML or running Compose around them ([ADR 0014](../adr/0014-one-editable-compose-file.md)).
- Keep App-local relative data paths and distinct App-owned service names. The Project boundary rebases those paths into the root file while preserving storage identity, unrelated services and comments.
- Declare Host settings through Compose; set `UsesMedia` when mounting media/download folders.
- Reuse `internal/apps/mediastorage` for cooperating media paths and supported identities. New Projects default to shared storage; an absent layout preserves legacy paths. `UsesMedia` alone does not establish support for separate media App users.
- Use the contract's container-port rules for internal Addresses, separately from browser Addresses.
- Consult optional interfaces in [`app.go`](../../internal/app/app.go) only as needed:
  `SecretDeclarer` for first-start secrets, `FileDeclarer` for configuration and `File.Keep` for person/App-owned files;
  `StandaloneGuide` for plain Compose instructions, `PreflightChecker` for read-only prerequisites, `DeferredStarter` for delayed services.
- A standalone guide may name only variables/folders its exported Compose actually supplies.
- The Project's private `.env` supplies Compose interpolation; legacy `secrets.env` is a compatibility input. Keep public settings allowlisted and grant each App only its required secrets, never the whole Project environment.

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

- Follow edit-catalog for Recipes, descriptions and DCM collisions; a spec needs no registration, a Go App runs `go run ./tools/registrygen`.
- Icon and repository metadata default from the id and `Docs`; override them in `tools/sitegen/icons.go` and `tools/sitegen/stars.go` only when they differ.
- Follow [generated-data ownership and counts](../agents/generated-data.md) for site/DCM generation, aliases, hand-kept counts and drift checks; never hand-edit generated output.
- Use the applicable [repository gate](../../AGENTS.md); report focused verification separately from deferred runtime acceptance.
