# self-host testkit

Real, isolated App fixtures for integration tests. Keep your existing API adapter;
replace its mock Address and API key with a real App configured by self-host's Setup.

This guide describes **1.3.2**, which keeps the **1.3.0** testkit APIs compatible,
including `withApp`, ordered Pairs and the automatic-cleanup JavaScript Action.
Version 1.2.1 has none of them.

```javascript
import { withApp } from "@ajnart/self-host-testkit";

await withApp("sonarr", async (sonarr) => {
  const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
    headers: { "X-Api-Key": await sonarr.getApiKey() },
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error("Sonarr rejected the request");
  const status = await response.json();
  if (status.appName !== "Sonarr") throw new Error("Unexpected App identity");
});
```

`withApp` starts the App, awaits your callback and always attempts cleanup,
including when the callback throws. No test-runner plugin or runtime npm dependency.
Each fixture gets a temporary Project, loopback-only Address and private credentials;
it never uses your normal self-host Project. See the [Node guide](node/README.md)
for mock replacement, timeouts, recovery and CI.

## Existing test commands (unreleased)

This source checkout also provides `self-host-testkit-run`: start several Apps
in parallel, supply their endpoints to an existing test command, clean up, and
write a sanitized report. No new test DSL is required. See the [runner guide](node/README.md#run-your-existing-tests-unreleased)
for configuration and the optional Action `apps` / `run` inputs. This is not yet
available in the published 1.3.2 package or the v1.3.0 Action pin below. Source helpers use shared startup admission and cross-process
port claims so independent image pulls, container creation and Setup can overlap.

## CI in another repository

Use the published SDK and released helper; **no Go or self-host checkout is
needed**. This example runs one Sonarr fixture on a disposable GitHub-hosted
Ubuntu runner, with tests running directly on the host rather than in a job
container.

1. In your Node.js 22+ repository, add the exact SDK version and commit both
   `package.json` and `package-lock.json`:

   ```bash
   npm install --save-dev --save-exact @ajnart/self-host-testkit@1.3.0
   ```

2. Save this as `tests/sonarr.integration.test.mjs`:

   ```javascript
   import assert from "node:assert/strict";
   import { test } from "node:test";
   import { withApp } from "@ajnart/self-host-testkit";

   test("Sonarr authenticates", { timeout: 900_000 }, async () => {
     await withApp("sonarr", async (sonarr) => {
       const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
         headers: { "X-Api-Key": await sonarr.getApiKey() },
         signal: AbortSignal.timeout(10_000),
       });
       assert.equal(response.ok, true);
       const status = await response.json();
       assert.equal(status.appName, "Sonarr");
       assert.equal(typeof status.version, "string");
     });
   });
   ```

3. Save this as `.github/workflows/integration.yml`:

   ```yaml
   name: integration

   on:
     pull_request:
     workflow_dispatch:

   permissions:
     contents: read

   jobs:
     sonarr:
       runs-on: ubuntu-latest
       timeout-minutes: 20
       steps:
         - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
           with:
             persist-credentials: false
         - uses: actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38
           with:
             node-version: "24"
             package-manager-cache: false
         - run: npm ci
         - uses: ajnart/self-host/testkit@eb8569ec50a8c62d1406d55b19f6bfee218a4240
         - run: node --test --test-concurrency=1 tests/sonarr.integration.test.mjs
   ```

The testkit Action pin above is from **v1.3.0**. It resolves your installed SDK,
downloads its exact matching checksummed helper, exports the helper/manifest
environment and registers automatic post cleanup. Do not add `source: true` or
a Go setup step. The Action does not install your dependencies or start Apps.
No extra secret or write permission is needed for this public-package example;
fork pull requests may still need maintainer approval under GitHub's runner policy.

Keep your own adapter assertions inside `withApp` once this smoke test works;
testkit provides Setup, not your test datasets or complete integration acceptance.
For monorepos and Bun/pnpm, see the [Node CI guide](node/README.md#github-actions).
For a local run after installation, first run
`npm exec -- self-host-testkit-setup`, then the same `node --test` command; this
starts a real App and needs local Docker.

### CI limits and diagnostics

- Start with **one fixture at a time**. Each container is capped at 1 GiB, one
  CPU and 512 PIDs; a fixture may use five containers, including dependencies.
  Budget up to 5 GiB and five CPUs per simultaneous fixture plus runner overhead.
- Cold pulls and Setup can take minutes. `withApp` defaults to a **12-minute
  lifetime**, not merely a startup timeout; Node permits at most 14 minutes.
  Keep the longer test/job timeouts above so cleanup has time to finish.
- `withApp` attempts cleanup on success and callback failure. Action post cleanup
  is a second attempt, not protection against runner loss or an unavailable
  Docker daemon. Prefer disposable runners; no Backup/Restore cache is provided.
- On failure, check the sanitized `TestkitError.code` and `reason`, Docker/Compose
  availability, image access and resource budget. Do not upload manifests,
  Project files, credentials or raw response bodies. See [private recovery](node/README.md#errors-and-private-recovery).

### Fixture validation status

Catalog membership does **not** mean runtime-verified fixture support. The
[consumer smoke workflow](../.github/workflows/testkit-smoke.yml) uses the
published 1.3.0 SDK/helper on Linux x64, checks authenticated Sonarr identity and
rejected credentials, and verifies owned containers/networks are removed after
successful and deliberately failing callbacks. Check its
[actual workflow runs](https://github.com/ajnart/self-host/actions/workflows/testkit-smoke.yml)
for passing evidence; a configured workflow or skipped run is not a pass.

| App IDs                                                              | Validation boundary                                                                                                                                                                                                                                               |
| -------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `sonarr`                                                             | Covered by the consumer smoke workflow above; not a promise for every image version or platform.                                                                                                                                                                  |
| `radarr`, `prowlarr`, `bazarr`, `sabnzbd`, `qbittorrent`, `jellyfin` | Consumer candidates in [Homarr's profiles](https://github.com/homarr-labs/homarr/blob/5ea802234adff615cc78a2041522ca3f09ac4917/packages/integrations/weekly/profiles.ts); not covered by this smoke workflow. Profile definitions alone are not runtime evidence. |
| `homarr` + `sonarr` ordered Pair                                     | Source example exists; Pair/native/rendered acceptance remains separate from single-App smoke coverage.                                                                                                                                                           |
| Other Catalog Apps                                                   | Unverified as testkit fixtures; check their Setup and the restrictions below before adopting.                                                                                                                                                                     |
| Products outside the Catalog                                         | Not accepted by `withApp`; an arbitrary Docker image is not an App definition.                                                                                                                                                                                    |

Apps needing Manual steps, external accounts, hardware, host networking, socket
mounts or named volumes are unsupported fixtures. Containerized test runners,
Docker-in-Docker, remote Docker and native Windows are also unsupported; see the
[environment matrix](#headless-and-container-requirements). The `image` option
only substitutes a selected Catalog App's image; it cannot remove these limits.

## Published release

Version **1.3.2** must exist on
[npm](https://www.npmjs.com/package/@ajnart/self-host-testkit/v/1.3.2) and
[GitHub](https://github.com/ajnart/self-host/releases/tag/v1.3.2) before using
these local commands. The CI quickstart above remains pinned to the separately
verified 1.3.0 SDK/helper combination.

```bash
npm install --save-dev --save-exact @ajnart/self-host-testkit@1.3.2
npm exec -- self-host-testkit-setup
```

Explicit setup verifies and caches the matching released helper; **no Go required**.
Package installation and import never download a helper or start Docker.
The 1.2.1 composite Action requires its documented separate cleanup step; for
1.2.1, follow its [immutable Node guide](https://github.com/ajnart/self-host/blob/5bd44f7f4cd3c350d6cf449e958eae6c0df28a69/testkit/node/README.md).

## Try this source checkout

You need Node.js 22+, Bun 1.4.3+, a Go launcher able to obtain Go 1.27.3, and the
Docker requirements below. Build the SDK and helper from the **same checkout**.
From the repository root of a checkout:

```bash
bun install --frozen-lockfile
bun run --cwd testkit/node build
mkdir -p dist
GOTOOLCHAIN=go1.27.3 go build -o "$PWD/dist/self-host-testkit" ./cmd/self-host-testkit
SELF_HOST_TESTKIT_BINARY="$PWD/dist/self-host-testkit" node testkit/node/examples/sonarr.mjs
```

The final command starts a real Sonarr App. Run it only when you intend to use
Docker. The explicit helper path prevents mixing a released helper with source SDK.

## Headless and container requirements

The **Apps run in containers; your test process runs on the Docker host**.
No browser or terminal UI is needed.

| Test environment                                                    | Fixture topology                                                                      |
| ------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Linux/macOS, x64/arm64                                              | Supported with local Docker Engine and a Unix socket; Docker Desktop must be running. |
| GitHub-hosted Ubuntu                                                | Simplest headless route: run tests directly on the runner.                            |
| Self-hosted Linux/macOS runner                                      | Same local topology, resource limits and Docker permissions required.                 |
| Containerized test runner, `jobs.<job>.container`, Docker-in-Docker | Unsupported.                                                                          |
| Remote Docker/TCP endpoint or native Windows                        | Unsupported.                                                                          |

You also need:

- Docker Compose **v2** (`docker compose`), and permission to access local Docker.
- Enforced memory, CPU and PID limits: at most five containers per fixture, each
  capped at **1 GiB, one CPU and 512 PIDs**. Budget up to **5 GiB and five CPUs**
  per simultaneous fixture, plus the test runner and image/storage overhead.
- Network access and disk space for images; explicit release setup also needs
  network access and `tar` for its checksummed helper archive.

A socket mount alone does not make a containerized runner work: the returned
`127.0.0.1` Address belongs to the Docker host, and bind-mount paths are interpreted
on the daemon's host, not inside the test container. There is no supported
socket-mount or privileged-container shortcut.

Cold image pulls and Setup can take minutes. The helper has a **15-minute maximum
fixture lifetime**; Node currently accepts up to **14 minutes**, reserving cleanup
time. This is a lifetime bound, not just a startup timeout. Always close fixtures;
SIGKILL, daemon failure or runner loss can prevent cleanup even with an Action
post step. Prefer disposable CI runners.

Apps requiring browser-only Manual steps, external accounts, hardware, host
networking, socket mounts or named volumes fail explicitly. Not every Catalog
App is a supported fixture. See [fixture validation status](#fixture-validation-status)
for the narrow consumer smoke coverage. Broader live App acceptance and
Backup/Restore caching remain deferred; passing transport tests establishes neither.

## Ordered pairs

`withPair("homarr", "sonarr", callback, { order: "consumer-first" })` starts both
Apps in one disposable Project through production Setup and Links. Choose
`target-first` for a separate fresh scenario; two independent `spinup` calls do
not share a Project. App views cannot close a peer: close the Pair or its manager.
The same five-container/5-GiB/five-CPU aggregate guards and lifetime apply to the
whole pair, including dependencies. No resource override is available.

The optional `testkit/node/examples/homarr-sonarr.mjs` accepts either installation order as its
first argument. It starts real Docker Apps: run only in an approved runtime slot.
The integrated Sonarr verifier registration is required for advancing Stats.
No downloader adapter or Kuma fix is needed. Pair/native/rendered acceptance is
not established by helper or transport tests; see the Node guide for boundaries.

### Opt-in Sonarr preservation witness

The matching Go helper and Node SDK expose
`CreateSonarrFilterWitness` / `ReadSonarrFilterWitness` and
`createSonarrFilterWitness()` / `readSonarrFilterWitness()`. Enable the capability
explicitly with `PairOptions.SonarrFilterWitness` in Go or
`sonarrFilterWitness: true` in Node, only for a Homarr consumer and Sonarr target.
Select Sonarr through the existing `Images` / `images` option using a reviewed
immutable `repository@sha256:...` reference. Default and standalone fixtures do
not gain write permission or perform capability negotiation.

The Node SDK checks the helper's static capability before starting that opted-in
Pair. Creation then requires the complete owned fixture, authenticated Sonarr v4
identity and an empty custom-filter collection. It writes exactly one unique,
unselected saved series filter with empty predicates, returning only its original
ID and opaque content hash. Reads address that original object and fail on
missing/changed data or image drift; they never reseed after reinstall. A tag is
not used because Sonarr housekeeping deletes unused tags.

This capability is not an arbitrary HTTP/configuration writer and starts no
series, provider, scan or download job. Non-purge target removals must retain the
witness; normal final Pair cleanup removes the disposable fixture. Use requires
separate approval of the actual image/source contract and runtime slot. Fake
tests and compiled capability support do not prove native data preservation.
See the [Node witness contract](node/README.md#sonarr-data-preservation-witness).

## Guides

- [Node: mock replacement, API, CI and recovery](node/README.md)
- [GitHub Action metadata](action.yml)
- [Go API](https://pkg.go.dev/github.com/ajnart/self-host/testkit)
- [Release packaging](../docs/releasing.md)
