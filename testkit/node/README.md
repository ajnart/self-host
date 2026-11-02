# @ajnart/self-host-testkit

Real App fixtures for integration tests, using self-host's existing headless
Setup. ESM only, Node.js 22+, no runtime npm dependencies or test-runner plugin.

**Unreleased source:** `withApp`, recovery paths and the JavaScript Action's
automatic post cleanup described here are not in published **1.2.1**. The version
is still 1.2.1; build this checkout to try the changes. For published usage, see
the [immutable 1.2.1 guide](https://github.com/ajnart/self-host/blob/5bd44f7f4cd3c350d6cf449e958eae6c0df28a69/testkit/node/README.md)
(`createSelfHost`, matching helper setup and explicit cleanup).

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

**Source:** follow the [checkout build](../README.md#try-this-source-checkout),
which builds generated SDK exports with Bun and the helper with Go, then selects
that helper using `SELF_HOST_TESTKIT_BINARY`. Run the included demo from this
checkout so the package self-import resolves to its matching SDK.

**Published 1.2.1 only:** install and explicitly prepare its matching helper:

```bash
npm install --save-dev @ajnart/self-host-testkit@1.2.1
npm exec -- self-host-testkit-setup
```

Released setup requires network access and `tar`, verifies the archive's SHA-256
checksum and reuses a private versioned helper cache. It needs **no Go** and does
not start Docker or modify PATH. Neither npm installation nor import downloads
anything. Installing 1.2.1 does **not** install the unreleased `withApp` API.

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
Options combine `binary`, `timeoutMs`, `manifest` and an optional `image` override.

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

## GitHub Actions

**Unreleased source workflow:** run this on a branch containing these changes.
The local Action builds its helper and uses the generated SDK from the same
checkout, rather than the published 1.2.1 package. The demo starts a real Sonarr
App; run it only when you intend to use Docker.

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
helper, without Go. Pin a reviewed Action commit once available; no future SHA
or SDK release is claimed here. For monorepos, set `working-directory` to the
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

`TestkitError` has sanitized `code` and optional startup `reason` with safe hints.
Codes: `BINARY`, `PROTOCOL`, `STATE`, `START`, `CREDENTIALS`, `INFO`, `CLEANUP`,
`TIMEOUT`, `TRANSPORT`. Raw provider output and helper stderr are not forwarded.

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

## Source maintenance

`src/*.mts` owns the generated `.mjs` and `.d.mts` files. From the repository root,
run `bun run --cwd testkit/node build`, then `check` and `lint` with the same
command prefix. Never mix source SDK output with an unrelated released helper.
For Go callers, see the [Go API](https://pkg.go.dev/github.com/ajnart/self-host/testkit).
Live App acceptance and Backup/Restore caching remain deferred; transport tests
do not establish either.
