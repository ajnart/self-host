# Unraid: provisional installation and reboot verification

**No Unraid version or storage arrangement has been verified on real hardware.**
Detection, source review and fake-Host tests are not a support claim. Work follows
[#121](https://github.com/ajnart/self-host/issues/121), then
[#122](https://github.com/ajnart/self-host/issues/122), then the real-Host evidence
gate [#123](https://github.com/ajnart/self-host/issues/123). Representative App
Setup, Link behaviour and reboot persistence remain blocked on a consenting
operator, target-Host facts and an approved verification window.

## Inspect before approval

Doctor reads `/etc/unraid-version` on Linux and reports its version as evidence,
not certification. An absent file leaves ordinary Linux behaviour unchanged;
an unreadable or unrecognized file gives an uncertain finding. It does not read
Unraid configuration, credentials or plugin files.

Doctor's `unraid-home` finding inspects the resolved Home folder, or its nearest
existing ancestor, through `/proc/self/mountinfo`, following existing symlinks.
It creates nothing. It reports the mount, filesystem and read-only flag, warns
about memory-backed storage and marks persistence/database suitability unverified
even for a local disk filesystem. A missing pool can expose a different backing
mount: recheck before every new session's changes. Do not infer storage from the
spelling `/root`, `/mnt/user` or a pool name.

1. Record the exact Unraid version and architecture, Docker engine version and
   `docker compose version --short`. Use the local Unraid Docker engine, not a
   remote context whose bind paths may refer to another machine.
2. Verify the selected mount is available and persistent in this Host's storage
   configuration. Select a dedicated binary directory and Home folder on it.
   The binary directory must support execution and atomic replacement for updates.
   Never put live databases on the boot flash just because it retains files.
3. Review each selected App's current upstream requirements: ownership, file
   locking, case sensitivity, atomic rename, database durability and any special
   filesystem restrictions. Writable storage alone proves none of these. For new
   Unraid Projects, self-host accepts only directly observed local `xfs`, `btrfs`,
   `zfs` or `ext2/3/4` Home folder filesystems; shared/FUSE, layered and unknown
   layouts stop with guidance. This is a conservative guard, not certification of
   those local filesystems for every App. Permissions are not rewritten.
4. Keep the whole Home folder on the selected storage: root `compose.yml`, state,
   Profile, private `.env` (mode `0600`) and App configuration/database bind mounts under
   `apps/`. Legacy `secrets.env` remains a compatible migration/restore input.
   Audit the authoritative root Compose services for other bind paths or named volumes;
   per-App Compose files are recovery snapshots, not the current configuration.
   those depend on their own mounts or Docker's data storage. Media and downloads
   are separate Host settings and are not included in App Backups.
5. Use Docker Compose v2 or later, available as `docker compose`. The root
   Compose file contains services directly and does not require `include` support. A
   third-party Compose-management plugin is not inherently required. If Compose
   is missing, the operator must establish an installation method supported by
   their Unraid version under a separate approval; self-host installs no plugin.
6. Check actual selected App ports, including secondary TCP/UDP inputs. Easy
   names at home uses port 80; it does not require 443. Check 443 when an App or
   an existing proxy actually needs it. `self-host host report --json` probes
   both 80 and 443 read-only; omit `--extra`, which includes temporary Docker
   resource creation. Do not stop listeners or change Unraid management settings.

## Plan, approve, install

The plan must name the exact binary destination, Home folder, backing mounts,
App/database paths (including Docker volumes), media/download paths if used,
App set, Access, ports, administrator identity, Manual steps and recovery plan.
Include the exact fresh-session invocation. Approval for this plan is separate
from approval to install the binary, change public Access or reboot the Host.

These are templates, **not verified Unraid paths**. Replace the mount with the
operator's verified, already mounted storage. Do not create a replacement mount
directory when storage is absent.

```bash
SELF_HOST_MOUNT='/replace/with/operator-verified/mountpoint'
findmnt --mountpoint "$SELF_HOST_MOUNT" --output TARGET,SOURCE,FSTYPE,OPTIONS
SELF_HOST_BINARY_DIR="$SELF_HOST_MOUNT/self-host-bin"
SELF_HOST_HOME="$SELF_HOST_MOUNT/self-host-home"
```

After approval to install the binary, use the reviewed installer with its
existing destination override (pin `SELF_HOST_VERSION` to the agreed release).
The installer does not remember the Home folder or establish persistence:

```bash
SELF_HOST_INSTALL_DIR="$SELF_HOST_BINARY_DIR" \
  SELF_HOST_SKIP_SKILL_PROMPT=1 bash /path/to/reviewed/install.sh
SELF_HOST_HOME="$SELF_HOST_HOME" "$SELF_HOST_BINARY_DIR/self-host" --version
SELF_HOST_HOME="$SELF_HOST_HOME" "$SELF_HOST_BINARY_DIR/self-host" doctor --json
```

Before approving the App install, inspect its information and planned ports:

```bash
SELF_HOST_HOME="$SELF_HOST_HOME" "$SELF_HOST_BINARY_DIR/self-host" apps info ntfy --json
SELF_HOST_HOME="$SELF_HOST_HOME" "$SELF_HOST_BINARY_DIR/self-host" doctor ntfy --set ntfy.port=8088 --json
```

Use the agreed App set instead of this single-App example. Planned Doctor checks
also inspect additions to an existing Project, reserving its effective Compose publications
including manual services and Unmanaged apps, alongside recorded App ports
and port 80 when Easy names are on, even when those Apps are stopped. Already
installed targets are skipped; plain Doctor inspects their current bindings. Add
`--easy-names` to planned Doctor only if choosing Easy names. An occupied
explicit port fails; an occupied default suggests the existing allocator's
alternative. Review that alternative before approval. Permission/unsupported
bind results are inconclusive, not proof a port can be published.

After approval of the exact plan, for the example using Home network Access:

```bash
SELF_HOST_HOME="$SELF_HOST_HOME" "$SELF_HOST_BINARY_DIR/self-host" install ntfy --set ntfy.port=8088 --exposure lan --yes
SELF_HOST_HOME="$SELF_HOST_HOME" "$SELF_HOST_BINARY_DIR/self-host" status --json
```

New Unraid installs require explicit `SELF_HOST_HOME`; they refuse known volatile
or read-only backing storage, unknown inspection results, and a binary on known
volatile, read-only or `noexec` storage. These guards cannot verify future mount
availability or database semantics. Existing storage is not moved. The first
approved mutation consolidates supported legacy includes into the root Compose
file, preserving data paths and `compose.legacy.yml` as a recovery snapshot.
Doctor warnings require operator review even when its exit code is zero.

Installation success is provisional. Verify that the Apps do their jobs, accounts
are usable, and consuming App Links work. Resolve or record Manual steps. Only
the self-host Compose Project may be managed; leave unrelated containers,
networks, volumes and Unraid management settings untouched. A necessary plugin,
boot edit, data move, permission change or container adoption requires a new
explicit plan and approval, not an implied follow-up.

## Fresh session and updates

Shell variables and `PATH` changes are not assumed to survive reboot. In **every
fresh session**, re-enter the same approved absolute paths, verify the mount is
available with the same backing storage, then use the full binary path and
`SELF_HOST_HOME` on every command above. If the mount differs or is missing,
stop: do not reinstall, create a new Home folder, or repeat Onboarding.

`self-update` replaces the running executable at its resolved location, staging
beside it and renaming atomically; it does not switch to `/usr/local/bin` or move
the Project. Invoke the retained binary with the same Home folder after approval.
No boot file, startup hook, shell profile or Unraid auto-start setting is changed.
Automatic App resumption is unverified; after storage and Docker are available,
record whether the operator needs the approved explicit `self-host start` Action.
If persisted Setup has to run again for recovery, the reboot criterion fails.

## Real-Host evidence gate (#123)

No reboot command is provided or authorized here. Obtain explicit installation
consent and **separate disruptive reboot approval**, with an operator, window,
App set, storage arrangement and recovery plan agreed beforehand.

Record a redacted before/after evidence entry for each tested combination:

- Date, self-host release/commit, Unraid version, architecture, Docker and Compose
  versions, relevant existing plugin versions if any, App image tags/digests.
- Binary/Home folder absolute locations and resolved backing mounts/filesystems,
  mount options, ownership, database requirements, external data/volume storage,
  Access and every selected port. Redact private paths and hostnames consistently.
- Before reboot: Doctor and Status, usable administrator accounts without
  credential values, identifiable harmless data in a stateful/database-backed
  App, and an actual consuming App Link operation against its target. Record
  expected Addresses and Manual steps. Choose the behavioural checks in advance;
  e.g. publish/read a harmless ntfy message, and query a target through a consumer.
- After a real reboot and normal storage/Docker availability: fresh-session
  invocation of the same binary and Project, without reinstall, Restore,
  repeated Onboarding, account recreation or rerunning Setup. Verify the same
  accounts, created data, saved configuration, Link behaviour and Addresses.
  Distinguish automatic resumption from an approved explicit start Action.
- Agreed read-only before/after observations of unrelated containers and Unraid
  management configuration/lifecycle. Separate whole-Host reboot effects from
  self-host Actions; never exercise broad Docker stop, prune or volume deletion.
- Each criterion's result, redacted evidence and any failure or unknown. Running
  containers and HTTP 200 alone are insufficient. Keep credentials only in the
  Project; `self-host credentials` is an operator reference, never evidence to copy.

Publish only operator-reviewed, redacted results. Until every criterion passes,
the tested-combination list is **empty**, and all Unraid versions, storage
arrangements, Access choices and reboot behaviours remain unverified.

## Evidence behind the guidance

- Unraid's WebGUI reads `version` from `/etc/unraid-version`:
  [upstream source pinned to 9754759](https://github.com/unraid/webgui/blob/975475904af36845053e3f96bd7a84f85fd2a969/emhttp/plugins/dynamix/include/DefaultPageLayout/Header.php).
  This establishes a detection convention, not version-specific support.
- [ADR 0014](adr/0014-one-editable-compose-file.md) defines the authoritative root
  Compose file and private `.env`; legacy includes are consolidated by self-host,
  not a prerequisite for the installed Compose plugin.
- [Unraid container guidance](https://docs.unraid.net/unraid-os/using-unraid-to/run-docker-containers/managing-and-customizing-containers/)
  covers external App data, host port mappings and startup dependencies. Its
  example `appdata` share is not evidence of database suitability for every App
  or of this Compose Project's automatic startup.
- [Linux mountinfo](https://man7.org/linux/man-pages/man5/proc_pid_mountinfo.5.html)
  supplies current mount evidence only, not a promise about reboot persistence.
- ADRs [0001](adr/0001-compiled-apps-compose-is-truth.md) and
  [0005](adr/0005-agents-load-the-skill-on-their-own.md) retain the single Compose
  Project boundary and explicit approval for changes.
