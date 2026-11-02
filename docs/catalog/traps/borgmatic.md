# Borgmatic traps

## Pitfalls

- The publisher documents daily scheduling by default and CRON=false to disable it; do not start a backup schedule before separate source, destination, and write approval.
- The recipe's managed source and repository folders are empty staging placeholders, not selected production data or an independent off-host copy.
- The version-only container exits and cannot accept `docker compose exec`. After separate source, destination and write approval and repository preparation, run a one-off job from the Home folder against its canonical `compose.yml`: `docker compose run --rm --no-deps -T borgmatic borgmatic create --verbosity 1`. Ordinary Compose reads the private `.env` and existing Project identity there; `secrets.env` is a legacy compatibility input, not a required second env file. Preserve the Project name and keep cron disabled. Setup initializes no repository and runs no backup.
- Hooks must fail closed when a database dump or quiesce step fails; a successful archive still requires an isolated restore drill.

## Security

- Protect the Borg repository passphrase, keyfiles, and any remote SSH key outside ordinary reports and logs.
- Select backup sources and a repository destination only after their exact ownership is known; do not mount the Docker socket by default.

## Sources

- [Compose one-off commands and dependency isolation](https://github.com/docker/compose/blob/main/docs/reference/compose_run.md).
- [Borgmatic repository creation and backups](https://torsion.org/borgmatic/how-to/set-up-backups/#repository-creation).
