# SABnzbd traps

## Pitfalls

- Provider access and retention are external operational dependencies.
- Setup secures the WebUI login and finishes OK without a Usenet provider, noting what is left. Add your own authorized provider in Config > Servers; Setup does not create or test provider accounts.
- A first-run wizard is not API failure: readiness uses the version API, then verifies authenticated queue/history. Explicit empty arrays and zero download rate are valid.
- Setup saves the existing full API key as `SABNZBD_API_KEY` through the Project secret boundary. The NZB upload-only key cannot read queue/history. Retry Setup to recover a missing saved key without rotation.
- The source-verified Homarr 2.4.1/SABnzbd 5.1.3 contract and pending runtime acceptance are recorded in [the SABnzbd Link contract](../../homarr-sabnzbd-contract.md).
- `${DOWNLOADS_ROOT}` is mounted at `/downloads`; the existing `./media-data:/data` mount is retained. Setup does not migrate files or rewrite download paths, sorting or categories.
- For new downloads, choose separate Temporary and Completed Download Folders in Config > Folders, for example `/downloads/incomplete` and `/downloads/complete`. In Config > Categories, set the folder for `tv`, `movies` and `music` to the same relative name; relative folders live beneath the completed folder. Match the consuming App's category and save each category row. Review existing paths and pending jobs before changing anything; these are recommendations, not automatic settings or downloader Links.
- `PUID`, `PGID` and `UMASK` follow Host settings. An explicit Permissions for completed downloads setting can override the process umask; Setup does not change it. Separate download/media mounts do not guarantee hardlinks or atomic moves, even on the same host filesystem.
- Sources: [SABnzbd folders](https://sabnzbd.org/wiki/configuration/5.1/folders), [relative category folders](https://sabnzbd.org/wiki/configuration/5.1/categories), and [LinuxServer mounts and UMASK](https://docs.linuxserver.io/images/docker-sabnzbd/).

## Security

- Treat provider credentials, server configuration, and the SABnzbd API key as secrets.
- Keep its WebUI private; the API key is accepted as a query parameter by the upstream API.
- Homarr SABnzbd credential-safety hold #363: the pinned native adapter sends the full API key in the request query. A rejected response can retain that Address into downstream logs. Homarr Link Check/apply must remain Manual before any request or mutation until a supported adapter and complete logging-chain fix are verified in the actual supported image. Do not manually activate the integration or bypass the hold; standalone SABnzbd Setup and owned Link removal stay separate.
