# xTeVe traps

## Pitfalls

- Hidden until an image is approved. Every recommended xTeVe image is linux/amd64 only and unmaintained for years; the best candidate, `dnsforge/xteve`, is amd64-only (last tag 2023-02-01). Decision needed: approve an image, or a maintained derived image, to unhide.
- Persistent data is the whole `config` folder (`/home/xteve/conf`: settings, users, playlists, XEPG mapping, tuner `DeviceID`). Keep it on removal, Backup and Restore. `guide2go` is optional tooling state; `/tmp/xteve` is temporary.
- The dnsforge entrypoint installs a Perl module from CPAN on first start, recursively chowns its home, rewrites the persisted `api` flag from `XTEVE_API` (default on), adds a daily playlist filter cron and logs to `/var/log/xteve.log`, which Docker's log limits do not bound. xTeVe auto-update defaults on.
- `streamsActive` counts active channels, not playing streams or used tuners.
- The raw stream counters are omitted when zero. Only a genuine status response (`status`, API and xTeVe versions, EPG source) proves a zero; `null`, wrong types and errors never do.
- API tokens are single-use. Login with API authentication disabled succeeds without a token, so a saved username and password against that mode is a Manual mismatch.
- API disabled returns HTTP 423; authentication rejection can be HTTP 200 with `status: false`.

## Security

- First-run tuner, guide and playlist choices stay yours; Setup never adds a feed, device or media-server connection.
- Never open `/web/` on an empty instance to probe it: the first-run wizard it starts turns web authentication off.
- Use a dedicated API-permission-only user. xTeVe has no read-only role, so API permission also allows update commands. Setup never creates, rotates or disables accounts or authentication.
- Missing credentials are not proof that authentication is disabled; unauthenticated mode must be chosen with the private `auth=none` input.
- Recovery: restore the existing user's credentials into the private inputs, or create a dedicated user in xTeVe yourself, then rerun `self-host setup xteve` and `self-host setup homarr`.
