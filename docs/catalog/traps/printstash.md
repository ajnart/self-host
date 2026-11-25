# PrintStash

PrintStash manages a private 3D printing file library. The single-container
image serves the UI, API and background work on port 3000. The catalog uses
the floating `latest` tag in accordance with its image policy. It resolved to
0.15.1 at source inspection, but must be resolved again before runtime tests.
Record the current version and retain the old image before updating.

One App-local bind mount supplies `/data`, including the database, managed
files, thumbnails, staging, and backups. The App takes positive PUID and PGID
from Host settings and uses them to prepare the private data directory. The
image rejects zero IDs, including the default identity in a rootless Project.
Keep the location and ownership through restart, update and restore. Do not automatically adopt another library.
Before an upgrade, verify a backup of the database and managed files, separately
back up external library originals, and retain the old image. A rollback needs
the pre-upgrade database and managed files, not only the previous image.

The default `trusted_network` setup is not safe internet exposure. The owner
creates the first administrator and selects storage on a trusted local network.
No default credentials are supplied. Close setup and use HTTPS before internet
exposure. Existing accounts and data must never be reset to force Setup through.
`/api/v1/setup/status` does not prove ownership or authorization. Once setup
is complete, the App verifies the shared administrator identity and the
authenticated database and storage status. A missing or mismatched credential,
recovery, pending storage, redirect, malformed response or degraded health
stays Manual. Do not print returned response data.

A successful login creates a hashed refresh-token record that expires after
14 days. PrintStash schedules hourly cleanup of expired records. The App reuses
one access token for its Setup and Check in the same run and discards the refresh
token. It avoids logout, which invalidates the user's other sessions.

## Primary sources

- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/docker-compose.yml
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/backend/app/api/v1/setup.py
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/backend/app/api/v1/auth.py
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/backend/app/api/v1/health.py
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/backend/app/modules/identity/auth.py
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/backend/app/modules/identity/jobs.py
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/backend/docker-entrypoint.sh
- https://github.com/xiao-villamor/PrintStash/blob/v0.15.1/docs/deployment.md
- https://www.printstash.org/en/docs/getting-started/first-run/
- https://www.printstash.org/en/docs/getting-started/installation/
- https://www.printstash.org/en/docs/guides/upgrading/
