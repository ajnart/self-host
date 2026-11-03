# Bazarr traps

## Pitfalls

- Match Sonarr/Radarr's selected media paths: `/data/media/tv` and `/data/media/movies` for shared layouts, `/media/tv` and `/media/movies` for legacy layouts. Bazarr mounts only the library subtree; subtitles need write access, including shared-group permissions when separate media users are enabled. Matching paths need no path mappings; do not add identity mappings.
- Compose start alone does not establish login or API access; the shared adapter must verify both against the exact private origin.
- Bazarr owns independent Sonarr and Radarr Links. After each installed target finishes its own Setup, a Link can save that target's accepted API key and internal Compose Address in a vacant, disabled Bazarr connection block. Absent targets are not installed or contacted; an unready target leaves a wait on Bazarr. Allocated host ports and selected browser Access are not used for the connection.
- Setup verifies the Admin login and Bazarr API access, not subtitle readiness. Links preserve enabled/disabled services, profiles, providers, synchronization schedules and mappings. They neither enable a backend nor start an initial library synchronization. Choose your own providers and language profiles, and assign profiles to existing media too: defaults apply only to newly added media.
- A saved connection is accepted natively only after Bazarr's own network context reads the selected target's identity, roots and library with the target's key and sees the same roots as self-host's direct read. That read is one bounded, redirect-free, proxy-free GET run inside Bazarr's container (`docker exec` by verified container ID, key passed only through the environment, 1 MiB/8 s bounds, peer re-checked afterwards). It is admitted only for the trusted layout: one Project-owned IPv4 bridge network shared by exactly one replica of each App, the standard LinuxServer image (non-root `abc`, curl 8.4+, GNU timeout, python3), and name resolution returning exactly the target. Admission is a snapshot, not an atomic lease: a hostile or concurrently reconfigured Docker network, other run modes and non-standard images stay **Manual**, as does any failure of the read. Positive configured Bazarr filesystem responses and read-only directory-identity checks remain supporting evidence. No subtitle search, download, provider action, health task or media write is used as proof, and no initial synchronization runs; populated-media and runtime acceptance are separate.

## Connection ownership and recovery

- Bazarr exposes singleton settings blocks, not connection IDs. Each Link records only its actual `settings.sonarr` or `settings.radarr` identity and the five owned connection fields through the Project's private credential boundary. Existing matching endpoints, keys or imported data never prove ownership. Unowned, malformed, duplicated or ambiguous blocks remain Manual and unchanged.
- Connection writes use only Bazarr's supported form settings fields and require a 204 response plus scoped readback. Pending apply/remove records survive uncertain responses, concurrent settings edits and private-storage failures. Retrying Setup does not repeat uncertain writes, replace accepted credentials or adopt current settings. Restore only a known matching ownership record through an explicitly approved Project Backup/Restore; do not blindly reinstall with `--yes` or clear a hold to claim a matching connection.
- Saving connection fields restarts SignalR. An enabled backend may automatically synchronize and enqueue subtitle downloads. Active writes and removal therefore remain Manual: explicitly disable only that backend in Bazarr before retrying. Setup never temporarily changes enable flags or search/defer policy to bypass this boundary.
- Target removal restores only unchanged, recorded owned connection fields and releases ownership after verified restoration; it does not delete Bazarr's imported library or any target data. User-edited fields and the other backend are preserved. If active removal was explicitly requested and the target then disappeared, disabling the backend and running `self-host setup bazarr` retries the recorded cleanup without reinstalling the target. Uncertain removal outcomes stay Manual; a verified completed restoration can finalize its recorded removal without a second settings write.
- Effective roots come from the target's current API, not a replacement default. Shared, legacy and custom roots are checked read-only as the normal LinuxServer `abc` application identity; inaccessible/different directory identities require mount/permission review, never migration or a write-producing hardlink probe. Existing nonempty custom mappings, unsupported identity/image layouts, non-root target API bases, and libraries exceeding the bounded verification limits remain Manual. The current read bound is 30 seconds per Link, 1 MiB per decoded target/settings object, at most 32 roots and 128 library items.

## Security

- Keep the web UI private; the shared first-run adapter configures Basic authentication, but any wider access needs separate review.
- Forms authentication can serve HTTP 200 without an authenticated session. Setup checks the App's saved authentication state before creating a login; existing incompatible credentials or unreadable authentication settings require Manual recovery, never an automatic reset or switch to Basic.
- Store provider and Arr credentials outside catalog and preferences.

Sources: [Bazarr setup](https://wiki.bazarr.media/Getting-Started/Setup-Guide/),
[existing-media profiles](https://wiki.bazarr.media/Getting-Started/First-time-installation-configuration/),
[authentication implementation](https://github.com/morpheus65535/bazarr/blob/master/bazarr/app/ui.py),
[TRaSH setup](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Bazarr/Setup-Guide.md).

Source-backed Link contract, not resolved-image or runtime acceptance:
[settings form API](https://github.com/morpheus65535/bazarr/blob/bfe4593b04caa16eb4a09c2a233777b0bd7723b6/bazarr/api/system/settings.py),
[settings and reconnect side effects](https://github.com/morpheus65535/bazarr/blob/bfe4593b04caa16eb4a09c2a233777b0bd7723b6/bazarr/app/config.py),
[SignalR sync-on-connect](https://github.com/morpheus65535/bazarr/blob/bfe4593b04caa16eb4a09c2a233777b0bd7723b6/bazarr/app/signalr_client.py),
[Sonarr sync can enqueue subtitle downloads](https://github.com/morpheus65535/bazarr/blob/bfe4593b04caa16eb4a09c2a233777b0bd7723b6/bazarr/sonarr/sync/episodes.py),
[filesystem API](https://github.com/morpheus65535/bazarr/blob/bfe4593b04caa16eb4a09c2a233777b0bd7723b6/bazarr/api/files/files_sonarr.py).
