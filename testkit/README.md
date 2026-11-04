# self-host testkit

Real, isolated App fixtures for integration tests. Keep your existing API adapter;
replace its mock Address and API key with a real App configured by self-host's Setup.

This guide describes **1.3.0**, which adds `withApp`, ordered Pairs and the
automatic-cleanup JavaScript Action. Version 1.2.1 has none of them.

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

## Published release

Version 1.3.0 must exist on npm and GitHub before using these commands; until
then, use the source checkout above:

```bash
npm install --save-dev @ajnart/self-host-testkit@1.3.0
npm exec -- self-host-testkit-setup
```

Explicit setup verifies and caches the matching released helper; **no Go required**.
Package installation and import never download a helper or start Docker.
The 1.2.1 composite Action requires its documented separate cleanup step; for
1.2.1, follow its [immutable Node guide](https://github.com/ajnart/self-host/blob/5bd44f7f4cd3c350d6cf449e958eae6c0df28a69/testkit/node/README.md).

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
App is a supported fixture. Live App acceptance and Backup/Restore caching remain
deferred; passing transport tests does not establish either.

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

## Guides

- [Node: mock replacement, API, CI and recovery](node/README.md)
- [GitHub Action metadata](action.yml)
- [Go API](https://pkg.go.dev/github.com/ajnart/self-host/testkit)
- [Release packaging](../docs/releasing.md)
