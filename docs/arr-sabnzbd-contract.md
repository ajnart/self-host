# Arr → SABnzbd Link

Sonarr and Radarr API v3 and Lidarr API v1 consume SABnzbd through their normal Setup Links. SABnzbd completes its own Setup first. Absent targets do not schedule a Link; installed unready targets retain a wait on the consumer, and completing target Setup retries the waiting Link. Existing qBittorrent remains independent.

## Native contract

The provider implementation is `Sabnzbd`, display name `SABnzbd`, configuration contract `SabnzbdSettings`, protocol `usenet`. The consumer discovers the exact schema instead of constructing a separate settings framework. New resources use the target's internal Compose service/container port (`sabnzbd:8080`), HTTP without a proxy base path, and the already accepted full `SABNZBD_API_KEY`. Browser Addresses and host-published ports remain separate. The upload-only NZB key is insufficient; no key is rotated or created by this Link.

Sonarr's new category is `tvCategory=tv`, Radarr's is `movieCategory=movies`, and Lidarr's is `musicCategory=music`. An existing owned resource retains its actual category, priority and unrelated settings. A recorded connection edited to another host/port/TLS/base path, disabled or changed to alternative authentication remains Manual, not silently rewritten.

Before every native test, SABnzbd verifies the accepted full key, explicit authenticated queue/history data, the actual category and its effective completed path, scripts and sorters. The consumer checks its configured library roots, existing mappings and accessible directory identity as the App user. A native `/downloadclient/test` succeeds only with the supported explicit empty result; configuration readback alone cannot complete a no-op rerun. No test adds an NZB, changes an indexer or tests a Usenet account.

Before queue or category-list reads, the Link uses targeted `get_config` reads for the raw `download_dir`, `complete_dir` and existing `*` category. Both global folders must already be absolute, supported and accessible directories, checked without writes as SABnzbd's App user. This prevents those reads from reaching directory creation or default-category initialization with absent prerequisites. The Link never uses `fullstatus` to resolve a relative global folder: even `skip_dashboard=1` can create folders. Relative category folders still work beneath a verified absolute completed folder, and existing absolute category folders remain supported.

Remote Path Mappings match the actual download-client resource's host with case-insensitive exact equality, as Arr does. Neither a trailing dot nor the Catalog target's alternative service name is normalized into a match.

## Manual boundaries and recovery

- Run `self-host setup sabnzbd` to recover its existing full key, then `self-host setup sonarr`, `radarr` or `lidarr` as appropriate. Preserve rejected/missing credentials until the target owner has verified them.
- Choose an existing dedicated category in SABnzbd > Config > Categories. The default suggestions contain `audio`, not `music`; missing music remains Manual rather than being substituted or created. Relative category folders are beneath Completed Download Folder; absolute folders replace it. Existing folders are not changed.
- Missing default (`*`) category, missing/inaccessible global download folders, unavailable App-user probes or relative global folders remain specific Manual steps before potentially mutating reads or native tests. Review or restore those prerequisites explicitly; the Link does not initialize categories, create folders or guess a relative-folder base.
- Preserve sorting/scripts and pending jobs. Review a selected category with disabled job folders (`*`), an effective script other than `None`, or active sorting explicitly before retrying. The Link does not disable them for you.
- Choose Lidarr's intended music root and required profiles yourself if none exists. Setup does not choose the first profile or create a library merely to pass verification.
- Shared layouts normally expose Usenet beneath `/data/usenet` and libraries beneath `/data/media`. Stock legacy SABnzbd `/data` and Lidarr `/data` are different App-local folders, while Sonarr/Radarr use `/downloads` and `/media`. Equal path strings are not shared data. Existing compatible folders/mappings can be checked; incompatible storage requires an explicit reviewed arrangement or migration outside this Link.

Specific recovery is persisted in the consumer's Manual step. Directory checks and successful native authentication do not establish a hardlink, atomic move, imported file, populated library or provider-account acceptance.

Read-only directory probes include Linux mount metadata so different bind aliases cannot hide an overlapping backing library. Nested or stacked mounts and unreadable/unsupported metadata remain Manual for explicit storage review. Backing paths are used only for comparison and never emitted in recovery output. See the [Linux mountinfo format](https://man7.org/linux/man-pages/man5/proc_pid_mountinfo.5.html).

## Ownership and lifecycle

The existing private `<CONSUMER>_LINK_SABNZBD_RESOURCE` record stores the positive returned Arr ID, target, endpoint and connection fingerprint. Creation intent is saved immediately before creation; an uncertain response remains Manual without a second POST or adoption by name/endpoint. Reruns keep the owned ID; accepted target credential changes update only that ID. A lost recorded resource or a changed unmasked credential needs explicit recovery.

Removing SABnzbd removes only each consumer's validated recorded connection. It requires neither target authentication nor category/path/native tests, retains same-target manual entries, preserves qBittorrent, and leaves target data intact. No ownership record means no owned deletion. Reinstallation creates a new owned ID; it cannot reclaim a matching manual resource.

## Evidence and limits

Focused Go HTTP/command contracts exercise all three consumer APIs, native tests after prerequisites, both installation orders, absent/unready target behavior, retries, stable IDs, category preservation, negative authentication, Manual boundaries, non-purge removal/reinstallation and an independent qBittorrent survivor. Commands inside those tests are scripted fakes; no Docker or real provider call occurs.

Real image/API versions, native and rendered acceptance, actual App-user filesystem behavior, real credentials and lifecycle must still be checked in the separately approved disposable runtime slot. No live/native/rendered/hardlink/import acceptance is claimed by source or fake results.

Prerequisite checks observe the current running App; they do not lock it against concurrent configuration or filesystem changes. SABnzbd's own Setup and background operations remain separate from this Link's read-only checks.

Primary sources inspected:

- [Sonarr schema](https://github.com/Sonarr/Sonarr/blob/52972e7efcce800560cbbaa64f5f76aaef6cbe77/src/NzbDrone.Core/Download/Clients/Sabnzbd/SabnzbdSettings.cs), [native test](https://github.com/Sonarr/Sonarr/blob/52972e7efcce800560cbbaa64f5f76aaef6cbe77/src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs).
- [Radarr schema](https://github.com/Radarr/Radarr/blob/90cd8df1ae4985387ad1965d95a8bebad5c2bb10/src/NzbDrone.Core/Download/Clients/Sabnzbd/SabnzbdSettings.cs), [native test](https://github.com/Radarr/Radarr/blob/90cd8df1ae4985387ad1965d95a8bebad5c2bb10/src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs).
- [Lidarr schema](https://github.com/Lidarr/Lidarr/blob/350860e524029b7fb4165ed14fbcabb11217ada2/src/NzbDrone.Core/Download/Clients/Sabnzbd/SabnzbdSettings.cs), [native test](https://github.com/Lidarr/Lidarr/blob/350860e524029b7fb4165ed14fbcabb11217ada2/src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs).
- [SABnzbd API](https://sabnzbd.org/wiki/configuration/5.1/api), [categories](https://sabnzbd.org/wiki/configuration/5.1/categories), [category/sorter representation](https://github.com/sabnzbd/sabnzbd/blob/a647f73426cb70ad60ae7964b96c2d5e939b4727/sabnzbd/config.py), [LinuxServer image](https://docs.linuxserver.io/images/docker-sabnzbd/).
- [Arr exact-host mapping](https://github.com/Sonarr/Sonarr/blob/52972e7efcce800560cbbaa64f5f76aaef6cbe77/src/NzbDrone.Core/RemotePathMappings/RemotePathMappingService.cs#L142), [SABnzbd directory getters](https://github.com/sabnzbd/sabnzbd/blob/a647f73426cb70ad60ae7964b96c2d5e939b4727/sabnzbd/config.py#L248-L267), [targeted config lookup](https://github.com/sabnzbd/sabnzbd/blob/a647f73426cb70ad60ae7964b96c2d5e939b4727/sabnzbd/config.py#L779-L822), [default-category initialization](https://github.com/sabnzbd/sabnzbd/blob/a647f73426cb70ad60ae7964b96c2d5e939b4727/sabnzbd/config.py#L1129-L1178).
