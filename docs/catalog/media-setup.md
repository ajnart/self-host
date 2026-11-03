# Media Setup

Use this guide when reviewing media folders, downloader categories or quality/naming choices.
It describes boundaries, not an automatic TRaSH profile import or storage migration.
For App implementation, see [Adding an App](adding-apps.md); product-specific recovery lives in [traps/](traps/).

## Shared storage is the default

New Projects default to `MEDIA_LAYOUT=shared` with one `DATA_ROOT` (`~/data`, configurable with `--data-root`):

| Host folder                            | Container path                                  | Purpose                                         |
| -------------------------------------- | ----------------------------------------------- | ----------------------------------------------- |
| `${DATA_ROOT}`                         | `/data` in Sonarr, Radarr and Lidarr            | Common parent for downloads and libraries       |
| `${DATA_ROOT}/torrents`                | `/data/torrents` in cooperating torrent clients | Torrent output at the importers' matching path  |
| `${DATA_ROOT}/usenet`                  | `/data/usenet` in SABnzbd and NZBGet            | Separate `incomplete` and `complete` folders    |
| `${MEDIA_ROOT}` (`${DATA_ROOT}/media`) | `/data/media` in Jellyfin and Bazarr            | Libraries, including `tv`, `movies` and `music` |

Check the root `compose.yml` and each App's configured paths; not every media App uses the same internal mount. Audiobookshelf uses `/data/media` in shared Projects and `/media` in legacy Projects.
Sonarr/Radarr Setup enables hardlink imports and probes temporary links across selected download/library pairs as the App user. Compatible filesystems, mount boundaries and permissions are still required: nested mounts, datasets or subvolumes can prevent links. A host-side probe or successful temporary container link is not proof that an App import used hardlinks; Arr can fall back to copying.

If the person accepts extra writes and disk usage, `--media-copy-fallback` for a new Project or `self-host config set MEDIA_COPY_FALLBACK=on` acknowledges a failed capability probe. Neither option repairs storage or proves an import. Review in-place music tagging before using hardlinks: changing a linked file can also change its seeding source.

## Legacy layouts and ownership

Existing Projects without `MEDIA_LAYOUT=shared` retain their paths. Explicit `--media-root` or `--downloads-root` without `--data-root` also selects the legacy layout for a new Project. Some Apps use separate `${MEDIA_ROOT}:/media` and `${DOWNLOADS_ROOT}:/downloads` mounts; others retain App-local `/data`. Matching downloader paths avoids remote-path translation, but separate download/library mounts cannot provide cross-mount hardlinks or atomic moves, even on one host filesystem. Torrent imports can then occupy duplicate space while seeding.

- Preserve existing libraries, pending jobs and paths. Moving to shared storage requires an explicitly reviewed migration covering files, App records, mounts and recovery; Setup does not perform it or deduplicate old copies.
- Keep completed downloads outside library roots; check the downloader's final path, category override and permissions when imports fail. Avoid remote-path mappings that merely disguise a mismatch.
- The default uses shared effective media UID/GID and umask `022`. Optional separate supported media App users use distinct UIDs, shared PGID and media umask `002`; this is off by default and independent of hardlink capability or the Admin login.
- Separate users rely on selected images' documented identity mechanisms, shared group writes and setgid media directories, while App configuration stays private. Existing ownership, ACLs and Docker namespace mappings need explicit migration; a toggle is not migration, and unsupported mappings are refused. Never apply blanket recursive ownership changes.

## Categories and completion

- Categories isolate App-managed downloads from unrelated queue entries; check both the consuming App's category and the downloader's category save path.
- A category may override the downloader's default directory. Verify the effective completed path is accessible to the consumer.
- Preserve existing categories, seed goals and cleanup choices. Post-import category changes can affect completed-download removal; removing torrents/data needs explicit intent.
- Provider connection success is narrower than a successful import; neither proves hardlinks or populated-library acceptance.

## Quality and naming are user choices

- Review playback hardware, language, storage budget, allowed qualities and upgrade limits before changing a quality profile.
- Preserve existing profiles, custom formats, defaults and per-series/movie assignments. Selecting the first returned profile is not a preference decision.
- Consult current primary guides rather than copying score tables, screenshots or mutable custom-format bundles into Setup.
- Naming should retain useful release/quality information. Preview representative names before approval; changing a naming rule is not approval for a bulk rename.
- Setup imports no quality/custom-format bundles and performs no bulk renames or content acquisition. Any external synchronization or migration needs separately reviewed scope and approval.

## Primary references

Fetched TRaSH web pages may carry a PR-build warning; use the official repository sources below, including their linked includes, rather than treating a preview as authoritative.

- Paths: [TRaSH Docker layout](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/How-to-set-up/Docker.md).
- Categories: [qBittorrent](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Downloaders/qBittorrent/How-to-add-categories.md), [SABnzbd](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Downloaders/SABnzbd/Paths-and-Categories.md).
- Quality profiles: [Sonarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Sonarr/sonarr-setup-quality-profiles.md), [Radarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/radarr-setup-quality-profiles.md).
- Naming: [Sonarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Sonarr/Sonarr-recommended-naming-scheme.md), [Radarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Radarr-recommended-naming-scheme.md).
- Official behavior: [Sonarr settings source](https://github.com/Servarr/Wiki/blob/master/sonarr/settings.md), [Radarr settings source](https://github.com/Servarr/Wiki/blob/master/radarr/settings.md).
