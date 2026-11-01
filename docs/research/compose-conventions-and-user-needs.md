# Compose conventions and firsthand user needs

Researched 2026-11-01 at repository revision `865e7a0b`. Three research agents
covered media storage/permissions, ordinary Compose operation, and original
community reports. Agents were read-only; the parent saved this synthesis.
No Docker, tests, browser, installer, account changes or public posts ran.

## Accepted direction and evidence limits

The user chose agent-assisted users as the primary audience, one authoritative
Compose YAML used by both self-host and ordinary Docker Compose, and
hardlink-compatible defaults with an explicitly acknowledged copy fallback.
See the [interview record](compose-hardlinks.md#interview-record).

The intended result supports ordinary `docker compose up -d`, `down`, `pull`,
`logs`, `ps` and related commands from its folder without self-host or special
shell preparation. One YAML can still reference configuration, credentials and
persistent data. It contains the selected Apps and their required resources;
it does not imply installing the entire Catalog.

Separate four kinds of evidence: the Compose specification, image-specific
requirements, author recommendations such as TRaSH, and user preferences.
There is no universal Docker rule prescribing one file, PUID/PGID or a umask.
The public reports below are a small qualitative sample, not new interviews or
a measurement of demand for this product. Recommendations remain proposals
unless explicitly recorded as accepted.

## Media storage and permissions

The current product choice separates performance from ownership: hardlink-friendly
storage is the baseline; separate media App users are optional and default off.
Use one effective media UID/GID with `022` by default; the option uses supported
per-App identities with a shared group and `002`. Settings and media-relevant
Onboarding should expose the same persisted choice. Existing ownership changes
in either direction need migration, not just a changed umask.

TRaSH and Servarr recommend a shared data tree, with App configuration outside
it. The host location is chosen deliberately; `/data` is the common path inside
the containers. [TRaSH layout](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Docker/),
[Servarr Docker guide](https://wiki.servarr.com/docker-guide).

```text
<data-root>/
  torrents/{tv,movies,music}
  usenet/incomplete/
  usenet/complete/{tv,movies,music}
  media/{tv,movies,music}
```

| Rule | Reason and repository implication |
| --- | --- |
| Importing Apps receive one `<data-root>:/data` mount. | Sonarr/Radarr perform the link or atomic move. Their current separate `/media` and `/downloads` mounts prevent it (`internal/apps/arr/arr.go:94`). |
| Downloaders receive narrower subtrees at matching internal paths. | qBittorrent can use `/data/torrents`, SABnzbd `/data/usenet`; Arr must see the same paths. Current qBittorrent Setup forces `/downloads` (`qbittorrent.go:166`); SABnzbd uses App-local data (`sabnzbd.go:49`). |
| Verify the actual source-to-library pair. | Matching path names and writable folders are insufficient. Probe inside the importing container as its application user. Distinguish a successful capability probe from an observed Arr import. |
| Select an ownership model deliberately. | TRaSH recommends per-App users, a shared media group and `002`; it also documents one shared user with `022` as a simpler valid setup. Current owner IDs plus `022` match the simpler model (`internal/service/service.go:113`). |
| Preserve existing content and ownership until explicit migration. | Changing a default download path does not move torrents; adding an Arr root does not rewrite existing series/movie records. Update refreshes unmodified fragments, so a new mount must not silently strand old paths. |

These recommendations follow
[TRaSH hardlinks and permissions](https://trash-guides.info/File-and-Folder-Structure/Hardlinks-and-Instant-Moves/).
They address filesystem and access constraints, not a mandatory Compose style.
Hardlinks share file contents: in-place music tagging can alter the seeding
file too, so music workflows need separate consideration.

Important qualifications:

- **Image identity:** PUID/PGID and `user:` are different mechanisms. LinuxServer's
  current non-root guidance limits support to tested images and says PUID/PGID
  do not apply in that mode. Its older PUID article is broader and conflicts
  with that newer qualified support. Follow current per-image documentation;
  the official Jellyfin image and the repository's LinuxServer image have
  different contracts. [LinuxServer non-root](https://docs.linuxserver.io/misc/non-root/),
  [official Jellyfin image](https://jellyfin.org/docs/general/installation/container/).
- **Shared groups:** setgid directories preserve group inheritance; umask and
  file modes still determine group write access. Do not solve this by changing
  all existing media recursively. [GNU directory setgid semantics](https://www.gnu.org/software/coreutils/manual/html_node/Directory-Setuid-and-Setgid.html).
- **Rootless Docker:** container UID 0 maps to the daemon's host user; positive
  IDs usually map to subordinate IDs. Preserve the repository's existing
  rootless distinction (`service.go:122`). [Docker UID/GID mapping](https://docs.docker.com/engine/security/rootless/uid-gid-mapping/).
- **Storage boundaries:** separate ZFS filesystem datasets and Btrfs subvolumes
  can prevent links despite a common parent path. mergerfs supports links with
  branch/policy restrictions, so one probe is not a pool-wide guarantee.
  [OpenZFS](https://openzfs.github.io/openzfs-docs/man/master/7/zfsconcepts.7.html),
  [Btrfs](https://btrfs.readthedocs.io/en/stable/mkfs.btrfs.html),
  [mergerfs maintainer guidance](https://trapexit.github.io/mergerfs/latest/faq/technical_behavior_and_limitations/).
- **Read-only media:** appropriate for playback when enabled features allow it;
  configuration/cache remain writable. Bazarr writes subtitles and must not
  receive a blanket read-only mount. Feature-specific checks matter more than
  container health alone. [Jellyfin container storage](https://jellyfin.org/docs/general/installation/container/).

## Requirements for ordinary Compose operation

| Priority | Requirement | Primary source / repository gap |
| --- | --- | --- |
| Required by the chosen contract | One default-discovered YAML with all selected services and required resources, without included per-App YAML or wrapper dependence. | [Compose application model](https://docs.docker.com/compose/intro/compose-application-model/). Root currently writes includes (`internal/project/project.go:510`). |
| Required for existing installations | Write the effective Project name and preserve service/storage identities. | [Top-level name](https://docs.docker.com/reference/compose-file/version-and-name/), [volume names](https://docs.docker.com/reference/compose-file/volumes/). Root currently writes `selfhost`, while CLI may use an override (`project.go:527`, `service.go:70`). |
| Required | Rebase each App's relative bind/config paths without changing the referenced data. | [Volume syntax](https://docs.docker.com/reference/compose-file/services/#volumes). Each App's `./config` means a different directory today; flattening raw fragments would collide. |
| Required | Preserve configuration, credentials and generated prerequisites. | Install writes files before starting containers (`internal/service/install.go:408`, `:523`). Plain YAML does not reproduce API Setup or Links. |
| Required | Resolve interpolation in a fresh shell while keeping credentials private. | Current CLI explicitly loads both environment files (`internal/compose/compose.go:51`). See the unresolved design constraint below. |
| Recommended | Prefer service DNS and container ports; avoid unnecessary fixed container names. | [Compose networking](https://docs.docker.com/compose/how-tos/networking/), [container_name limitations](https://docs.docker.com/reference/compose-file/services/#container_name). DCM adds fixed names (`tools/sitegen/standalone/compose.go:174`). |
| Recommended | Omit obsolete `version:`; use a deliberate restart policy for long-running Apps. | [Version/name](https://docs.docker.com/reference/compose-file/version-and-name/), [restart policies](https://docs.docker.com/engine/containers/start-containers-automatically/). Media Apps already use `unless-stopped`. |
| Recommended | Bound local logs with per-service configuration, preserving intentional user choices. | Docker recommends the rotating [local driver](https://docs.docker.com/engine/logging/configure/); bounded [json-file options](https://docs.docker.com/engine/logging/drivers/json-file/) are another choice. Current media definitions omit log limits. |
| Conditional | Use `depends_on` for real startup requirements; use meaningful health checks when readiness matters. | [Startup order](https://docs.docker.com/compose/how-tos/startup-order/). A Link does not automatically imply a container startup dependency. |
| Image-specific or optional | Version/digest pinning, `user`, `_FILE`, read-only roots, capabilities, devices/GPU and limits. | Verify the selected image's contract; these are not universal defaults to apply throughout the Catalog. |

The ordinary runbook should explain that `pull` downloads images and an update
also needs `up -d`. Plain `down` normally removes containers/networks; retaining
App data requires the correct persistent mounts/volumes. Do not silently change
volume lifecycle ownership by marking everything external.
[Docker pull](https://docs.docker.com/reference/cli/docker/compose/pull/),
[Docker down](https://docs.docker.com/reference/cli/docker/compose/down/).

### Secret loading needs an explicit design choice

Current [AGENTS.md](../../AGENTS.md) requires credentials to live only in the
Project's `secrets.env`. `.env` stores non-secret Host settings, while the CLI
passes both files using `--env-file`.

Service `env_file: secrets.env` supplies the container environment, not Compose's
`${...}` interpolation. It could also disclose unrelated Project credentials to
every service receiving it. An `environment:` entry overrides `env_file`, even
when empty. [Docker env_file](https://docs.docker.com/reference/compose-file/services/#env_file).

Putting `COMPOSE_ENV_FILES=.env,secrets.env` inside `.env` is not a reliable
bare-command solution. Docker maintainers declined that behavior in
[issue #11122](https://github.com/docker/compose/issues/11122); current flag and
loader ordering corroborates the limitation.
[Compose source](https://github.com/docker/compose/blob/main/cmd/compose/compose.go),
[project loader](https://github.com/docker/compose/blob/main/internal/projectloader/loader.go),
[compose-go env loading](https://github.com/compose-spec/compose-go/blob/main/cli/options.go).

Compose secret `file:` supplies a whole file, not one selected dotenv key;
`environment:` still needs its source value loaded. `_FILE` support belongs to
the image. [Secret sources](https://docs.docker.com/reference/compose-file/secrets/).

A conventional private `.env` alongside the authoritative Compose file is now
accepted by the user. It revises the current secrets convention for the new
layout; existing persisted formats still need compatible migration. No
credentials have been read or moved. A symlink workaround is not selected.

### One authoritative file is the selected architecture

self-host will install, configure, add, update and remove Apps through the same
root Compose file that ordinary Compose uses. The user explicitly rejected the
research's earlier separate-export and transfer-of-management proposals.
App configuration, data and API-based Setup/Links still need to be preserved.

DCM demonstrates combined generation but its converter is not a safe replacement
for the live Project model: it rewrites paths, changes Access bindings,
converts/drops secret handling, adds fixed container names and leaves Links
manual (`tools/sitegen/standalone/compose.go:128`, `:145`, `:174`, `:196`, `:304`;
`notes.go:27`).

The selected architecture changes discovery, hashing, refresh, removal and backup
behavior. [ADR 0014](../adr/0014-one-editable-compose-file.md) supersedes
ADR 0001's per-App include layout. Changes to one App must preserve unrelated
services, manual edits and comments. Comments can group Apps and carry notes;
the parser should use YAML structure and explicit ownership information.

## Firsthand user reports

These are accounts of particular experiences, not verified diagnoses of every
product or estimates of how common each problem is.

| Date | Original evidence | Implication to evaluate |
| --- | --- | --- |
| 2025-05-10 | A Sonarr user reported twelve hours of hardlink troubleshooting; a nested TrueNAS dataset explained duplicate copies despite read/write access and a common parent path. [Report](https://www.reddit.com/r/sonarr/comments/1kjn0ex/hardlinking/) | Detect storage capability rather than approving a directory diagram. |
| 2025-12-04 | A user with working hardlinks and separate App users was confused that imports retained downloader ownership. [Discussion](https://www.reddit.com/r/sonarr/comments/1pecw2c/hardlink_permission/) | Explain expected ownership as well as permission failures. |
| 2026-04-01 | DockSTARTer user bentallea fixed DNS directly in Compose; the wrapper removed those changes. A maintainer explained its regeneration/override model. [Issue #2463](https://github.com/GhostWriters/DockSTARTer/issues/2463) | Preserve manual Compose edits during later self-host operations. |
| 2026-01-16 | A DockSTARTer user reported a permissions failure moving the tool's own configuration. [Issue #2393](https://github.com/GhostWriters/DockSTARTer/issues/2393) | Preserve a working installation and make migration failures diagnosable independently. |
| 2025-09-18 | Users preferred separate related Projects for maintenance/migration; replies clarified that one file can still isolate networks and operate individual services. [Discussion](https://www.reddit.com/r/selfhosted/comments/1nk5ier/are_there_any_benefits_drawbacks_to_putting_all/) | One combined file is this product's chosen interface, not a universal preference. |
| 2026-09-12 | A user requested clear backup coverage, automatic Project/volume discovery, and individual restore; replies offered different approaches. [Request](https://www.reddit.com/r/selfhosted/comments/1we6jj9/a_server_backup_solution_that_actually_works/) | Identify required files/data and explain recovery; YAML alone is incomplete. |
| 2025-03-18 | Users disagreed about unattended updates, notifications and pinning; some described failures. [Discussion](https://www.reddit.com/r/selfhosted/comments/1je2o4u/psa_watchtower_is_an_unmaintained_project/) | State an update policy and preserve user control rather than assuming automatic updates. |
| 2025-03-31 | Feedback on a media Compose guide requested Jellyfin documentation and clarity that the VPN configuration was not tied to PIA. [Feedback](https://www.reddit.com/r/selfhosted/comments/1jnuad8/) | Explain optional components and provider choice. |
| 2026-06-29 | An experienced infrastructure user credited AI with finishing OAuth, email and YAML configuration but stressed that expertise helped catch mistakes; another user described AI helping them enter self-hosting. [Accounts](https://www.reddit.com/r/selfhosted/comments/1uim44v/tribute_to_a_careful_use_of_ai/) | Supports assisted configuration/explanation; demand for unrestricted autonomous operation remains unestablished. |

YAMS's own Prowlarr instructions require API keys, internal addresses, connection
tests and synchronization. This establishes concrete manual work that Setup and
Links could save; it is author documentation, not independent customer demand.
[YAMS instructions](https://yams.media/config/prowlarr/).

## Concrete tasks for outside feedback

1. Complete one Recipe with an agent, then stop using self-host and operate it
   from a fresh terminal using ordinary Compose. Identify missing instructions.
2. Import a permitted test file while keeping the downloader's copy available.
   Establish whether it linked or copied, and whether the explanation is clear.
3. Edit one port or DNS setting in YAML, then use self-host to change another
   App. Verify that the manual edit survives.
4. Restore one App using the Project configuration, data inventory and
   instructions. Identify missing files or credentials.
5. Compare against asking an ordinary agent to generate Compose directly:
   identify which actual Setup/Link steps saved work and which needed review.

Ask for observed friction and concrete changes, not general endorsement.
Use the current community showcase threads linked in the earlier research.
No outreach has been sent.

## Remaining design work

- Implement the shared-user default and optional separate-media-user setting
  with image/rootless-specific support and explicit existing-Project migration.
- Specify safe conversion of legacy Compose files and credentials without
  changing App data paths or storage identity.
- Specify service/resource ownership and comment-preserving edits in the single
  YAML, including Unmanaged apps and App sidecars.

The file architecture and private `.env` are settled; a separate export or
transfer of management is outside the requested direction.

The maintained requirements, acceptance criteria and current code gaps are in
[self-hosting practices](../agents/self-hosting-practices.md). That checklist
records which safeguards exist and which remain unimplemented; it does not
claim runtime verification.
