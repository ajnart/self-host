# Oxynote traps

## Pitfalls

- The image runs the whole product (web, API, realtime and an embedded PostgreSQL 18) as UID 65532. `oxynote-init` hands `./data` to that user before each start.
- `OXYNOTE_PUBLIC_URL` is the only browser origin Oxynote supports: it fixes the cookie domain, trusted origin, absolute API calls and WebSockets. self-host keeps it on the Home network `IP:port` Address, or `127.0.0.1:port` under This computer only. Easy names, Tailscale, Cloudflare and Bring your own Access are withheld for this App, even when installed with remote Access or when Access changes later. The other Apps keep their normal Addresses. A proxy header rewrite cannot make simultaneous origins work.
- A standalone deployment can choose a different single origin by changing `OXYNOTE_PUBLIC_URL` and restarting. Every user must then use that exact Address; the numeric Address is not an authenticated fallback for a named origin. Keep the generated origin when using self-host; changing it by hand also needs corresponding Setup and navigation changes and is not managed automatically.
- The first start creates one workspace with the admin `admin@example.com` and the password `oxynote-admin-1234`, shown on the login page until it changes. Setup signs in with it and sets the Admin password through `/auth-realtime/api/auth/change-password`. Oxynote requires 16 to 128 characters, so a shorter Admin password stops Setup before anything changes.
- Changing the admin's email address in Oxynote stops `self-host check oxynote` from signing in.
- Sign-in allows three attempts in ten seconds and answers 429 with `X-Retry-After`.
- Without an email sender, invitation links are copied by hand from the workspace settings and password reset is off. `OXYNOTE_SMTP_DSN` and `OXYNOTE_EMAIL_FROM_ADDRESS` turn email on.
- The MCP server needs an `https://` origin or a loopback host, so it stays off on a plain `http://` LAN address.
- Shutdown flushes open documents last, so the service keeps a 60 second stop grace period.

## Security

- `./data` holds the database, the generated secrets (session signing, data-source credential encryption) and uploads. Losing it signs everyone out and orphans stored data-source credentials; back it up whole.
- Workspace members can point data sources at any address the container reaches, including other Apps in the Project.

## Upstream sources

- [Production configuration](https://github.com/oxynote/oxynote/blob/7f96f0297055c17ff6e68dcc7695bf5c0a58ddad/docker/prod/README.md#public-address) and [launcher mapping](https://github.com/oxynote/oxynote/blob/7f96f0297055c17ff6e68dcc7695bf5c0a58ddad/docker/prod/launcher/src/mapping.ts), checked 2026-11-01. The launcher builds component environments from one origin; component-level overrides are not supported by the image.
