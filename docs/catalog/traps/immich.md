# Immich traps

## Pitfalls

- Auxiliary Compose services use App-owned names: `immich-database`, `immich-redis`. Keep these names in connection settings and dependencies; persistent data paths stay unchanged.
- To upgrade old service names, rerun `self-host install immich nextcloud --yes`. Reinstall renders the current definitions and Compose removes orphan containers only in this Project, without deleting volumes. `self-host update` pulls images from saved definitions; it does not rename services.
- PostgreSQL belongs on suitable local storage; back up database and originals.

## Security

- Keep originals private and protect mobile/session credentials.
- Homarr uses an administrator-owned key with exactly `user.read` and `server.statistics`; album access and write permissions are not needed. Setup reuses an accepted key and repairs a missing recorded ID only after checking permissions, administrator identity and useful statistics.
- A lost or rejected Homarr secret with a recorded ID or an existing `self-host Homarr statistics` candidate requires a Manual step. Setup never deletes or rotates keys by name, including renamed recorded keys. Review Immich user settings > API Keys, restore the matching `IMMICH_HOMARR_API_KEY` and `IMMICH_HOMARR_API_KEY_ID` through the Project secret boundary, then run `self-host setup immich` followed by `self-host setup homarr`. Delete only a key confirmed to belong to this Project, never a same-name user key. A mismatched saved key and recorded ID also requires review rather than silently changing ownership.
- New key responses must contain a nonempty ID, secret, expected name and exact permissions before Setup saves the secret. Interrupted persistence can recover the ID from an accepted saved key; otherwise use the Manual recovery above. Transport errors, unavailable verification and incomplete key discovery do not authorize issuing replacement keys.

Sources: [Immich key controller](https://github.com/immich-app/immich/blob/main/server/src/controllers/api-key.controller.ts), [key service](https://github.com/immich-app/immich/blob/main/server/src/services/api-key.service.ts) and [response schema](https://github.com/immich-app/immich/blob/main/server/src/dtos/api-key.dto.ts).
