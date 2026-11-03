# NZBGet traps

## Pitfalls

- The archived legacy repository and images can be mistaken for the maintained successor.
- The official Docker image documents NZBGET_PASS as a plain environment variable, not a file-backed secret; shared Compose must generate its private environment file before the container starts.
- NZBGet RPC uses username/password pairs and does not document an API key.
- Arr Links reuse the target's accepted Admin RPC login, not Homarr's restricted login or an add-only account. They verify the selected existing category with exact case and its active completed path before the native Arr connection test; no category, global path, provider, script, mapping or mount is changed.
- A nonempty Arr category must exist in NZBGet's contiguous category sequence. Stock `Movies`, `Series` and `Music` are not the lowercase `movies`, `tv` and `music` defaults. Create or select the intended category explicitly, apply its settings, then retry `self-host setup sonarr`, `self-host setup radarr` or `self-host setup lidarr`. Existing owned category choices are preserved.
- Category `DestDir` overrides the global destination; otherwise `AppendCategoryDir` controls whether its name is appended. Active RPC configuration resolves variables; historical jobs retain their recorded paths. Enabled global/category extension scripts, unsuitable `KeepHistory`, unresolved paths, inaccessible folders and overlapping library/download folders remain Manual prerequisites, never automatic repairs.
- Legacy Lidarr's App-local `/data` is not a shared downloads mount. Review existing mounts and Remote Path Mappings explicitly; a mapping cannot make inaccessible files available. Path checks do not prove an import or hardlink, and an idle authenticated queue needs no paid Usenet provider.
- The official image's path probe requires `su-exec user:users`, with exact nonroot user/group checks inside its child before any filesystem reads. Arr consumer probes retain `s6-setuidgid abc`. A missing wrapper, unsupported custom identity, unavailable mount information or failed access remains Manual; there is no root, direct-shell or alternate-user fallback. Preserve the selected image and review compatibility explicitly instead of changing it just to pass a probe.
- Missing or uncertain consumer ownership requires explicit review of only the recorded resource. Never adopt a same-name/endpoint client, clear an uncertain creation record blindly, rotate credentials or reinstall NZBGet to retry. Unlink preserves categories, files, target settings and unrelated clients.

## Security

- Keep provider credentials and the control interface on a private network; the RPC control interface uses HTTP Basic authentication unless HTTPS is configured.
- Change the documented default control password before exposing the interface to another device.
