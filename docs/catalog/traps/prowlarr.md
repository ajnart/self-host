# Prowlarr traps

## Pitfalls

- Indexer availability and acceptable use are external constraints.
- Setup registers installed, ready Sonarr, Radarr and Lidarr Apps using the upstream schema and Full Sync. Schema category defaults, including Sonarr's anime categories, are kept. Full Sync can overwrite the corresponding indexer settings in those Apps; configure categories and Sync Profiles in Prowlarr.
- Registration is not proof that any indexer is configured or usable. Provider selection, accounts and acquisition remain explicit person-owned steps. Prowlarr does not configure or sync download clients into the consuming Apps; its own download client is needed only for direct Prowlarr searches.
- New registrations save their positive returned ID under `PROWLARR_LINK_<TARGET>_RESOURCE` in the Project's `secrets.env`. Retries repair only that ID's internal Addresses and target API key, preserving Sync Level, categories, tags and other settings. Unlink deletes only that verified ID.
- Legacy or person-owned same-target/name entries, an uncertain creation result, a missing recorded resource, or a changed implementation/target remain Manual. Preserve person-owned entries; remove only a confirmed failed self-host creation manually. Clear uncertain ownership only after confirming that creation is absent, then retry Setup. Never adopt an entry by its name or endpoint.
- For limited-API indexers, review per-indexer Query/Grab Limits and a Sync Profile with RSS disabled when appropriate. Setup does not impose TRaSH's optional policies or change existing profiles.

## Security

- Protect indexer credentials and API keys as secrets.

## From docs/prowlarr.md

### Traps

- An entry that merely saved is not a working indexer.
- Bootstrap never chooses a provider based on availability. Without an explicit
  authorized source, indexer sync and acquisition acceptance remain pending.
- Use Docker DNS and internal ports for all application links.

Sources: [Servarr Prowlarr docs](https://wiki.servarr.com/prowlarr),
[application settings and Sync Profiles](https://wiki.servarr.com/prowlarr/settings#applications),
[provider API and connection tests](https://github.com/Prowlarr/Prowlarr/blob/develop/src/Prowlarr.Api.V1/ProviderControllerBase.cs),
[Lidarr application schema](https://github.com/Prowlarr/Prowlarr/blob/develop/src/NzbDrone.Core/Applications/Lidarr/LidarrSettings.cs),
[TRaSH limited-API guidance](https://trash-guides.info/Prowlarr/prowlarr-setup-limited-api/).
