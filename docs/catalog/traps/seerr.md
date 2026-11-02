# Seerr traps

An interrupted first Jellyfin connection can retain its server identity without
completing library sync or selection. Retry leaves this state Manual: finish
the library selection in Seerr's Setup, then run `self-host setup seerr`.
It does not initialize a partially configured connection or re-enable a
person's disabled libraries.

## Pitfalls

- The first-run flow needs owner-selected media-server credentials, libraries, and Sonarr/Radarr endpoints and API keys; use service DNS rather than localhost between containers.
- Back up existing Overseerr/Jellyseerr data before first start because migration is automatic and forward-only.

## Security

- Keep media-server credentials and Sonarr/Radarr API keys in secret storage; scope public access separately.

## From docs/seerr.md

### Traps

- `localhost` and `127.0.0.1` are invalid internal Docker targets.
- Browser Jellyfin sign-in stays disabled until its required Email Address is
  filled. Do not infer a network failure from that disabled button alone.
- Seerr's Radarr/Sonarr browser forms append help text to the API-key label.
  Use `browser-key refs` for the matching service and `browser-key fill` with
  its fresh ref; the private helper recognizes only the exact service-specific
  label on Seerr setup/service pages.
- If automatic search is declined or a safety review rejects the setting,
  leave it off, do not retry through another route, and report the verifier
  gap. A saved server is not proof that requests can initiate acquisition.
- Rootless Docker remaps container UID 1000. The isolated session creator makes
  only Seerr's nested config mount writable while keeping its parent run private.
- Sonarr/Radarr requests fail unless a default server, quality profile, and root
  folder are all selected.
- Existing Jellyfin connections are checked against their saved service, port,
  TLS/base-path settings and server ID before Setup changes anything. Reruns and
  reconnects preserve disabled libraries, including an entirely disabled selection.
  A new Jellyfin connection still enables its discovered libraries on first Setup.
- A new Sonarr/Radarr Link selects the sole available quality profile, or an exact
  upstream match for the optional `seerr.sonarr_profile` / `seerr.radarr_profile`
  public Inputs. Each accepts a profile ID or unique exact name. Multiple profiles
  without a unique selection remain Manual without creating an entry. Recover with
  `self-host install seerr --set seerr.sonarr_profile=4 --set seerr.radarr_profile=4 --yes`
  using the actual upstream choices; no manual server creation or adoption is needed.
  The registered `/media/tv` or `/media/movies` root must exist; no quality or path
  migration runs.
- New request services retain Seerr's normal request-search behavior and do not
  replace an existing non-4K default. Setup itself submits no acquisition request.
  Existing connection fields, profiles, defaults and search choices are not rewritten.
- Automatic removal requires a recorded nonnegative created ID, matching target and
  service implementation, and unchanged connection identity/API key. Endpoint
  matches alone never confer ownership. Legacy, manually added, changed or
  uncertain entries remain Manual and are never adopted or deleted automatically.
- Seerr assigns its first server of each kind ID `0`; that valid native ID is
  recorded and removable. Missing/null IDs are not zero, and native integer IDs
  are decoded without float rounding. Empty server lists permit normal first Setup.
- An uncertain create/delete result retains an ownership hold rather than retrying
  a mutation. Review the native settings before recovery; do not clear the hold or
  replace credentials blindly. Jellyfin removal has no automatic disconnect/reset.
- API setup is safe to resume only when the existing Jellyfin, Radarr, and
  Sonarr identities match the managed topology. It rejects conflicts rather
  than replacing another server.
- The API key grants admin access. Source it from the session file, never copy it
  into Markdown or screenshots.

Sources: [Docker](https://docs.seerr.dev/getting-started/docker/),
[media server settings](https://docs.seerr.dev/using-seerr/settings/mediaserver/),
[Sonarr/Radarr services](https://docs.seerr.dev/using-seerr/settings/services/),
[general/API key](https://docs.seerr.dev/using-seerr/settings/general/),
[current auth implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/auth.ts),
[current settings implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/settings/index.ts),
[current Sonarr implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/settings/sonarr.ts),
[current Radarr implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/settings/radarr.ts).
