# One editable Compose file per Project

Each Project uses one root Compose file containing all of its Apps and required
resources. self-host and ordinary Docker Compose operate on that same file;
people can alternate between them or stop using self-host. This replaces the
per-App include-file layout in [ADR 0001](0001-compiled-apps-compose-is-truth.md)
so Compose remains understandable and directly usable without a separate export
or transfer of management.

self-host preserves unrelated services, manual edits and comments when changing
an App. Comments may group Apps and explain settings; service identity comes
from YAML structure and explicit ownership information rather than comment
markers. API Setup and Links remain capabilities of self-host.

A private `.env` alongside the Compose file supplies ordinary Compose
interpolation. Public settings are allowlisted before serialization. Legacy
`secrets.env` remains readable and is synchronized when already present.

The first approved mutation consolidates supported legacy includes, rebasing
relative paths and retaining `compose.legacy.yml` as a recovery snapshot.
Per-App files remain recovery snapshots; subsequent operations read the root
file. Ambiguous includes or resource ownership stop consolidation before writes.
Existing App configuration, media paths and storage identities are preserved.
Fresh media Projects use a shared parent mount; changing existing media paths or
file ownership requires an explicit migration.

Accepted and implemented, 2026-11-01. Migration and recovery are covered by
Project and service regression tests; this records implementation, not runtime
acceptance of every App or storage backend.
