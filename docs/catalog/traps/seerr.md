# Seerr traps

An interrupted first Jellyfin connection can retain its server identity without
completing library sync or selection. Retry leaves this state Manual: finish
the library selection in Seerr's Setup, then run `self-host setup seerr`.
It does not initialize a partially configured connection or re-enable a
person's disabled libraries.

## Selecting Plex or Emby

- Choose through the existing public Input: `self-host install seerr --set seerr.media_server=emby --yes` (or `plex` / `jellyfin`), then retry `self-host setup seerr`. An already configured backend wins; a conflicting input remains Manual until separately reviewed migration. Changing this input does not migrate users, requests, credentials or libraries.
- An empty choice preserves a configured backend. Fresh single-Jellyfin installs retain their existing default; fresh Plex/Emby or multiple media servers require an explicit choice. Unselected Links are excluded before target readiness, so an unready unrelated server does not become a wait on Seerr. Installing a target later uses the normal Setup/Link retry scheduler.
- Complete Plex's server claim/account ownership and Seerr's own Plex authorization yourself. They are separate prerequisites; neither the temporary claim nor the Project's PMS token is submitted to plex.tv by this Link. Emby administrator/account prerequisites and initial library selection likewise stay in Seerr's `/setup`. New Plex/Emby Links perform no provider authentication, settings mutation, library sync, library enable/disable, initialize, job, scan, import or request.
- Emby uses Seerr's `/api/v1/settings/jellyfin` contract with backend type 3, not an invented Emby endpoint. The Link verifies Seerr's saved server ID against authenticated Emby identity using Seerr's existing connection key, matches saved native library IDs/types to authenticated folders, then requires a valid user read through Seerr's own `/settings/jellyfin/users` connection. Missing/rejected keys, stale IDs, missing flags and interrupted Setup stay actionable Manual recovery. Existing mixed enabled/disabled and initialized all-disabled choices remain unchanged.
- Plex uses backend type 1 and `/api/v1/settings/plex`. The target-owned read-only action verifies the existing account-ownership assertion, stored PMS token, claimed stable machine ID and library section IDs. This does **not** prove Seerr's separately stored Plex authorization works: Plex's Link explicitly remains Manual rather than calling a mutating settings test, library sync or provider account endpoint to manufacture a passing check. Verify the existing connection in Seerr; retrying still reports this read-only verification limit truthfully.
- `SEERR_BACKEND_IDENTITY` is a private record of observed backend/server/endpoint identity, not adoption or deletion ownership. Reruns retain it; changed or incomplete records require review. Removing the selected or historically recorded Plex/Emby target remains Manual because the verified upstream contract has no safe connection-only disconnect. The connection, record, selected libraries, users and requests are retained. Removing an unrelated target does not clear another backend, and reinstall never resets identity or reselects libraries.
- These are source/fake-backed checks, not live/native acceptance, media playback, populated-library or successful acquisition evidence. In particular, cached settings alone never complete the Plex Link. No new key is generated and Homarr is not required for these Seerr Links.

Sources: [Seerr v3.5.1](https://github.com/seerr-team/seerr/releases/tag/v3.5.1),
[backend constants](https://github.com/seerr-team/seerr/blob/564bfb08b54caa374bddaf820abfd226e5e69151/server/constants/server.ts),
[settings, libraries and initialize](https://github.com/seerr-team/seerr/blob/564bfb08b54caa374bddaf820abfd226e5e69151/server/routes/settings/index.ts),
[authentication side effects](https://github.com/seerr-team/seerr/blob/564bfb08b54caa374bddaf820abfd226e5e69151/server/routes/auth.ts),
[Emby/Jellyfin inventory mapping](https://github.com/seerr-team/seerr/blob/564bfb08b54caa374bddaf820abfd226e5e69151/server/api/jellyfin.ts),
[Plex inventory mapping](https://github.com/seerr-team/seerr/blob/564bfb08b54caa374bddaf820abfd226e5e69151/server/api/plexapi.ts).

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
  The registered root must exist: `/data/media/tv` or `/data/media/movies` for
  shared layouts, `/media/tv` or `/media/movies` for legacy layouts. No quality or
  path migration runs.
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
- The API key grants admin access. Use `self-host credentials` privately when
  needed; never read session/env files or copy credentials into Markdown or screenshots.

Sources: [Docker](https://docs.seerr.dev/getting-started/docker/),
[media server settings](https://docs.seerr.dev/using-seerr/settings/mediaserver/),
[Sonarr/Radarr services](https://docs.seerr.dev/using-seerr/settings/services/),
[general/API key](https://docs.seerr.dev/using-seerr/settings/general/),
[current auth implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/auth.ts),
[current settings implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/settings/index.ts),
[current Sonarr implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/settings/sonarr.ts),
[current Radarr implementation](https://github.com/seerr-team/seerr/blob/dca1af6af26312ec13ffb4920e5dd948049962ad/server/routes/settings/radarr.ts).
