# Reviewed App actions

## Standards

The first review found five issues. The ordinary Start path could clean up
unrelated containers. The guard could accept stale Catalog ownership. A
successful exit could lead to a completion notification without verification.
State recovery could run before a stale review was refused. The user-facing
docs needed to describe the new path.

Desktop Start and Restart now tell Compose to act only on the selected
services. Stop passes an explicit service list. The guard checks current
ownership before recovery and live Status again before reporting completion.
The README, package guide and version-matched agent guide cover the feature.
The host package owns the Docker endpoint check.

## Spec

The first review found four issues. One-App Start could also reconcile Easy
names across the Project, stale ownership could pass the guard, degraded Apps
could lose their Stop and Restart rows, and a suspended Cloudflare Tunnel could
report a successful Start without starting. Desktop Actions skip Project-wide
Easy names reconciliation. Both degraded Actions are offered after a live
check. A suspended tunnel returns an explanation directing the person to
Public access in the Dashboard.

## Repeating the native check

Launch the picker from the active seat with Super + Space. An SSH-started
terminal can look identical on screen while Polkit correctly rejects its
inactive session. Use the default Docker socket access, and record the App's
container start time before the review. Add a disposable peer container to
catch accidental Project-wide Compose operations.

[Docker Compose up](https://docs.docker.com/reference/cli/docker/compose/up/)
· [Docker Compose restart](https://docs.docker.com/reference/cli/docker/compose/restart/)
