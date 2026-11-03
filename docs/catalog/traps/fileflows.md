# FileFlows traps

## Pitfalls

- Flows can rewrite or replace library files; test on copies and keep recoverable originals before enabling a library.
- First run shows an EULA. Setup never sets `FF_EULA_ACCEPTED`; the owner reviews and accepts it in the web console.
- Setup only proves the web console answers on `/`. It does not touch `/api`, enable the API, or write licence, user or library settings.
- `./data` holds the SQLite database, users and flows, `./logs` the logs and `./temp` scratch space only. Reinstall and Setup preserve all three; no media folder is mounted, so add library paths deliberately.
- The `stable` image tag is the tested channel; `latest` can be experimental.
- The free tier is personal use only and sends telemetry; commercial or organizational use needs a licence. No account or licence action is taken for you.
- Web-console security defaults to none. Forms or OIDC login are licensed features, and users must exist before security is enabled.

## Homarr

- No native Homarr integration ships yet. Homarr's `fileflows` kind probes the unauthenticated `GET /api/status`, but current FileFlows serves `/api` only when licensed, enabled and HMAC-signed, so that probe answers 404 or 401. The token-protected `/webhook/fenrus` endpoint uses different keys and is not supported by Homarr.
- Hold: wiring waits for either an explicit Manual hold or separately verified compatibility. No downgrade, auth bypass or custom bridge is acceptable.

## Security

- Keep the console on loopback or a separately approved private route until security and users are configured.
- Nodes, Agents, schedules, GPU or device access and media mounts stay owner-controlled and are not part of the recipe.

## Source

- [Docker installation](https://fileflows.com/docs/installation/docker/)
