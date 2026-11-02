# Hardlinks and “just Docker Compose”

Research date: 2026-11-01. Repository inspected at `865e7a0b` by two read-only
research agents: media storage and Compose ownership/export. Documentation was
checked with Context7 and the primary sources below. No Docker, tests, browser
or installer ran. Accepted choices are identified in the interview record;
other recommendations remain proposals.

Follow-up: [Compose conventions and firsthand user needs](compose-conventions-and-user-needs.md)
records deeper TRaSH, image identity, bare-Compose and community research.

## Hardlinks: the reported problem is real

Sonarr and Radarr receive separate `${MEDIA_ROOT}:/media` and
`${DOWNLOADS_ROOT}:/downloads` binds (`internal/apps/arr/arr.go:94`). Their
configured library roots are `/media/tv` and `/media/movies` (`:44`, `:62`).
qBittorrent Setup sets its save path to `/downloads`
(`internal/apps/qbittorrent/qbittorrent.go:166`).

Linux rejects hardlinks across different mount points, even when the mounts
refer to the same underlying filesystem. Separate binds also prevent atomic
renames between them. LinuxServer documents this exact issue for these images:
[link(2)](https://man7.org/linux/man-pages/man2/link.2.html),
[rename(2)](https://man7.org/linux/man-pages/man2/rename.2.html),
[Sonarr](https://docs.linuxserver.io/images/docker-sonarr/#media-folders),
[Radarr](https://docs.linuxserver.io/images/docker-radarr/#media-folders).

A candidate layout, following the
[Servarr Docker guide](https://wiki.servarr.com/docker-guide) and
[TRaSH guidance](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Docker/):

```text
<data-root>/
  torrents/{tv,movies,music}
  usenet/{incomplete,complete}
  media/{tv,movies,music}
```

Sonarr/Radarr receive `<data-root>:/data`; qBittorrent can receive only
`<data-root>/torrents:/data/torrents`. The importing App performs the hardlink,
so it needs the shared parent. The downloader needs a consistent reported path,
not access to the library. A simpler `/data/{downloads,media}` layout also works.

A shared parent bind is necessary for this layout but does not prove hardlinks
work. Nested mounts, datasets/subvolumes, filesystem support and permissions
still matter. A real capability check must run inside the importing container
as its application user, creating a temporary link between each relevant source
and library root and checking inode/device identity. An observed successful
Arr import is stronger evidence than that capability check alone.

The repository does not manage or verify `copyUsingHardlinks`. Upstream Sonarr
defaults it to true, but Arr imports can fall back to copying when links fail:
[Sonarr settings](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Configuration/ConfigService.cs),
[Sonarr import](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/MediaFiles/EpisodeFileMovingService.cs),
[Radarr fallback](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Common/Disk/DiskTransferService.cs).

### Scope and migration constraints

- Host defaults are separate `~/media` and `~/downloads` folders; current path
  checks require absolute paths, not hardlink compatibility
  (`internal/service/service.go:113`, `internal/project/project.go:185`).
- DCM inherits the separate binds from Go App definitions. Change the Go source
  and generator as needed, then regenerate; do not patch generated YAML
  (`tools/sitegen/standalone/compose.go:18`, `:328`).
- SABnzbd and Lidarr currently use App-local `/data`; Deluge and Transmission
  use App-local downloads. The existing Arr download-client Links cover
  qBittorrent, not all downloaders (`internal/apps/arr/links.go:107`).
- Update can replace unmodified generated fragments. A new mount must not
  silently hide old libraries (`internal/service/lifecycle.go:82`,
  `internal/service/fragments.go:135`).
- Changing the root-folder constant only adds a root; it does not rewrite
  existing series/movie paths. Changing qBittorrent's default does not relocate
  existing torrents. Seerr also uses the old roots
  (`internal/apps/arr/arr.go:151`, `internal/apps/seerr/seerr.go:243`).
- Correcting mounts does not turn old duplicate copies into hardlinks. Media
  relocation and deduplication need separate, explicit decisions.
- Choose a dedicated data root; do not infer `/` or an entire home directory as
  a common parent for arbitrary existing paths.

Proposal: correct new-Project defaults; keep existing Projects intact until an
explicit migration. Describe incompatible storage and any copy fallback clearly.
Reserve “hardlinks working” for verified results. Shared PUID/PGID already exist;
changing UMASK globally is not required to fix the mount boundary.

## One Compose Project already exists; one YAML is feasible

The root `compose.yml` includes each App's file, Unmanaged apps under `extra/`,
and the Caddy file when used. App discovery follows those folders
(`internal/project/project.go:430`, `:510`).

After successful Setup, ordinary Compose can operate the existing installation:

```bash
cd ~/.self-host
docker compose -p selfhost --env-file .env --env-file secrets.env up -d
```

The actual Home folder and Project name may differ. The CLI passes the same
Project and environment-file arguments (`internal/compose/compose.go:51`,
`internal/service/service.go:64`). App configuration and data must stay in place.

DCM already exports combined `docker-compose.yaml` plus `.env`
(`dcm/src/lib/export/index.ts:41`). The CLI has no full-Project Compose export;
`access export` only exports reverse-proxy information. One Compose YAML can
still reference secrets, configuration files and data directories: one YAML
does not mean one total file.

Fresh YAML does not perform API-based Setup or Links. Install also writes some
required files before containers start. DCM documents manual steps and excludes
Apps whose generated-file requirements it cannot provide
(`internal/service/install.go:388`, `:408`, `:433`, `:523`;
`tools/sitegen/standalone/standalone.go:42`, `:57`; `notes.go:27`).

**Accepted design:** self-host itself will use one authoritative, editable root
Compose file. Both self-host and ordinary Compose operate on that file; the
user explicitly rejected a separate export or transfer of management.
[ADR 0014](../adr/0014-one-editable-compose-file.md) replaces ADR 0001's per-App
include layout. This changes discovery, App/service ownership, manual-edit
protection, add/remove, refreshes and backups. Simply flattening root YAML is
insufficient: later mutations currently rewrite it
(`internal/service/mutation.go:169`, `:183`, `:198`). Existing fragment hashes
protect manual edits (`internal/service/fragments.go:36`, `:135`).

### Single-file migration requirements from Docker documentation

- Includes resolve relative paths against each included file's directory.
  Concatenation or repeated `-f` arguments do not preserve that behavior.
  Every `./config` must still point at its own App directory.
  [Include](https://docs.docker.com/reference/compose-file/include/),
  [CLI](https://docs.docker.com/reference/cli/docker/compose/).
- `config` normally resolves interpolation. `--no-interpolate` and
  `--no-env-resolution` prevent different expansions; neither removes secrets
  already written literally in a user-provided file. Conversion must preserve
  credential privacy.
  [Config](https://docs.docker.com/reference/cli/docker/compose/config/).
- Repeated `--env-file` arguments apply in order, with shell interpolation values
  taking precedence. Service `env_file:` does not supply YAML interpolation.
  [Interpolation](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/).
- Preserve Project identity and paths to existing volumes/configuration. A
  `config`-based conversion still needs path validation: included files are loaded
  separately, with path resolution in the
  [include loader](https://github.com/compose-spec/compose-go/blob/main/loader/include.go).

## Feedback and positioning

The pasted comments raise concrete questions about storage, control and who
benefits. Their votes do not establish demand or absence of demand. A useful
positioning hypothesis is: an optional toolkit that lets people and their agents
install, configure and connect Apps, then keep operating them with Compose.
API Setup and Links are the existing value beyond writing YAML.

Verified first-party places to request feedback:

| Community | Available route and proposed angle |
| --- | --- |
| [r/codex showcase](https://www.reddit.com/r/codex/comments/1x52817/show_us_all_what_youve_been_building_with_codex/) | Weekly thread for projects built with Codex; demonstrate an actual agent workflow. |
| [r/ClaudeCode showcase](https://www.reddit.com/r/ClaudeCode/comments/1xfu9ul/weekly_showcase_thread_what_are_you_building_with/) | Quick promotion belongs in the thread; substantial technical write-ups may use Built with Claude Code. Explain Claude Code's actual involvement. |
| [r/ClaudeAI showcase](https://www.reddit.com/r/ClaudeAI/comments/1xi1yek/show_us_what_youve_created_with_claude/) | Weekly place to show work created with Claude. |
| [r/ChatGPTCoding promotion](https://www.reddit.com/r/ChatGPTCoding/comments/1x9qs5j/weekly_self_promotion_thread/) | Dedicated promotion thread asks for problem, tools, audience, affiliation and requested feedback. |
| [r/LocalLLaMA showcase](https://www.reddit.com/r/LocalLLaMA/comments/1x9yg86/biweekly_megathread_project_showcase/) | Lower-priority fit unless demonstrating locally hosted models; asks for comparison and validation, with emphasis on open, locally hostable software. |

Use each community's current pinned thread when posting; these are the verified
examples, not permanent posting targets. Dedicated rules pages were not readable
through the available fetchers; this table relies on the threads themselves.
No Reddit post or message was sent.

Proposal: ask people to try a specific task, inspect what changed and report
where trust or usefulness breaks. Demonstrate Compose ownership and acknowledge
current limitations. Do not claim hardlink support before implementation and
verification. A move to an AI community tests a different audience; it does not
resolve the technical criticism.

## Interview record

1. **Accepted: agent-assisted users are the primary audience.** Confirmed by
   the user on 2026-11-01. The Dashboard and disposable integration labs remain
   supported uses; this decision sets the priority for product design and feedback.
2. **Accepted: self-host uses one authoritative Compose file.** Clarified by
   the user on 2026-11-01: this replaces the current per-App include files;
   self-host and ordinary `docker compose up`, `down` and related commands
   operate on the same Project. Users may continue self-host or stop using it.
   There is no separate export, detached copy or transfer of management.
   Required App data and configuration remain outside the YAML.
3. **Accepted: hardlink-compatible defaults with an explicit copy fallback.**
   Confirmed by the user on 2026-11-01. Existing installations remain intact
   until an explicit migration is requested.
4. **Accepted: a private conventional `.env`.** The user accepted this
   recommendation and clarified that it accompanies the shared Project, rather
   than a separate exported folder. Legacy `secrets.env` compatibility needs
   migration; the current implementation and agent instructions still describe
   the legacy layout.
5. **Accepted: comments can group services and carry notes.** Machine identity
   should use YAML structure and explicit ownership information; comments are
   for readability and must survive unrelated edits.
6. **Refined: media ownership is independently configurable.** After clarifying
   the difference between hardlinks and Unix users, the user requested optional
   separate media App users in Settings/Onboarding and suggested default off.
   The working default is one effective media UID/GID with `022`; the option
   uses supported per-App identities, a shared group and `002`. Hardlink-friendly
   paths remain the baseline in both modes. Changing an existing mode requires
   explicit ownership migration.

The user requested deeper research into TRaSH guidance, permissions and other
Compose conventions, plus firsthand community needs. Distinguish the Compose
specification, image-specific requirements, author recommendations and user
preferences; “industry standard” does not establish a universal setting such as
PUID/PGID support or one umask for every image.

Migration mechanics, manual-edit policy and feedback tasks follow these choices.
Record accepted terminology in `CONTEXT.md`; record an architectural trade-off
in an ADR only when the choice has actually been made.

The maintained checklist and source-inspected gaps are in
[self-hosting practices](../agents/self-hosting-practices.md).
