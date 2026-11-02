# Sonarr traps

## Pitfalls

- Mismatched downloader and library paths break imports and hardlinks.
- Setup creates a qBittorrent download client with category `tv` when that App is ready. Later retries repair only its recorded ID's endpoint and Admin login; categories, tags, enabled state and other settings are preserved. SABnzbd Links are not configured.
- Person-owned or legacy entries matching the qBittorrent host or name remain Manual, not adopted or duplicated. Review `SONARR_LINK_QBITTORRENT_RESOURCE` in the Project's `secrets.env`; uncertain creation or missing recorded resources require inspection. Never assign ownership by name or endpoint. Unlink deletes only the verified recorded ID.
- `/media` and `/downloads` are separate mounts. Imports can copy instead of hardlinking even when the Host folders share a filesystem. A single shared mount would require an explicit path/data migration; Setup does not move libraries or change naming and quality choices.

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
