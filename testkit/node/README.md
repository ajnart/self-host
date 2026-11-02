# @ajnart/self-host-testkit

Real, isolated Docker App fixtures for TypeScript and JavaScript integration
tests. Start an App through self-host's existing Setup, call its API, and close
the fixture. ESM only: use `import`, not CommonJS `require`. No runtime npm
dependencies and no test-runner dependency.

## Before you start

You need **Node.js 22+**, a running local **Docker Engine** and the **Docker
Compose v2 plugin** (`docker compose version`). Linux and macOS x64/arm64 are
supported; Docker must use a local Unix socket and enforce memory, CPU and PID
limits. Allow up to 5 GiB RAM and five CPUs per fixture, plus image/storage space.
First startup pulls images and can take several minutes.

For a published release, install the SDK as a dev dependency. Version 1.2.1 must
exist on npm and GitHub before using these commands; for an unpublished source
checkout, use the source workflow linked below instead:

```bash
npm install --save-dev @ajnart/self-host-testkit@1.2.1
```

This guide describes version 1.2.1. The older v1.2.0 tarball does not contain the setup command,
`TestkitError` export or safe startup reasons. For that release, extract
`self-host-testkit` from its platform archive, verify `checksums.txt`, and set
`SELF_HOST_TESTKIT_BINARY` to the helper's absolute path. For the improved source,
follow the [checkout quickstart](../README.md#typescript-install).

Version 1.2.0 used the unpublished package name `@self-host/testkit`. When
upgrading, change imports to `@ajnart/self-host-testkit`. The exact npm package
is also available as `self-host-testkit-1.2.1.tgz` on the matching GitHub release.

## Install the matching helper

After installing the SDK, run:

```bash
npm exec -- self-host-testkit-setup
```

This explicit setup downloads only the matching Linux/macOS helper, requires one
SHA-256 checksum entry and verifies the archive before extraction. It caches the
helper privately under `$XDG_CACHE_HOME/self-host-testkit/`, or
`~/.cache/self-host-testkit/`. It does not run Docker, modify PATH, require Go, or
download anything during package installation or import. Repeating setup reuses
the cached helper. `createSelfHost()` discovers it automatically.

To use a source-built or offline helper instead:

```bash
export SELF_HOST_TESTKIT_BINARY=/absolute/path/to/self-host-testkit
```

Resolution order: `binary` option, `SELF_HOST_TESTKIT_BINARY`, the versioned
private cache, then `self-host-testkit` on PATH. Keep SDK, helper and Action on
the same release or immutable source commit. Never substitute the normal
`self-host` CLI: the helper is a separate executable.

## A small demo

Save this as `demo.mjs` and run `node demo.mjs` after installing the SDK/helper:

```javascript
import { createSelfHost } from "@ajnart/self-host-testkit";

const selfHost = createSelfHost();
try {
  const sonarr = await selfHost.spinup("sonarr");
  const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
    headers: { "X-Api-Key": await sonarr.getApiKey() },
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error("Sonarr rejected the request");
  const status = await response.json();
  if (status.appName !== "Sonarr") throw new Error("Unexpected App identity");
  console.log(`Connected to Sonarr ${status.version}`);
} finally {
  await selfHost.close();
}
```

The package also includes `examples/sonarr.mjs`. No API key appears in its output.
Cleanup uses ordinary `try/finally` or test-runner hooks, without requiring
TypeScript disposable libraries or newer JavaScript syntax.

## Vitest

Use your project's existing Vitest dependency. Set hook timeouts explicitly;
the default hook timeout is too short for image pulls and Setup.

```typescript
import { afterAll, beforeAll, expect, test } from "vitest";
import { createSelfHost, type App } from "@ajnart/self-host-testkit";

const selfHost = createSelfHost();
let sonarr: App;

beforeAll(async () => {
  sonarr = await selfHost.spinup("sonarr");
}, 780_000);

afterAll(async () => {
  await selfHost.close();
}, 160_000);

test("Sonarr returns its authenticated identity", async () => {
  const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
    headers: { "X-Api-Key": await sonarr.getApiKey() },
    signal: AbortSignal.timeout(10_000),
  });
  expect(response.ok).toBe(true);
  const status = await response.json();
  expect(status.appName).toBe("Sonarr");
  expect(status.version).toMatch(/^\d+\./);
});
```

This verifies identity, not complete integration acceptance. Your adapter tests
should also check meaningful data and invalid-key behavior. Do not print raw
response bodies, credentials or API keys in failing assertions.

## API

```typescript
const selfHost = createSelfHost({
  timeoutMs: 720_000,
});
const app = await selfHost.spinup("sonarr", {
  image: "lscr.io/linuxserver/sonarr:latest",
});
const apiKey = await app.getApiKey();
const credentials = await app.credentials();
const info = await app.info();
await app.close();
await selfHost.close();
```

- `createSelfHost({ binary?, timeoutMs?, manifest? })`: creates a fixture manager
  without starting Docker. `timeoutMs` defaults to 720,000 and accepts 1–840,000.
  It bounds the App lifetime, not just startup; cleanup time is reserved within
  that budget. Do not share a fixture across a longer-running suite.
- `spinup(id, { image? })`: awaits Setup and returns an App with its loopback
  `url`. Each call gets a separate helper and Project; image overrides apply to
  the selected App, not its dependency images.
- `getApiKey(name?)`: explicitly reads a private key; the default name is
  `api_key`. Use the App's declared key name when it differs.
- `credentials()`: `username`, `password`, `email`, `apiKey`. Fields are
  non-enumerable and JSON/inspection redact the object, but reading a field
  returns the actual secret. Never log it or upload it.
- `info()`: public `id`, `url`, `image`, `imageId`, `repoDigests`. Record image
  digests when reproducibility matters; `latest` is deliberately mutable.
- `app.close()` closes one fixture; `selfHost.close()` closes all its fixtures.
  Both are idempotent. Successful cleanup removes owned containers, networks,
  anonymous container volumes and private storage. Named volumes are rejected.
- `manifest`: optional absolute recovery filename in an existing owned mode-0700
  directory. Otherwise the SDK creates a private temporary parent. The helper
  records ownership before startup. Keep recovery manifests and Project data
  private; never upload them as public CI artifacts.

## Errors and recovery

```typescript
import { TestkitError } from "@ajnart/self-host-testkit";

try {
  await selfHost.spinup("sonarr");
} catch (error: unknown) {
  if (error instanceof TestkitError && error.code === "BINARY") {
    throw new Error(
      "Install the matching testkit helper before running this suite",
    );
  }
  throw error;
}
```

Errors expose a sanitized `code`: `BINARY`, `PROTOCOL`, `STATE`, `START`,
`CREDENTIALS`, `INFO`, `CLEANUP`, `TIMEOUT` or `TRANSPORT`. They never include raw
provider output, helper stderr or executable paths. A `START` error may also
have an allowlisted `reason` and a corresponding safe hint: `APP`, `OPTIONS`,
`DOCKER`, `CAPABILITIES`, `PORT`, `OWNERSHIP`, `LIMITS`, `LOCK`, `UNSUPPORTED`,
`STORAGE`, `CONTAINER` or `SETUP`. These distinguish unsupported fixtures from
failed Setup without forwarding provider messages.

- **BINARY:** run the explicit setup command or set the helper path.
- **START:** check local Docker/Compose, supported App prerequisites, resource
  limits and image availability. Apps needing a Manual step, hardware, external
  account, host networking, socket mount or named volume fail rather than skip.
- **TIMEOUT:** allow cold-pull time within the bounded lifetime, lower concurrent
  fixture count, or use a smaller supported App.
- **CLEANUP:** retain the private manifest and retry scoped recovery after helper
  exit. For CI, use the always-run step below. For local recovery, pass the
  matching helper `--cleanup-manifest /absolute/private/manifest.jsonl`.

If you supply a manifest, retain its path outside the test for recovery. SIGKILL,
runner loss and daemon outages can prevent cleanup; disposable runners are best.
Do not run Docker prune or broad container/volume removal to recover fixtures.

## GitHub Actions

```yaml
jobs:
  integration:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-node@v6
        with:
          node-version: 22
      - run: npm ci
      - id: testkit
        uses: ajnart/self-host/testkit@v1.2.1
      - run: npx vitest run path/to/live-integration.test.ts
      - name: Clean up owned fixtures
        if: always() && steps.testkit.outcome == 'success'
        run: '"$SELF_HOST_TESTKIT_BINARY" --cleanup-manifest "$SELF_HOST_TESTKIT_MANIFEST"'
```

Pin the Action to a full commit SHA and install the SDK from the same source or
release. The Action requires Bash, Python 3 and tar; it verifies and extracts a
patched Go toolchain privately for its own build without changing the caller's
PATH or `GOTOOLCHAIN`. It does not require a preinstalled Go launcher and works
when the caller sets `GOSUMDB=off`. Standard GitHub-hosted Ubuntu runners supply
these prerequisites.
The original v1.2.0 Action does not yet preserve the caller's Go environment.

Outputs: `binary-path`, `sdk-path`, `manifest-path`. Job environment:
`SELF_HOST_TESTKIT_BINARY`, `SELF_HOST_TESTKIT_SDK`, `SELF_HOST_TESTKIT_MANIFEST`.
The Action does not install the npm dependency or start Apps. Its committed SDK
exports and source-built helper match its ref. Set bounded matrix concurrency;
each simultaneous fixture adds its own resource budget. Always-run cleanup is
mandatory because composite Actions have no finally hook.

## Go

```bash
go get github.com/ajnart/self-host@v1.2.1
```

```go
peer, err := testkit.Start(ctx, testkit.Options{App: "sonarr"})
if err != nil {
    return err
}
defer func() {
    cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
    defer cancel()
    if err := peer.Close(cleanup); err != nil {
        log.Print("fixture cleanup failed; retain its private recovery path")
    }
}()
key, err := peer.GetAPIKey(ctx, "")
```

Import `github.com/ajnart/self-host/testkit`; it is part of the root module, not a
separate Go module. `RecoveryPath()` is private operational metadata. Go startup
errors preserve known safe testkit causes and context cancellation while hiding
raw provider errors. The current source removes storage on successful `Close`;
v1.2.0 retains it until a dead-process `Cleanup` pass.

## Developing this package

From the repository root:

```bash
bun install --frozen-lockfile
bun run --cwd testkit/node build
bun run --cwd testkit/node check
bun run --cwd testkit/node lint
bun test testkit/node/tests
npm pack ./testkit/node --ignore-scripts --dry-run
```

`src/*.mts` is the source of truth. TypeScript generates the committed `.mjs`
exports and `.d.mts` declarations; CI verifies they remain current. No hand-kept
parallel declarations. The tarball includes only runtime modules, public types,
license, README and the demo; it does not ship development dependencies or tests.
`SELF_HOST_RELEASE_VERSION=X.Y.Z node testkit/node/pack.mjs` validates the package
allowlist and adds its checksum to release artifacts. Build first. Version bumps
and npm trusted publishing require a separate release decision; never overwrite
v1.2.0 assets with a modified package.

Backup/Restore caching and live Docker acceptance are deferred, not implied by
passing SDK transport tests. The downstream Homarr weekly checklist is tracked
in [draft PR #7446](https://github.com/homarr-labs/homarr/pull/7446).
