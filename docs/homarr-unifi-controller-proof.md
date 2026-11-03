# UniFi Controller external connection: source contract and safety hold

## Outcome

The exact Homarr kind `unifiController` is an explicit external connection on the
shared external seam. **Native Setup is Manual before every provider request,
credential forwarding or integration creation.** The audited Homarr UniFi client
does not confine login or request redirects and falls back between controller
flavors and ports, so a CLI no-redirect probe cannot establish native
confinement. The hold has no input, environment or version bypass. This is not a
working native integration or runtime acceptance claim.

UniFi is not a Catalog App, Unmanaged app or Compose service. Removal changes only
local configuration and recorded owned Homarr bindings, never the controller.

## Private configuration

```json
{
  "endpoint": "https://unifi.example.invalid:8443",
  "credentials": [
    { "kind": "username", "value": "<dedicated-local-read-only-account>" },
    { "kind": "password", "value": "<private-password>" }
  ],
  "selection": { "flavor": "classic", "siteId": "<site-id>", "siteName": "<site-name>" }
}
```

- Endpoint: trusted HTTPS origin only; no user info, path, query or fragment.
- Credentials: exactly one `username` and one `password`; the only alternative.
  Local non-MFA accounts only; cloud and MFA accounts are unsupported.
- Selection: `flavor` is `unifiOS` or `classic`, plus one explicit site ID and
  name. Homarr aggregates every site the account can see, so the account must
  see only the selected site; verification rejects a summary reporting another.

```sh
self-host connections add unifiController --file /absolute/owner-only-input.json --yes
self-host setup homarr
self-host connections remove <opaque-id> --yes
```

## Surface

One read-only `networkControllerStatus` widget (`content: wifi`) and shared Stats
`wifi` (count), `lan` (count) and `latency` (ms). Verification of the native
summary requires matching integration ID and kind, no error, non-null summary,
recent `updatedAt`, finite non-negative integral client counts and latency.
Legitimate zeros are accepted; VPN users may be null and are not coerced to zero.
Stats need one successful advancing refresh.

## Evidence

| Label | Status |
| --- | --- |
| Source | Input contract, preflight hold, native summary and Stats validators |
| Fake | Unit tests over fixtures in `internal/apps/homarr/target_unifi_controller_test.go` |
| Native | Not run: no approved runtime, peer version or measured value |
| Rendered | Not run |
| Lifecycle | Not run: connect, loss/recovery, endpoint change, disconnect |

## Manual prerequisites

- Approved HTTPS controller flavor/origin with trusted hostname and CA in the CLI
  and Homarr; no TLS or MFA bypass.
- Explicit selected site and a dedicated local account whose visible stats match
  exactly that site.
- Verified minimum read-only permissions, peer version and wan/www/wlan/lan/vpn
  health data, without device or network changes. Do not fall back to Owner.
- An audited native adapter with fail-closed redirect and approved-destination
  behavior before the transport hold can be lifted.
