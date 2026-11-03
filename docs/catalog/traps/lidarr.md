# Lidarr traps

## Pitfalls

- Path mismatches and music metadata availability can prevent reliable imports.
- Lidarr uses `/api/v1`, unlike Sonarr/Radarr's `/api/v3`. Setup configures Forms login and links a ready qBittorrent App through the API v1 schema with `musicCategory` set to `music` for a newly created client. Prowlarr registers a ready Lidarr App through its application schema. SABnzbd Links remain deferred; music roots, import behavior and restart persistence still need direct acceptance.
- Shared layouts mount `${DATA_ROOT}` at `/data`; choose a music root such as `/data/media/music` explicitly. Legacy App-local `/data` and any existing shared-folder mounts remain in use until an explicit migration. Setup neither moves files nor rewrites existing roots. Compatible common-parent mounts can support hardlinks; separate download/library mounts cannot provide cross-mount hardlinks or atomic moves. In-place tagging of a hardlinked music file can alter its seeding source. See [Media Setup](../media-setup.md).
- qBittorrent ownership is recorded as `LIDARR_LINK_QBITTORRENT_RESOURCE` through the Project secret boundary. Only the verified recorded ID is repaired or deleted, preserving categories, tags, enabled state and other settings. Legacy/person-owned same-host/name entries and uncertain or missing ownership remain Manual; use `self-host debug lidarr` for recovery hints rather than reading env files or assigning ownership by name or endpoint.
- The Homarr Link checks authenticated Lidarr 3.x API v1, artist monitoring data and the selected calendar source. Its shared Stats callback validates one advancing forced sample: integral nonnegative `artists`, `monitored` and `upcoming` in `count` units, with monitored artists bounded by artists. Native calendar/library observations are independent and are not compared with refreshed Stats. Empty results and nullable calendar subtitles remain valid. Source and focused HTTP fixtures do not establish rendered or lifecycle acceptance.

- The Homarr Link probes the App-owned readiness before any Homarr request. Unsupported versions/API contracts and external authentication retain retryable Manual classification and the owner's recovery hint; rejected credentials remain failed Setup. This catches policy drift even when a previous Setup was recorded ready.

## Security

- Treat API keys as secrets and keep management access private.

Sources: [Lidarr API v1 download clients](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/DownloadClient/DownloadClientController.cs),
[qBittorrent music category schema](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/Download/Clients/QBittorrent/QBittorrentSettings.cs),
[LinuxServer Lidarr media paths](https://docs.linuxserver.io/images/docker-lidarr/),
[TRaSH Docker paths](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Docker/).
