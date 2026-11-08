# @ajnart/self-host-testkit

## Run your existing tests (unreleased)

This checkout adds a command runner and optional Action `apps` / `config` / `run`
inputs. **These are not included in the published 1.3.2 package or the v1.3.0
Action pin shown elsewhere in this guide.** They require a built source checkout
until a new SDK and Action release is published together.

The runner handles App startup, environment, cleanup and reports. Keep your test
runner and assertions, including Vitest, Jest, Playwright or pytest. There is no
new test DSL and no special test-file suffix. Tests that already read configurable
endpoints need only an environment-name mapping; hardcoded URLs or mocks still
need to be changed. Starting an App alone cannot establish adapter correctness.

From a source checkout, build the SDK and matching helper as described in the
[parent guide](../README.md#try-this-source-checkout), then:

```bash
SELF_HOST_TESTKIT_BINARY="$PWD/dist/self-host-testkit" \
  node testkit/node/cli.mjs --apps sonarr,radarr -- npm test
```

The packaged form after release is `self-host-testkit-run --apps sonarr,radarr --
npm test`. The command after `--` is passed as an argument vector, without shell
interpolation. For a shell pipeline, explicitly use `-- sh -eu -c '…'`.

Every declared App starts concurrently by default. The runner waits for all
starts to settle, runs the existing command once only if every App is ready, and
always attempts cleanup. A failed startup skips the test command and remains a
failure. A failed test preserves its exit code. Failed cleanup also fails the run.

Each App exports the following variables only to the test command and its children:

- `SELF_HOST_SONARR_URL`
- `SELF_HOST_SONARR_API_KEY`
- `SELF_HOST_SONARR_USERNAME`, `SELF_HOST_SONARR_PASSWORD`, `SELF_HOST_SONARR_EMAIL`

Aliases are uppercase with hyphens converted to underscores. To use existing
variable names or test multiple image versions, add `self-host.testkit.json`:

```json
{
  "apps": {
    "sonarr": {
      "app": "sonarr",
      "image": "lscr.io/linuxserver/sonarr:latest",
      "env": { "url": "SONARR_URL", "apiKey": "SONARR_API_KEY" }
    },
    "radarr": "radarr"
  },
  "concurrency": 2,
  "timeoutMs": 720000
}
```

Then run `self-host-testkit-run --config self-host.testkit.json -- npm test`
(or the source CLI path above). Unmapped fields retain their default names. The
config rejects duplicate environment names and reserved runtime/CI variables.
Do not put credentials in this file; the fixtures generate private credentials.

### Parallelism and lifetime

- Declare 1–8 Apps. Budget memory/CPU for **all** of them at once: up to five
  containers per App, each capped at 1 GiB / one CPU / 512 PIDs. Eight Apps can
  require 40 GiB. Use a larger disposable runner or a matrix for large suites.
- `--concurrency 2` limits simultaneous **startups**, not the number of Apps kept
  running during tests. The default starts all declared Apps together.
- The whole startup + test-command budget defaults to 12 minutes. Set
  `--timeout-ms` or `timeoutMs` up to 780000 (13 minutes). Cleanup has a separate
  reserve. Choose a job timeout of at least 20 minutes for the default budget.
- SIGINT, SIGTERM and timeout stop the command process group and attempt cleanup.
  SIGKILL, runner loss or a failed Docker daemon can still prevent it. Keep the
  Action's post cleanup and prefer disposable hosts.
- These are independent single-App fixtures. For Links between Apps, continue to
  use `withPair`; merely declaring two Apps does not wire them together.

### Reports and GitHub Actions

The runner writes `self-host-testkit-report/report.json` and `report.md`, and
appends the Markdown to the GitHub job summary when available. Change the folder
with `--report-dir`. Reports include each App's Setup outcome and duration,
resolved image ID/digests, the test command outcome, interruption and cleanup.
They intentionally omit credentials, Addresses, command arguments, Project files
and raw errors/output. Existing test output still streams to the terminal; ensure
your tests do not log credentials. Fixture credential values are masked in GitHub
Actions before the test command runs.

These are lifecycle reports, not individual test-case results or a claim that a
suite covers every integration. Keep your test runner's JUnit/coverage reporter
for assertion-level detail.

Once a runner-capable SDK and Action are released, pin both and replace separate
startup/test steps with:

```yaml
- run: npm ci
- uses: ajnart/self-host/testkit@<reviewed-runner-capable-commit>
  with:
    apps: sonarr,radarr
    run: npm test
- uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9
  if: always()
  with:
    name: integration-report
    path: self-host-testkit-report/
```

The placeholder is deliberate: no released Action currently provides this path.
For development in this checkout, use `uses: ./testkit` with `source: "true"`.
The source Action builds the matching Go helper and uses the checked-in SDK.
In a monorepo, set `working-directory`; use `config: self-host.testkit.json`
instead of `apps` for custom variable names. `run` uses Bash with `-euo pipefail`, like a normal
GitHub Actions run step. Optional `concurrency`, `timeout-ms` and
`report-directory` map to the CLI. Omitting `run` preserves the original
setup-only Action behavior.


Real App fixtures for integration tests, using self-host's existing headless
Setup. ESM only, Node.js 22+, no runtime npm dependencies or test-runner plugin.

This guide describes **1.3.2**, which keeps the **1.3.0** testkit APIs compatible.
`withApp`, ordered Pairs, `recoveryPath` getters, the Sonarr witness and the JavaScript
Action's automatic post cleanup are not in 1.2.1. For 1.2.1, see its
[immutable guide](https://github.com/ajnart/self-host/blob/5bd44f7f4cd3c350d6cf449e958eae6c0df28a69/testkit/node/README.md).
Version 1.2.1 already supports manifest-based cleanup recovery.

## Requirements and setup

Run tests directly on a Linux/macOS x64/arm64 Docker host with Node.js 22+, local
Unix socket access and Docker Compose v2. Docker Desktop must be running. Docker
must enforce memory, CPU and PID caps: up to five containers per fixture, each
limited to 1 GiB, one CPU and 512 PIDs. Budget up to 5 GiB and five CPUs per fixture,
plus runner overhead, disk space and network access for images.

GitHub-hosted Ubuntu is the simplest headless environment. Self-hosted runners
need the same limits and permissions. Containerized test runners, GitHub job
containers, Docker-in-Docker, remote/TCP Docker and native Windows are unsupported.
A mounted socket is insufficient: App loopback Addresses and bind-mount paths
belong to the daemon host, not the test container. See the [support matrix](../README.md#headless-and-container-requirements).

**Published release:** version 1.3.2 must exist on npm and GitHub before using
these commands. Install it and explicitly prepare its matching helper for local use:

```bash
npm install --save-dev --save-exact @ajnart/self-host-testkit@1.3.2
npm exec -- self-host-testkit-setup
```

Released setup requires network access and `tar`, verifies the archive's SHA-256
checksum and reuses a private versioned helper cache. It needs **no Go** and does
not start Docker or modify PATH. Neither npm installation nor import downloads
anything.

For CI, follow the complete [external-repository quickstart](../README.md#ci-in-another-repository).
The Action prepares the helper for you; no separate setup command or Go is needed.
See [fixture validation status](../README.md#fixture-validation-status) before
selecting an App; the Catalog is not a tested-fixture list.

**Source:** follow the [checkout build](../README.md#try-this-source-checkout),
which builds generated SDK exports with Bun and the helper with Go, then selects
that helper using `SELF_HOST_TESTKIT_BINARY`. Run the included demo from this
checkout so the package self-import resolves to its matching SDK.

Helper resolution: `binary` option, `SELF_HOST_TESTKIT_BINARY`, private versioned
cache, then `self-host-testkit` on PATH. Keep SDK and helper on the same release
or source commit; matching version strings alone do not prove source identity.
The normal `self-host` CLI is not the testkit helper.

## Replace a mock, not your adapter

Pass the real App's `url` and key to the adapter you already test. For example,
if your existing test helper is `testSonarrAdapter({ baseUrl, apiKey })`, replace
its mock configuration with:

```javascript
import { withApp } from "@ajnart/self-host-testkit";

await withApp("sonarr", async (sonarr) => {
  await testSonarrAdapter({
    baseUrl: sonarr.url,
    apiKey: await sonarr.getApiKey(),
  });
});
```

`testSonarrAdapter` denotes your own existing test code, not another package to
install. Keep its real assertions for meaningful data and rejected credentials;
HTTP 200 or a running container alone is not integration acceptance. Never print
API keys, credentials or raw response bodies in assertion failures.

For a standalone example without an adapter, run `examples/sonarr.mjs` after the
source build. It checks Sonarr's authenticated identity and prints no secrets.

## Scoped fixtures and timeouts

`withApp(id, callback, options?)` returns the awaited callback result and always
attempts cleanup after startup or callback failure. The callback receives the
existing `App` API, including `url` and `getApiKey()`; it is runner-independent.
Options combine `binary`, `timeoutMs`, `manifest`, an optional `image` override
and the same optional `onCleanupError` as `withPair`.

Use your runner's existing async-test API and increase its timeout for image
pulls, Setup, assertions **and cleanup**. For Node's built-in runner:

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
  });
});
```

`timeoutMs` defaults to 720,000 (12 minutes), accepts 1–840,000 (14 minutes) and
bounds the **App lifetime**, not just startup. The helper's hard maximum is 15
minutes, with time reserved for cleanup. Cold pulls can take minutes; avoid
sharing one fixture across a suite longer than its lifetime. Bound concurrency
and allow cleanup time in your test hooks and CI job timeout.

When startup or your callback throws, `withApp` preserves that exact thrown value,
including primitives or `undefined`, even if cleanup also fails. If there is no
primary failure, cleanup failure is thrown. For recovery after a primary failure,
use the Action's shared manifest or select your own private manifest in advance.

## Advanced: manage fixture lifetime yourself

Use `createSelfHost` when existing hooks need to own several App lifetimes:

```javascript
import { createSelfHost } from "@ajnart/self-host-testkit";

const selfHost = createSelfHost({ timeoutMs: 720_000 });
try {
  const sonarr = await selfHost.spinup("sonarr");
  const apiKey = await sonarr.getApiKey();
  await testSonarrAdapter({ baseUrl: sonarr.url, apiKey });
} finally {
  await selfHost.close();
}
```

- `createSelfHost({ binary?, timeoutMs?, manifest? })` creates a manager without
  starting Docker. `spinup(id, { image? })` awaits Setup. Every App has its own
  temporary Project and random loopback-only Address.
- `getApiKey(name?)` explicitly reads a key; the default name is `api_key`.
- `credentials()` returns private `username`, `password`, `email`, `apiKey` fields.
  JSON/inspection redact the object, but reading a field returns the real secret.
- `info()` returns public `id`, `url`, `image`, `imageId`, `repoDigests`. Record
  digests for reproducibility; `latest` is mutable. An `image` override changes
  the selected App's image, not dependency images.
- `app.close()` closes one fixture; `selfHost.close()` closes its fixtures.
  Concurrent calls share one attempt; successful cleanup is idempotent. A failed
  close can be retried, but does not reopen the manager for new starts.
- Successful cleanup removes owned containers, networks, anonymous container
  volumes and private storage. Named volumes are rejected.

The advanced example uses ordinary `finally` semantics: a cleanup error can
replace an earlier error. Prefer `withApp` when exact primary-error preservation
matters.

## Ordered pair API

Pair calls require the helper matching this SDK; 1.2.1 does not contain them. Existing `spinup`/`withApp` calls are unchanged.

```javascript
import { withPair } from "@ajnart/self-host-testkit";

await withPair(
  "homarr",
  "sonarr",
  async (pair) => {
    const result = await pair.inspect();
    if (result.outcome !== "complete") throw new Error("Pair needs attention");
    const key = await pair.target.getApiKey();
    await testSonarrAdapter({ baseUrl: pair.target.url, apiKey: key });
  },
  { order: "consumer-first" },
);
```

`createSelfHost().spinupPair(consumer, target, { order, images? })` returns the
same Pair for explicit lifetime management. `order` must be `consumer-first` or
`target-first`; both are sequential production installs into one fresh Project.
`images` may override only selected main App images, keyed by App ID.
Pairs need web Apps whose required Inputs have defaults or are host ports, and
one declared consumer Link. Unsupported Compose/account/hardware/private-input boundaries
remain explicit errors or Manual outcomes, not automatic skips.

- `consumer`/`target` expose `url`, `getApiKey`, `credentials`, `info` and private
  recovery access, but **no close method**. Only Pair/manager close owns cleanup.
- `initial` records first-install absence. `inspect()` returns selected public
  observations and `complete`, `manual`, `prerequisite` or `failed`. Missing,
  skipped or unready Links are never complete. Completion is the production
  recorded outcome, not a claim of rendered acceptance for every peer.
  Selected Info and inspection results must retain the selected IDs and exact
  loopback Addresses established at startup, including their frozen ports/paths.
  Inspections must also retain the startup-declared Link ID; response steps cannot
  nominate a replacement Link to satisfy completion.
  Step IDs must be unique within each App. Summaries must agree with observed
  failures, Manual Setup and prerequisite facts; contradictory results reject
  through the same awaited whole-owner cleanup boundary.
- `setup(app?)` retries production Setup for one/both selected Apps;
  `remove(app)` is non-purge and retains data; `install(app)` reinstalls only a
  selected admitted App with its original ports/options. No action renews the
  lifetime. An absent App cannot expose stale Info/credentials.
- One owner/admission budget covers both Apps plus every backing service:
  five containers, 5 GiB, five CPUs; first-creation 1 GiB/one CPU/512-PID caps.
  Local Access/private binds/volume/socket restrictions remain; no override or
  arbitrary command/secret-write interface is exposed.
- An infrastructure action/inspection or selected-view key/credential/Info failure
  immediately attempts independent-context whole-Pair cleanup and disables further actions.
  Expected Manual/incomplete Setup remains inspectable. `TestkitError` preserves
  an `OPERATION` reason (such as `LIMITS`), or `TIMEOUT`/`CANCELED`, and an additive
  `cleanupFailed` flag. A failed cleanup retains private recovery ownership and
  can be retried with close/manifest
  recovery; it does not replace the original action cause.
- Selected-view key/credential/Info reads share the Pair's original work deadline.
  Ordinary missing keys, absent Apps and Manual credentials remain nonfatal;
  cancellation, deadline and storage failures disable the whole owner. Malformed
  action/inspection or selected-view results likewise disable the owner and await
  owned cleanup before rejecting with `PROTOCOL`, retaining any secondary `cleanupFailed` flag.
  Retrying cleanup never re-enables Pair or selected-view actions.
- Beginning explicit Pair close also permanently disables live actions and reads,
  even if cleanup fails. Close remains retryable within the original lifetime.
- `withPair` preserves exact primary failures, including `undefined`, over
  cleanup failure. Cleanup-only failures throw. Optional `onCleanupError` receives
  a sanitized cleanup error when there is already a primary failure; failure of
  that notification does not replace the original. Recovery remains manifest-
  scoped and uses the existing Action post hook.

`examples/homarr-sonarr.mjs` demonstrates one order with authenticated Sonarr
identity and production-Setup-driven advancing Stats. Run a second fresh scenario
with `target-first` to test the other order. It never configures Homarr in Node,
does not require a downloader or Kuma, and must wait for integrated Sonarr leaf
registration plus a centrally approved runtime slot. Empty/native/error and
rendered/lifecycle acceptance remain separate evidence; no runtime is implied by
building or importing the SDK.

## GitHub Actions

**Another repository:** start with the complete
[published-package workflow and test](../README.md#ci-in-another-repository).
Install your locked SDK dependency before the SHA-pinned Action. It selects the
installed SDK's exact helper release; changing the Action pin alone does not
upgrade that dependency. Keep ordinary `withApp` cleanup as well as the Action's
automatic post cleanup.

**Monorepos:** installation, Action resolution and tests must all use the
consumer's package directory. For a package under `packages/api`, set
`working-directory: packages/api` on its `run` steps (or `defaults.run`) and
pass this separately to the Action:

```yaml
- uses: ajnart/self-host/testkit@eb8569ec50a8c62d1406d55b19f6bfee218a4240
  with:
    working-directory: packages/api
```

For a shared workspace lockfile, install at the workspace root as required by
your package manager, then resolve/run tests from the consuming package.
`defaults.run.working-directory` does not apply to `uses` steps. The Action does
not link a source SDK into your consumer.

**Other package managers:** Bun and pnpm can install the same exact dependency
using their normal locked CI commands (`bun install --frozen-lockfile` or
`pnpm install --frozen-lockfile`) before the Action. Ensure their runtime is set
up and that the package is resolvable through `node_modules` from the consumer
directory. Yarn Plug'n'Play without `node_modules` is not supported by this
Action's Node package resolver. Vitest/Jest users can keep their own async test
API and invoke their existing test command; increase test/hook timeouts and
bound worker concurrency rather than using the runner's short defaults.

**Source workflow:** the local Action builds its helper and uses the generated
SDK from the same checkout. The demo starts a real Sonarr App; run it only when
you intend to use Docker.

```yaml
jobs:
  integration:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v7
      - uses: ./testkit
        with:
          source: true
      - run: node "$SELF_HOST_TESTKIT_SDK/examples/sonarr.mjs"
```

The **JavaScript Action** registers automatic post cleanup, including after a
test step fails. Keep ordinary fixture cleanup too; post cleanup is a safety net,
not a guarantee after runner loss. No separate cleanup step is needed for this
new Action. The published 1.2.1 composite Action does not have this post hook.

In default release mode, install the consumer SDK with `npm ci` before the Action;
it resolves that installed SDK and downloads its exact matching checksummed
helper, without Go. Pin a reviewed Action commit. For monorepos, set `working-directory` to the
consumer directory and run installation/tests there. The Action does not install
npm dependencies or start Apps. Outputs remain `binary-path`, `sdk-path`, `manifest-path`; environment
variables remain `SELF_HOST_TESTKIT_BINARY`, `SELF_HOST_TESTKIT_SDK`,
`SELF_HOST_TESTKIT_MANIFEST`.

With `source: true`, the Action privately obtains Go and builds its helper,
using that checkout's committed generated SDK, with no release fallback. Source
mode also requires Python 3 and `tar`. Tests must import **that** SDK, not an older
installed npm package; for the included demo, run
`node "$SELF_HOST_TESTKIT_SDK/examples/sonarr.mjs"`. The consumer's working
directory does not change which source checkout supplies the helper and SDK.

## Errors and private recovery

`TestkitError` has sanitized `code`, optional startup/operation `reason` with safe
hints, and a `cleanupFailed` flag for secondary cleanup trouble.
Codes: `BINARY`, `PROTOCOL`, `STATE`, `START`, `CREDENTIALS`, `INFO`, `CLEANUP`,
`TIMEOUT`, `CANCELED`, `TRANSPORT`, `OPERATION`. Raw provider output and helper stderr
are not forwarded.

- **BINARY:** explicitly prepare a matching helper or set its absolute path.
- **START:** check Docker/Compose, resource limits and image availability. Apps
  needing Manual steps, external accounts, hardware, host networking, socket
  mounts or named volumes fail explicitly, rather than silently skipping Setup.
- **TIMEOUT:** budget cold pulls within the lifetime or reduce concurrency.
- **CLEANUP:** retain private recovery storage and retry once Docker is available.

`selfHost.recoveryPath` and `app.recoveryPath` explicitly expose the private
manifest filename; `TestkitError.recoveryPath` may expose it when available.
These properties are non-enumerable and must not be logged or uploaded. To choose a
manifest yourself, provide an absolute filename inside an existing owned
mode-0700 directory. The helper records ownership before starting containers.

Retry `close()` on a retained handle, or after helper exit use the matching helper:

```bash
"$SELF_HOST_TESTKIT_BINARY" --cleanup-manifest "$SELF_HOST_TESTKIT_MANIFEST"
```

The Action sets both variables; outside CI, set the manifest variable privately
from your chosen manifest or recovery getter. Failed cleanup retains the marker
for retry. Do not upload manifests, Project files or credentials as artifacts;
do not use Docker prune or broad container/volume deletion for recovery. SIGKILL,
runner loss and daemon outages can still prevent cleanup; prefer disposable runners.

## Sonarr data-preservation witness

This capability is new in 1.3.0. Keep the SDK and helper on the same release or
reviewed checkout. For a fresh ordered
`spinupPair("homarr", "sonarr", options)` or `withPair`, explicitly set
`sonarrFilterWitness: true` and provide `images.sonarr` as a reviewed immutable
`repository@sha256:<64 lowercase hex digits>` reference. Omission preserves the
legacy startup sequence and grants no witness permission. The two installation
orders use the same methods in separate fresh Pairs.

Before opted-in Pair startup, the SDK requires protocol-v1 static capability
`sonarrFilterWitness: 1` from its helper and then the matching startup echo.
An old or incompatible helper fails safely before `spinupPair`; there is no
fallback to Node HTTP. Capability support is not image/source approval: review
the resolved Sonarr release and image separately before any approved native run.
The current witness contract accepts authenticated stable Sonarr v4 identity,
requires a complete/running owned Pair and refuses an existing saved-filter
collection. Default/standalone APIs and resource/lifetime limits are unchanged.

An opted-in Pair exposes exactly two zero-argument methods:

```typescript
const original = await pair.createSonarrFilterWitness();
const before = await pair.readSonarrFilterWitness();
if (
  before.kind !== original.kind ||
  before.objectId !== original.objectId ||
  before.contentSha256 !== original.contentSha256
) {
  throw new Error("The original Sonarr witness changed");
}
```

Each returns an immutable `SonarrFilterWitness` with `kind` equal to
`"sonarr-custom-filter-v1"`, the original positive `objectId`, and a 64-character
lowercase `contentSha256`. The Go helper generates the opaque synthetic label;
Node never supplies or receives its content, resource selector, request body or
credential. The saved object is an unselected series filter with no predicates,
not a series, profile, tag, provider or job. Creation uses the official Sonarr v3
custom-filter API once and confirms the new identity by readback. Tags would not
be durable because Sonarr housekeeping deletes unused tags.

Create once after the production Pair Link is complete. Read before removal and
after each of two ordinary `pair.remove("sonarr")` / `pair.install("sonarr")`
cycles; compare all three fields with the original receipt. Do not call Create
inside a cycle. Repeated creation, read-before-create, ambiguous POST results,
changed/missing data, unsupported schemas and image drift fail rather than adopt
or recreate a witness. Owner work deadlines, mutex, credential owner and scoped
failure/cleanup behavior are reused. A timeout or failed write cannot be retried
within that Pair. Receipt IDs/hashes may be recorded; never export credentials,
nonce-bearing bodies, App logs or private fixture files.

An invalid witness receipt or startup capability echo disables that helper's
whole owner and awaits scoped cleanup before rejecting with `PROTOCOL`.
Secondary cleanup failure is retained as `cleanupFailed: true`; it does not
replace the protocol failure or expose raw causes. Subsequent actions are
rejected, while explicit close/manifest recovery may retry failed cleanup.

Both cycles must retain data through non-purge target removal. Final Pair/manager
close still cleans the complete owned disposable fixture; do not add an API
delete, broad Docker teardown or attach/reseed recovery. A helper lost before
verification can be recovered for cleanup, not resumed as a successful proof.
Fake transport tests do not establish native preservation, populated-media
semantics, rendering, cross-kind survivors or stored-key corruption/recovery.

## Source maintenance

`src/*.mts` owns the generated `.mjs` and `.d.mts` files. From the repository root,
run `bun run --cwd testkit/node build`, then `check` and `lint` with the same
command prefix. Never mix source SDK output with an unrelated released helper.
For Go callers, see the [Go API](https://pkg.go.dev/github.com/ajnart/self-host/testkit).
The [consumer smoke workflow](../../.github/workflows/testkit-smoke.yml) covers
the narrow published Sonarr path described in the [validation status](../README.md#fixture-validation-status).
Broader live App acceptance and Backup/Restore caching remain deferred;
transport tests do not establish either.
