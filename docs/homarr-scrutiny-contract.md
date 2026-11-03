# Homarr and external Scrutiny

Connects an existing Scrutiny web/collector endpoint to Homarr as the exact `scrutiny` kind with one read-only Stats widget (`passed`, `failed`, `unknown`; unit `count`). External only: there is no Scrutiny App, Compose, image or Catalog entry.

The counts are devices that pass Scrutiny's configured threshold, not an independent disk-health assurance.

## Connection

- The endpoint is an approved `http(s)` URL, optionally with a base path; no credentials, query or fragment. The kind takes no secret and no selection.
- Self-host reads `/api/settings` and `/api/summary` without following redirects. An endpoint behind authentication or a redirecting proxy is Manual: never disable authentication or bypass TLS.
- Removing the connection releases only Homarr bindings this connection owns and never contacts or changes Scrutiny.

## Prerequisites (Manual when absent)

- Both responses report `success: true`; the device-status threshold is 1, 2 or 3.
- At least one non-archived device. Archived devices are ignored.
- Every non-archived device has a stored SMART sample (`smart.collector_date`) whose identity matches its summary key (`scrutiny_uuid`, or legacy `wwn`).
- Sample age: no older than 48 hours and no more than 5 minutes in the future. This is product policy, not an upstream guarantee.
- Absent samples are a prerequisite, never a passed disk. Self-host never mounts devices, grants privileges, triggers SMART tests or installs collectors.

## Counts

Device status 0 passes; 1 is a SMART failure, 2 a Scrutiny failure, 3 both. Threshold 3 counts statuses 1-3 as failed; threshold 1 or 2 counts that status and 3. A status outside 0-3 is `unknown`.

## Recovery

Fix the prerequisite on the Scrutiny side, then run `self-host setup homarr`. Preserve the connection; reconnect only by removing and adding it.
