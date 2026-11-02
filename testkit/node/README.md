# @self-host/testkit

Reusable App fixtures backed by the same Go Service and Setup as self-host, not a
second provisioning implementation. Requires Node 22+, a local Docker Engine and
Compose, and the matching `self-host-testkit` helper. Linux and macOS only.

## Install

The SDK is a release asset, not an npm registry publication. After v1.2.0 is
published, pin this exact devDependency:

```json
{
  "devDependencies": {
    "@self-host/testkit": "https://github.com/ajnart/self-host/releases/download/v1.2.0/self-host-testkit-1.2.0.tgz"
  }
}
```

Extract `self-host-testkit` from the corresponding platform's
`self-host_1.2.0_<os>_<arch>.tar.gz` after verifying `checksums.txt`. Set
`SELF_HOST_TESTKIT_BINARY` to its absolute path, or pass `binary`. Keep helper,
SDK and Action pinned to the same release or immutable source commit.

## Vitest

Use your project's existing Vitest dependency; this package has no runtime
dependencies or test runner dependency. Startup and cleanup hooks need explicit
timeouts ([Vitest hooks](https://vitest.dev/api/hooks)).

```typescript
import { afterAll, beforeAll, expect, test } from "vitest";
import { createSelfHost, type App } from "@self-host/testkit";

const selfHost = createSelfHost({ timeoutMs: 720_000 });
let sonarr: App;

beforeAll(async () => {
  sonarr = await selfHost.spinup("sonarr", {
    image: "lscr.io/linuxserver/sonarr:latest",
  });
}, 780_000);

afterAll(async () => {
  await selfHost.close();
}, 160_000);

test("the installed peer returns its identity", async () => {
  const apiKey = await sonarr.getApiKey();
  const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
    headers: { "X-Api-Key": apiKey },
  });
  expect(response.ok).toBe(true);
  const status = await response.json();
  expect(status.appName).toBe("Sonarr");
  expect(status.version).toMatch(/^\d+\./);
});
```

`spinup` returns a Promise; await it before reading `url` or requesting a key.
Each App has its own helper process and isolated Project. `getApiKey()` selects
`api_key`; supply an explicit name for another key. `credentials()` returns
`username`, `password`, `email`, `apiKey`; its fields are deliberately
non-enumerable and JSON/custom inspection redact the object. Explicitly reading
a field still returns a secret: never log it, put it in an assertion payload or
upload it. `info()` returns `id`, `url`, `image`, `imageId`, `repoDigests` for
image provenance, without recovery paths or credentials. Close is idempotent.

`createSelfHost` accepts `binary`, `timeoutMs` (1–840,000; default 720,000), and
an absolute `manifest` filename whose existing parent is owned and mode 0700.
Without `manifest` or `SELF_HOST_TESTKIT_MANIFEST`, the SDK makes a private
temporary parent. The helper creates the mode-0600 recovery manifest before
startup. Protocol v1 uses bounded private JSONL pipes; helper stderr and
provider errors never propagate into test exceptions. Errors expose a sanitized
`code`, not raw Setup output. Helpers have a bounded lifetime and use separate
cleanup deadlines on close, stdin EOF and SIGTERM/SIGINT.

## Go

The Go SDK is the root module's `testkit` package, not a nested module:

```bash
go get github.com/ajnart/self-host@v1.2.0
```

```go
package peer_test

import (
    "context"
    "testing"
    "time"

    "github.com/ajnart/self-host/testkit"
)

func TestPeer(t *testing.T) {
    peer, err := testkit.Start(t.Context(), testkit.Options{
        App: "sonarr", Timeout: 12 * time.Minute,
    })
    if err != nil {
        t.Fatal("peer startup failed")
    }
    t.Cleanup(func() {
        ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
        defer cancel()
        if peer.Close(ctx) != nil {
            t.Error("peer cleanup failed")
        }
    })
    key, err := peer.GetAPIKey(t.Context(), "")
    if err != nil || key == "" || peer.URL() == "" {
        t.Fatal("peer credentials or Address unavailable")
    }
}
```

The Go sketch demonstrates lifecycle only; downstream tests must additionally
assert meaningful adapter results and invalid-key behavior, not just liveness,
an HTTP 200 or a nonempty key.

## GitHub Actions

```yaml
steps:
  - uses: actions/checkout@v7
  - id: testkit
    uses: ajnart/self-host/testkit@v1.2.0
  - run: npm ci
  - run: npx vitest run path/to/live-integration.test.ts
  - name: Clean up owned App fixtures
    if: always()
    run: '"$SELF_HOST_TESTKIT_BINARY" --cleanup-manifest "$SELF_HOST_TESTKIT_MANIFEST"'
```

Pin the Action to the release's full commit SHA for immutable CI. It builds the
helper from its own checkout, not the consumer's checkout, and exposes
`binary-path`, `sdk-path`, and `manifest-path` outputs. The SDK directory is available as
`SELF_HOST_TESTKIT_SDK`; helper and private manifest locations are set for the
job. The source SDK and helper always match the Action ref; a separately
installed tarball dependency must match too. No CLI binary is required.

The final `if: always()` step is mandatory: a composite Action cannot provide a
finally step. Cleanup only uses recorded owned roots and the Go SDK's strict
ownership checks, never arbitrary Docker names or broad teardown. A forced
helper kill uses the same scoped cleanup. Retain the private manifest on cleanup
failure; never upload it, Project directories, credentials or backups as public
artifacts. Runner loss/SIGKILL cannot guarantee cleanup; disposable runners are
recommended. Set explicit job timeouts and bounded matrix concurrency.

## Coverage and limits

- A peer's mutable `latest` image is not the latest Homarr image: downstream
  tests exercise the current checkout's Homarr adapters against selected peers.
  Record `info()` image digests; do not claim a mutable tag is reproducible.
- Apps requiring Manual steps, external accounts, hardware, host networking,
  socket mounts or unsupported overrides fail startup rather than pass as skips.
  Keep an explicit unsupported checklist with reasons in the downstream suite.
- Fixtures are local loopback only and enforce core resource/ownership guards.
  Concurrent Apps are separate Projects, not a shared user installation.
- Backup/Restore caching is not implemented. Cold starts remain the baseline;
  safe cross-job path/port/identity rebasing and encrypted transport need separate
  validation before caching can be enabled. No automatic issue creation.
