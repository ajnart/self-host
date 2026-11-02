# Radarr traps

## Pitfalls

- Mismatched downloader and library paths break imports and hardlinks.
- Setup creates a qBittorrent download client with category `movies` when that App is ready. Later retries repair only its recorded ID's endpoint and Admin login; categories, tags, enabled state and other settings are preserved. SABnzbd Links are not configured.
- Person-owned or legacy entries matching the qBittorrent host or name remain Manual, not adopted or duplicated. Review `RADARR_LINK_QBITTORRENT_RESOURCE` in the Project's `secrets.env`; uncertain creation or missing recorded resources require inspection. Never assign ownership by name or endpoint. Unlink deletes only the verified recorded ID.
- `/media` and `/downloads` are separate mounts, so imports can copy instead of hardlinking. Setup does not migrate to a shared mount or change naming and quality choices.

## Security

- Treat API keys as secrets and keep the management UI private.

## From docs/radarr.md

### Traps

- Keep a distinct qBittorrent category for movies. New managed clients use `movies`; an existing client's chosen category is preserved.
- Never use host ports or `localhost` on the Compose network.
- Re-snapshot after every modal change and test before save.

Sources: [Servarr Radarr docs](https://wiki.servarr.com/radarr),
[LinuxServer image docs](https://docs.linuxserver.io/images/docker-radarr/),
[provider API and connection tests](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/ProviderControllerBase.cs),
[TRaSH Docker paths](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Docker/).
