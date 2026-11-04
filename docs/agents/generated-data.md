# Generated data and shared facts

One fact often lives in several files. This page names each fact's source of truth, the artifacts derived from it, the command that regenerates them and the check that catches drift. Read it before changing an App, a Container, an icon or a star count. DCM specifics are in `docs/agents/dcm.md`; the gate is in `AGENTS.md`.

## Regenerate

```bash
go run ./tools/registrygen                # internal/catalog/apps_gen.go, after adding or removing a package under internal/apps
go run ./tools/sitegen                    # site/src/data/catalog.json, dcm/src/data/selfhost.json, site/src/styles/theme.css
GITHUB_TOKEN=... bun run stars            # site/src/data/stars.json and dcm/src/data/stars.json
go test ./internal/tui -update            # the Dashboard goldens, after a Catalog or layout change
go test ./e2e -run TestAcceptanceInventory -update   # e2e/testdata/inventory.json, after an App or Recipe is added, removed or renamed
```

The drift check CI runs (`ci.yml`, `test` job). `git add -N` makes a newly generated file count:

```bash
go run ./tools/registrygen -check
go run ./tools/sitegen && git add -N site/src/data dcm/src/data site/src/styles && git diff --exit-code site/src/data dcm/src/data site/src/styles
```

`internal/catalog/apps_gen.go` is written by `go run ./tools/registrygen`, which scans `internal/apps/*`: a package with exported `App` constructors registers them (`arr`), any other package with `type App` registers `App{}`. `-check` fails when the file is stale.

Nothing else in `internal/` is generated. An App declared as `internal/catalog/specs/<id>.yaml` is read by `internal/appspec` when the binary starts; edit the spec, never a Go copy.

## Drift map

| Fact                                                    | Source of truth                                                                                                                                        | Derived copy                                                                         | What catches drift                                                                                                                    |
| ------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------- |
| App id, name, description, category, image, port, Links | `internal/catalog/specs/<id>.yaml`, or `internal/apps/<name>/` (the id without hyphens, or a shorter name), registered by `internal/catalog/apps_gen.go`, or an embedded spec in `internal/catalog/specs/` | `site/src/data/catalog.json`                                                         | Loud: the sitegen drift check                                                                                                         |
| App Compose, env, notes; Recipes                        | the App, and the Recipes in `internal/catalog/`                                                                                                        | `dcm/src/data/selfhost.json`                                                         | Loud: the sitegen drift check                                                                                                         |
| Dashboard palette                                       | `tui.DarkColors` in `internal/tui`                                                                                                                     | `site/src/styles/theme.css`                                                          | Loud: the sitegen drift check                                                                                                         |
| App icon                                                | `svg/<id>.svg` on Dashboard Icons; `iconOverrides` in `tools/sitegen/icons.go` for another path, or `""` for none | the icon field of both JSON files                                                    | Loud: sitegen fails on an App with no icon and on an `icons` key that is not in the Catalog                                           |
| Standalone guide                                        | `standalone_guide` in the spec, or `StandaloneGuide()` on the App                                                                                                                         | the notes in `dcm/src/data/selfhost.json`                                            | Loud: `checkGuide` in `tools/sitegen/standalone/notes.go` fails when the guide names a variable or folder the converted Compose lacks |
| GitHub repo for an App's stars                          | the App's `Docs` URL when it is a GitHub repository, else `starRepoOverrides` in `tools/sitegen/stars.go` (`""` for none) | `site/src/data/stars.json`                                                           | Silent: an App whose `Docs` is not a GitHub URL and has no `starRepoOverrides` entry gets no star count, and the file only changes when `bun run stars` runs (weekly through `stars.yml`)      |
| GitHub repo for a Container's stars                     | `githubUrl` in `dcm/src/tools/*.ts`                                                                                                                    | `dcm/src/data/stars.json`                                                            | Silent, same as above                                                                                                                 |
| Container id against App ids                            | App ids; `ALIASES` in `dcm/src/lib/selfhost.ts`; `coveredByApp` in `dcm/src/lib/catalog.ts`                                                            | what DCM's docker compose mode shows                                                 | Silent: see Collisions                                                                                                                |
| Hidden Apps                                             | `unavailable` in `internal/catalog/catalog.go`                                                                                                         | `Apps()` leaves them out (JSON, `counts`, goldens); `AllApps()` keeps them (e2e)     | `unavailable_test.go` checks the behaviour, not the counts                                                                            |
| App and Recipe counts                                   | `Apps()`, `AllApps()` and `Recipes()` of `catalog.Default()`, counted in `tools/sitegen/counts.go`                                                     | `counts` in `site/src/data/catalog.json`                                             | Loud: the sitegen drift check                                                                                                         |
| e2e inventory                                           | `acceptanceInventory` in `e2e/acceptance.go`                                                                                                           | `e2e/testdata/inventory.json`                                                        | Loud: `TestAcceptanceInventory`, in `go test ./e2e/`                                                                                  |
| Legacy facts of a ported App                            | `internal/catalog/testdata/legacy-facts.json`                                                                                                          | the App's image repository, ports, env, mounts, category                             | Loud: `parity_test.go`; an intended difference needs a `deviations` entry with its reason                                             |
| App description                                         | `maxDescriptionLength` and `jargon` in `internal/catalog/descriptions_test.go`                                                                         | `Info().Description`                                                                 | Loud: at most 100 characters, no unexplained jargon                                                                                   |
| Service names                                           | `ownership_test.go`                                                                                                                                    | an App's extra services are named `<id>-...`, and no two Apps share a service        | Loud                                                                                                                                  |
| Traps and security notes                                | `docs/catalog/traps/<id>.md`                                                                                                                           | none                                                                                 | Silent: convention only; every App has one                                                                                            |

## App counts

No number is kept by hand. `tools/sitegen/counts.go` counts the Catalog and writes `counts` into `site/src/data/catalog.json`:

- `apps` and `recipes`: what a person is offered (`Apps()`, `Recipes()`), without the hidden ones.
- `allApps` and `allRecipes`: every App and Recipe the binary carries, the hidden ones included, which are what the e2e targets cover.

`site/` pages count from the arrays of `catalog.json`. The README badge is a shields.io endpoint badge that reads `https://self-host.fyi/badges/apps.json`, which `site/src/pages/badges/apps.json.ts` builds from `counts.apps`, so it moves when the site deploys. README, `e2e/acceptance.md` and the docs carry no App or Recipe number; keep it that way and point at `counts`. `docs/handoff.md`, `docs/runtime-matrix.*`, `docs/homarr-*` and `video/reel/` hold dated snapshots; leave them. The README's "What it replaces" image embeds a "+N more" count that `video/reel/readme` reads from `catalog.json` when it regenerates the image, so that count moves with the image, not with a new App.

Two checks still move with the Catalog and are regenerated, never edited:

- `internal/tui/testdata/onboarding-what-default-focus*.golden`, the `N more` line: `go test ./internal/tui -update`.
- `e2e/testdata/inventory.json`: `go test ./e2e -run TestAcceptanceInventory -update`. The test also fails when the file is stale.

Read the real values:

```bash
jq .counts site/src/data/catalog.json
```

## Collisions between Containers and Apps

DCM's `buildCatalog` merges `dcm/src/tools` with `selfhost.json`:

- A Container whose `id` equals an App's `id`, or is an `ALIASES` key for it, merges with the App: the App's compose, env and notes win and the Container keeps its tags and icon. This is the design, so a second Container for an App's product never adds a compose.
- A Container whose `id` starts with `<app>-` and whose services the App already provides is dropped from docker compose mode.
- A Container spelled differently from its App (`actualbudget` against `actual-budget`) and missing from `ALIASES` shows up twice, once as the Container and once as the App.

No test covers any of these. Adding a Container checks that its `id` is no App id, no `ALIASES` key and no `<app>-` prefix of an App that provides its services. Adding an App checks `dcm/src/tools` for a Container of the same product and, when the spelling differs, adds the `ALIASES` entry.

## Which check for which change

| You changed                            | Run                                                                                                     |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| an App, a Recipe, an icon, the palette | the Go gate from `AGENTS.md`, then the sitegen drift check, then `bunx turbo run build test lint check` |
| an App or Recipe added or removed      | both `-update` commands under Regenerate, then read their diffs                                         |
| a Container                            | `bun test` for the three files named in `dcm.md`, then `bunx turbo run build lint`                      |
| only the star counts                   | `bun run stars`, then the web gate                                                                      |

`validate-compose.test.ts` runs `docker compose config` and needs Docker; CI runs it as `test:compose`.
