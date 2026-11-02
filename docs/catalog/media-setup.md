# Media Setup

Use this guide when reviewing media folders, downloader categories or quality/naming choices.
It describes boundaries, not an automatic TRaSH profile import or storage migration.
For App implementation, see [Adding an App](adding-apps.md); product-specific recovery lives in [traps/](traps/).

## Shared paths, not hardlinks

The shared layout uses Host settings `${MEDIA_ROOT}` and `${DOWNLOADS_ROOT}`:

| Host folder         | Container path | Purpose                                                                      |
| ------------------- | -------------- | ---------------------------------------------------------------------------- |
| `${MEDIA_ROOT}`     | `/media`       | Organized libraries, including Sonarr `/media/tv` and Radarr `/media/movies` |
| `${DOWNLOADS_ROOT}` | `/downloads`   | Downloader output, visible at the same path to consuming Apps                |

These are separate bind mounts. Matching `/downloads` across Apps permits imports without a remote-path translation; it does **not** enable hardlinks across `/downloads` and `/media`.
Imports across these mounts copy data; torrent imports retain the source for seeding and can consume duplicate space.
Enabling Sonarr/Radarr's hardlink setting alone cannot change that mount boundary.

- Keep completed downloads separate from library roots; check the downloader's reported final path, category override and permissions when imports fail.
- Use shared PUID/PGID/UMASK consistently; do not repair permissions by recursively changing a person's library without review.
- Check each App's actual Compose and existing library settings: mounts alone do not create or import libraries, and legacy App-local paths may still be in use.
- A common-parent layout can support hardlinks/atomic moves only with compatible mounts and filesystem support. It requires an explicitly reviewed migration plan, backup/recovery, and coordinated downloader/library paths.
- Preserve existing paths and user libraries by default. Do not silently replace `/media`, `/downloads` or legacy mounts with `/data`, move files, or add remote-path mappings to disguise a mismatch.

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
- Keep guide synchronization or automated imports outside routine Setup unless separately reviewed and explicitly requested.

## Primary references

Fetched TRaSH web pages may carry a PR-build warning; use the official repository sources below, including their linked includes, rather than treating a preview as authoritative.

- Paths: [TRaSH Docker layout](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/How-to-set-up/Docker.md).
- Categories: [qBittorrent](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Downloaders/qBittorrent/How-to-add-categories.md), [SABnzbd](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Downloaders/SABnzbd/Paths-and-Categories.md).
- Quality profiles: [Sonarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Sonarr/sonarr-setup-quality-profiles.md), [Radarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/radarr-setup-quality-profiles.md).
- Naming: [Sonarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Sonarr/Sonarr-recommended-naming-scheme.md), [Radarr](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Radarr-recommended-naming-scheme.md).
- Official behavior: [Sonarr settings source](https://github.com/Servarr/Wiki/blob/master/sonarr/settings.md), [Radarr settings source](https://github.com/Servarr/Wiki/blob/master/radarr/settings.md).
