# Bazarr traps

## Pitfalls

- Match Sonarr/Radarr's selected media paths: `/data/media/tv` and `/data/media/movies` for shared layouts, `/media/tv` and `/media/movies` for legacy layouts. Bazarr mounts only the library subtree; subtitles need write access, including shared-group permissions when separate media users are enabled. Matching paths need no path mappings; do not add identity mappings.
- Compose start alone does not establish login or API access; the shared adapter must verify both against the exact private origin.
- The shared adapter does not configure Sonarr, Radarr, subtitle providers, or download and subtitle-path acceptance.
- Setup verifies the Admin login and Bazarr API access, not subtitle readiness. In Bazarr, connect Sonarr at sonarr:8989 and Radarr at radarr:7878 using their API keys, then choose your own providers and language profiles. Assign profiles to existing media too: defaults apply only to newly added media. Existing provider and language choices are never replaced; their absence does not block account Setup.

## Security

- Keep the web UI private; the shared first-run adapter configures Basic authentication, but any wider access needs separate review.
- Forms authentication can serve HTTP 200 without an authenticated session. Setup checks the App's saved authentication state before creating a login; existing incompatible credentials or unreadable authentication settings require Manual recovery, never an automatic reset or switch to Basic.
- Store provider and Arr credentials outside catalog and preferences.

Sources: [Bazarr setup](https://wiki.bazarr.media/Getting-Started/Setup-Guide/),
[existing-media profiles](https://wiki.bazarr.media/Getting-Started/First-time-installation-configuration/),
[authentication implementation](https://github.com/morpheus65535/bazarr/blob/master/bazarr/app/ui.py),
[TRaSH setup](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Bazarr/Setup-Guide.md).
