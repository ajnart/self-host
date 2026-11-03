# iCal external connection: Setup and recovery

**Native Setup is Manual before any feed request, secret submission or Homarr
mutation.** The inspected Homarr iCal adapter keeps private feed paths in
status errors, can log short capabilities, follows unguarded redirects and reads
feeds without a size bound. No input, environment or version-number bypass
exists. This is not a working integration or runtime acceptance claim.

iCal is an explicit external connection, never a Catalog App. Only local
configuration and recorded owned Homarr bindings are managed. Removal never
touches the remote feed or its data.

## Private configuration

Use an owner-only JSON file for an already-authorized, read-only feed. The
`endpoint` is the feed's trusted-HTTPS origin only; the full feed address,
including path, query and token, is the private `url` credential on the same
origin. There is no selection.

```json
{
  "endpoint": "https://calendar.example.invalid",
  "credentials": [{ "kind": "url", "value": "<private-full-feed-address>" }]
}
```

```bash
self-host connections add ical --file PATH --yes
```

The feed address stays in the Project's private store and never appears in
public output.

## Recovery

Keep the feed private and the connection saved. Retry
`self-host setup homarr` only after a reviewed Homarr update fixes iCal
URL/error redaction and fetch restrictions. Do not publish the feed, disable
TLS checks, or create a replacement integration.

Disconnect with `self-host connections remove <id> --yes`; it releases only the
recorded Homarr bindings and needs no feed or Homarr request.
