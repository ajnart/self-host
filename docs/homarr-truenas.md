# TrueNAS external connection

## Outcome

`truenas` is an exact external Homarr integration kind on the shared external-connection seam. It adds no Catalog App, Compose service, installer, storage action or power action. Only explicit local configuration and Homarr's own recorded owned resources are managed. Source and fake-backed tests are delivered; native, rendered and lifecycle acceptance against a real TrueNAS endpoint is **not run**.

## Prerequisites

- A TrueNAS system you operate, reachable by Homarr over a trusted HTTPS origin (valid certificate for the hostname you give). Homarr speaks WebSocket (`/api/current`, `/websocket` fallback); self-host never opens one.
- A dedicated user-linked API key with the Read-Only Administrator role. Not root, not a sharing or write-capable role.
- Reporting data (CPU, memory) served by the system.

## Private input

Review an owner-only JSON file. Placeholder structure:

```json
{
  "endpoint": "https://truenas.example.invalid",
  "credentials": [{ "kind": "apiKey", "value": "<private-api-key>" }]
}
```

The only supported alternative is `apiKey`. Password or username login is rejected: it has no one-time-code path, so accounts with two-factor authentication cannot work. A custom port is allowed; a path, query, fragment, userinfo or `http://` endpoint is rejected before the key is forwarded. TrueNAS revokes a user-linked key presented over insecure transport, so never use HTTP.

No credentials are issued, rotated or printed. Never disable TLS verification or broaden TrueNAS access to make a connection pass.

## Surface

One read-only native surface: a Homarr `systemResources` widget (CPU and memory) plus its Stats (`cpu` percent, `memoryUsed` bytes, `memoryAvailable` bytes, `uptime` seconds). No pool, dataset, share, job, disk, network or temperature surface.

Setup verifies, in order:

1. Homarr's stored-credential `integration.testConnection` for the owned integration, so a cached socket cannot mask a revoked key.
2. Exactly one health result for the owned integration, no error, non-null data, a recent timestamp, CPU 0–100, nonnegative integral memory bytes with positive total, nonnegative uptime. Legitimate zeros are valid.
3. One forced Stats refresh that advances, with `error:false`, `stale:false`, `retryAt:0`, correct units and a bounded clock.

Any failure is Manual and preserves the connection, its key and Homarr's owned resources.

## Status, recovery and disconnect

Setup is Manual when the key is revoked or under-privileged, TLS is untrusted, the endpoint lacks reporting data or Homarr cannot reach it. Fix the cause on the TrueNAS side, then run `self-host setup homarr`. Change a connection with `self-host connections update <id> --file PATH --yes`.

Disconnect needs no credentials or live reads. It releases only recorded owned bindings; a user-created integration or widget with the same endpoint stays untouched, and the remote TrueNAS is never changed.

## Limits and evidence

- `auth.login_with_api_key` is scheduled for deprecation in TrueNAS 28; acceptance is not claimed for it.
- The 24.10 versus 25.04 JSON-RPC start is disputed between sources; use the version your endpoint reports.
- Volatile CPU and memory values are not hardware, network or SMART acceptance.
- Evidence states: source and fake tests only. An approved controlled endpoint, observed `system.info` version and a runtime slot are required before any native claim.
