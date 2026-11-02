# self-host testkit

Start a real self-hosted App for an integration test, get its Address and API key,
then clean it up. The testkit reuses self-host's existing App Setup; your tests
do not need to configure Sonarr, Radarr or another supported App themselves.

```typescript
import { createSelfHost } from "@self-host/testkit";

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
} finally {
  await selfHost.close();
}
```

## Requirements

- **Docker Engine running locally**, with the **Docker Compose v2 plugin**
  (`docker compose`, not the legacy `docker-compose` command).
- Linux or macOS, x64 or arm64, with a local Unix Docker socket. Docker Desktop
  needs to be running. Remote Docker endpoints and native Windows are unsupported.
- Docker memory, CPU-quota and PID-limit support. Each fixture allows at most
  five containers, each capped at 1 GiB, one CPU and 512 PIDs.
- Node.js 22+ for the TypeScript/JavaScript SDK. Go is needed only for Go callers
  or building the helper from source; released Node packages use a prebuilt helper.
- Internet access and enough disk space to pull the selected App images. The
  helper setup command also needs `tar` to extract a checksummed release archive.

Startup can take several minutes on a cold Docker image cache. Each App gets a
separate temporary Project, random loopback-only ports and private credentials.
It never uses your normal self-host Project. Always close fixtures; Docker access
is privileged, and a killed runner cannot guarantee cleanup.

## Where does `@self-host/testkit` come from?

It is the package in [`node/`](node/), not a third-party dependency. **It is not
published on npm.** The npm-compatible tarball is attached to the
[v1.2.1 GitHub release](https://github.com/ajnart/self-host/releases/tag/v1.2.1):

```bash
npm install --save-dev https://github.com/ajnart/self-host/releases/download/v1.2.1/self-host-testkit-1.2.1.tgz
npm exec -- self-host-testkit-setup
```

Its `package.json` names it `@self-host/testkit`, so that is the import path even
when npm installs it from a tarball. `npm install @self-host/testkit` will not work
until a registry publication is explicitly configured and approved.

**Use SDK 1.2.1 and its matching helper.** The setup command, generated types,
typed errors and cleanup fixes are not in the older v1.2.0 tarball. Its release
assets remain unchanged. To develop from source, build the SDK and helper together:

```bash
git clone https://github.com/ajnart/self-host.git
cd self-host
bun install --frozen-lockfile
bun run --cwd testkit/node build
GOTOOLCHAIN=go1.27.3 go build -o "$PWD/dist/self-host-testkit" ./cmd/self-host-testkit
SELF_HOST_TESTKIT_BINARY="$PWD/dist/self-host-testkit" node testkit/node/examples/sonarr.mjs
```

This source workflow also requires Bun 1.4.3+ and a Go launcher capable of
downloading Go 1.27.3. It starts a real Sonarr fixture; only run the demo when you
intend to use Docker. A fresh clone follows main, so unmerged branch changes
require checking out their branch first.

## Guides

- [TypeScript/JavaScript: install, Vitest, API, cleanup and troubleshooting](node/README.md)
- [Go API](https://pkg.go.dev/github.com/ajnart/self-host/testkit)
- [GitHub Action](action.yml), with a mandatory always-run cleanup step
- [Release packaging](../docs/releasing.md)

Apps needing a browser-only Manual step, external account, hardware, host
networking, socket mounts or named volumes fail explicitly; not every Catalog
App is a supported fixture. Backup/Restore caching is deferred and unavailable.
Live Docker acceptance remains separate from transport and fake-system tests.
