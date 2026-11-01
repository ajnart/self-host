# Self-hosting practices

Use this checklist for the behavior a change touches. It combines upstream
requirements with the product choices accepted on 2026-11-01; recommendations
are not universal Docker standards. Keep focused changes focused and identify
unresolved gaps rather than claiming the whole checklist passes.

## Review process

1. Identify the applicable rows below and the selected image's current contract.
2. Preserve existing data, manual edits, Access and image-specific behavior.
3. Record evidence for each affected criterion and explain any exception with
   its upstream source. Distinguish code inspection, a capability probe and an
   observed working App.

The implementation evidence below combines source inspection and regression
tests, not runtime acceptance. Runtime evidence belongs in the existing CI or
an approved runtime evaluation; this guide does not authorize Docker runs for a
focused change.

The current Linux rootless acceptance run and its platform limits are recorded
in [the Compose and media verification](../compose-hardlinks-proof.md).

## Media storage and ownership

These are independent choices:

- **Hardlink-friendly storage is the default** for cooperating media Apps.
  Importers see downloads and libraries through one parent mount on compatible
  storage; downloaders see their narrower subtree at the same internal path.
- **Separate media App users are optional and off by default.** The simpler
  model uses one effective media UID/GID with umask `022`. The option uses
  distinct supported media App UIDs, one shared group and umask `002`.

Both ownership models support hardlinks. Separate users provide distinct file
ownership; they are not a performance prerequisite. These identities are Unix
file owners, distinct from the Admin login used in an App's web interface.
[TRaSH hardlinks and permissions](https://trash-guides.info/File-and-Folder-Structure/Hardlinks-and-Instant-Moves/),
[Servarr Docker guide](https://wiki.servarr.com/docker-guide).

| Criterion | Evidence required |
| --- | --- |
| Importers have a common parent mount covering completed downloads and libraries. | Rendered mounts and configured App paths agree. Sonarr/Radarr perform the link, so mounting only two separate subdirectories is insufficient even on the same host disk. |
| Storage really supports the operation. | Verify relevant source/library pairs inside each importing container as the application identity. A successful host-side probe or root exec does not prove the App can link. Identify nested mounts, datasets/subvolumes or union-filesystem restrictions. |
| A hardlink claim identifies its evidence. | Distinguish configured settings, a successful temporary link probe and an observed App import. Arr can fall back to copies. Incompatible storage requires the user's acknowledged copy fallback. |
| Both ownership modes use supported image mechanisms. | Use PUID/PGID, `user:` or another mapping only when the selected image documents it. Preserve namespace-aware rootless mappings. `UsesMedia` alone does not establish support for separate App identities. |
| Separate-user mode is a media-specific option. | Expose the same persisted choice in Settings and media-relevant Onboarding, default off. Preserve unrelated Apps and database users. Explain shared group writes and group inheritance; do not change every App's global IDs/umask. |
| Ownership changes preserve existing files. | Enabling or disabling the option on an existing Project needs an explicit migration plan covering existing config/media ownership, modes, ACLs and namespaces. A boolean or umask change is not a migration. Avoid blanket recursive ownership changes. |
| Configuration remains private and appropriately writable. | Shared media permissions do not automatically apply to App configuration. Media-only readers may use read-only media when features permit; Bazarr and other writers need appropriate write access. |
| Mount and App path changes happen together. | Align Arr library roots, download-client paths, Seerr defaults and media-server/subtitle paths. Preserve existing item/torrent records until explicit migration; correcting mounts does not deduplicate old copies. |

[TRaSH layout](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Docker/),
[LinuxServer identity support](https://docs.linuxserver.io/misc/non-root/),
[Docker rootless IDs](https://docs.docker.com/engine/security/rootless/uid-gid-mapping/),
[directory group inheritance](https://www.gnu.org/software/coreutils/manual/html_node/Directory-Setuid-and-Setgid.html).
Storage-specific qualifications and music-tagging consequences are recorded in
[the research](../research/compose-conventions-and-user-needs.md#media-storage-and-permissions).

## Compose, data and operations

| Criterion | Evidence required / primary source |
| --- | --- |
| One authoritative Compose file. | self-host and ordinary Compose operate on the same Project from a fresh shell. Preserve comments, custom services and unrelated edits. Use YAML structure and explicit ownership information to identify resources. [ADR 0014](../adr/0014-one-editable-compose-file.md). |
| Stable storage and Project identity. | Preserve the effective Project name, service DNS, named-volume identities and rebased relative paths. Each old `./config` must continue referring to its own App data. [Project name](https://docs.docker.com/reference/compose-file/version-and-name/), [volume names](https://docs.docker.com/reference/compose-file/volumes/). |
| Private credentials with public settings filtered. | For ADR 0014's private `.env`, write private permissions and classify values before status/config/share serialization. Preserve masking and per-App secret grants. Service `env_file` does not supply Compose interpolation; injecting the whole credential collection into every App is inappropriate. [Environment semantics](https://docs.docker.com/reference/compose-file/services/#env_file), [secret sources](https://docs.docker.com/reference/compose-file/secrets/). |
| Bounded logs. | Newly generated long-running services use a deliberate rotating logging policy, preserving explicit user choices and daemon settings. Docker recommends `local`; bounded `json-file` is also valid. [Logging guidance](https://docs.docker.com/engine/logging/configure/), [JSON-file options](https://docs.docker.com/engine/logging/drivers/json-file/). |
| Appropriate restart and readiness behavior. | Preserve long-running restart policies and intentional one-shot jobs. For real dependencies, verify upstream retries or health-gated startup. A started container, HTTP 200 or health check does not establish completed Setup and Links. [Restart policies](https://docs.docker.com/engine/containers/start-containers-automatically/), [startup order](https://docs.docker.com/compose/how-tos/startup-order/). |
| Service DNS and selected Access remain intact. | Use service names/container ports rather than fixed IPs or unnecessary global container names. Preserve every selected bind address, including secondary ports; conversion must not broaden Access. [Compose networking](https://docs.docker.com/compose/how-tos/networking/). |
| Ordinary lifecycle operations preserve data. | `down`, restart, update and non-purge removal retain persistent data. Changing YAML layout must not change host data locations or volume lifecycle ownership. [Docker down](https://docs.docker.com/reference/cli/docker/compose/down/). |
| Backups and restores match the real layout. | Include canonical YAML, credentials, required records and selected App data in the new format. Partial restore preserves unrelated services/resources. Explain exclusions: external media, arbitrary external binds and named volumes are not automatically covered by archiving App directories. Preserve legacy restore and rollback. |
| Updates remain deliberate and inspectable. | Preserve selected image references and user edits. Explain that `pull` downloads and `up -d` recreates. Unattended updates or universal digest pins require their own product decision. [Docker pull](https://docs.docker.com/reference/cli/docker/compose/pull/). |
| Exceptions follow the selected image. | Verify requirements for identity, secret files, capabilities, writable paths, devices and limits. Apply changes to supported images rather than imposing generic settings throughout the Catalog. Keep obsolete `version:` out of new Compose. |

The private `.env` is authoritative after consolidation. Existing
`secrets.env` files remain compatible recovery inputs. Public Host settings
are allowlisted separately from credentials.

## Implementation evidence and remaining work

| Area | Evidence and limits |
| --- | --- |
| Hardlinks and paths | `internal/apps/mediastorage` selects shared or legacy paths. Arr receives one parent `/data` mount; downloaders and readers receive their relevant subtree. App and standalone-render tests cover coordinated paths. `internal/apps/arr/hardlinks.go` probes temporary links as the App identity and checks device/inode equality; explicit copy fallback acknowledges extra writes and space. Actual imports and storage-specific behavior still require runtime evaluation. |
| One Compose file | `internal/project/canonical.go` consolidates supported legacy includes, rebases paths, records ownership and preserves unknown YAML/comments. Project tests cover migration, manual services, resource removal, anchors and unsafe metadata. Service tests cover Project names and customized updates. Unsupported ambiguous layouts fail before rewriting the root. |
| Private interpolation | Project env methods preserve comments and unknown keys, write mode `0600` and filter public settings. CLI private-input and Project tests cover credential handling and legacy compatibility. |
| Backup and Restore | Archives include canonical YAML, private env, state and selected App directories. Service regressions cover scoped and fresh Restore, manual definitions, legacy backups, customized updates, private media configuration and rollback. External media, arbitrary external binds and named volumes remain excluded and are reported. |
| Bounded logs | New generated long-running services default to Docker's `local` driver with `max-size: 10m` and `max-file: 3`, while explicit policies survive. Standalone exports use the same defaults. |
| Optional media users | Off by default in Onboarding and Settings. An explicit list of supported LinuxServer images uses distinct persisted PUIDs, shared PGID, umask `002`, setgid media directories and private App configuration. Unsupported namespace mappings and implicit changes to existing ownership are refused. No blanket recursive ownership changes. |
| Lifecycle, DNS and Access | CLI/service regression tests retain data-preserving lifecycle operations, selected bind addresses, service-name Links and root-relative paths. Existing manual image references and fields survive updates. |
| Readiness and image contracts | Existing App-specific restart, health and Setup behavior is preserved. This change does not establish readiness or identity support for every Catalog image; verify the affected image's documented contract when changing it. |

The source-backed audit and firsthand community evidence live in
[the research note](../research/compose-conventions-and-user-needs.md).
Update this evidence table when implementation changes; documentation alone
does not mark a criterion complete.
