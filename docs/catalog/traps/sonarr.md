# Sonarr traps

## Pitfalls

- Mismatched downloader and library paths break imports and hardlinks.
- Setup creates a qBittorrent download client with category `tv` when that App is ready. Later retries repair only its recorded ID's endpoint and Admin login; categories, tags, enabled state and other settings are preserved. SABnzbd Links are not configured.
- Person-owned or legacy entries matching the qBittorrent host or name remain Manual, not adopted or duplicated. Ownership is recorded as `SONARR_LINK_QBITTORRENT_RESOURCE` through the Project secret boundary; uncertain creation or missing resources require review through `self-host debug sonarr`, not direct env-file inspection. Never assign ownership by name or endpoint. Unlink deletes only the verified recorded ID.
- Shared layouts mount one parent at `/data`, with `/data/torrents` and `/data/media/tv`. Setup enables hardlink imports and probes temporary links as the App user; this is capability evidence, not proof of an import. Legacy `/media` and `/downloads` mounts remain separate and cannot provide cross-mount hardlinks. Existing paths, naming and quality choices stay unchanged; migration requires explicit review. See [Media Setup](../media-setup.md).

## Security

- Treat API keys as secrets and keep the management UI private.

## From docs/sonarr.md

### Traps

- Never use `localhost` for container-to-container links.
- Browser refs are ephemeral. Snapshot after opening every modal/dropdown.
- The download-client chooser can be unlabeled or reorder tiles. Confirm the
  opened dialog title includes `qBittorrent`; a wrong tile can produce a
  misleading connection-test failure.
- A saved form is not proof; run its connection test and inspect the list view.

Sources: [Servarr Sonarr docs](https://wiki.servarr.com/sonarr),
[LinuxServer image docs](https://docs.linuxserver.io/images/docker-sonarr/),
[provider API and connection tests](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/ProviderControllerBase.cs),
[masked provider credentials](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Http/ClientSchema/SchemaBuilder.cs),
[TRaSH Docker paths](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Docker/).
