# Audiobookshelf traps

## Pitfalls

- File ownership, metadata matching, and database plus media backup must be planned together.
- The Host media folder is mounted at `/data/media` in shared Projects and `/media` in legacy Projects; `/config` and `/metadata` retain their existing App-local mounts. Create missing audiobook/podcast subfolders as the Host user, then select them in Settings > Libraries. Mounting the prepared parent avoids Docker creating missing Host subfolders as root. Review existing libraries first: Setup neither creates libraries nor replaces their folders/settings, scans or imports media.
- The mounts make media persistent, not backed up: Project Backups exclude media libraries. Plan a separate backup and check write access before enabling podcast downloads or metadata writes.
- The [official Compose example](https://github.com/advplyr/audiobookshelf/blob/master/docker-compose.yml) separates media from configuration and metadata; this App exposes the shared media parent so existing libraries can choose their actual subfolders.

## Security

- Protect accounts, listening history, library metadata, and remote access.
- Homarr's native Audio Stats client needs an administrator for online sessions.
  Its separate account disables media changes but still has role-based
  administrative access; do not call its key read-only or share the root token.
- Named API keys are shown only when created. Setup preserves owned credentials
  and stops for manual recovery instead of adopting matching account/key names
  or silently replacing revoked keys. See
  [the staged Homarr prerequisite](../../homarr-audiobookshelf-stage1.md).
