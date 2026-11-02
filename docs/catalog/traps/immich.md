# Immich traps

## Pitfalls

- Auxiliary Compose services use App-owned names: `immich-database`, `immich-redis`. Keep these names in connection settings and dependencies; persistent data paths stay unchanged.
- To upgrade old service names, rerun `self-host install immich nextcloud --yes`. Reinstall renders the current definitions and Compose removes orphan containers only in this Project, without deleting volumes. `self-host update` pulls images from saved definitions; it does not rename services.
- PostgreSQL belongs on suitable local storage; back up database and originals.

## Security

- Keep originals private and protect mobile/session credentials.
- Homarr uses an administrator-owned key with exactly `user.read` and `server.statistics`; album access and write permissions are not needed. Setup reuses an accepted key and repairs a missing recorded ID only after checking permissions, administrator identity and useful statistics.
- A lost or rejected Homarr secret with a recorded ID or an existing `self-host Homarr statistics` candidate requires a Manual step. Setup never deletes or rotates keys by name, including renamed recorded keys. Review Immich user settings > API Keys, restore the matching `IMMICH_HOMARR_API_KEY` and `IMMICH_HOMARR_API_KEY_ID` through the Project secret boundary, then run `self-host setup immich` followed by `self-host setup homarr`. Delete only a key confirmed to belong to this Project, never a same-name user key. A mismatched saved key and recorded ID also requires review rather than silently changing ownership.
- Setup records an issuance marker in `IMMICH_HOMARR_API_KEY_PENDING` before requesting a key, then quarantines the one-time secret and original response there before validating metadata. Pending evidence blocks replacement issuance, deletion and name-only adoption, including after transport errors or interrupted local writes. A recoverable secret is retained even when response metadata cannot be decoded; unresolved evidence requires a Manual step.
- Only an independently accepted `/api/api-keys/me` identity with a nonempty ID, the expected creation name and exactly the required permissions can recover a pending candidate. It must match any saved ID and readable issuance IDs, belong to the Project administrator and return useful native statistics and users before Setup writes the active credential. The ID is written before the active secret, and pending evidence is cleared only after both writes succeed. Existing different active credentials are never overwritten.
- Retain pending evidence and saved key/ID during Manual recovery; never clear an ID or pending record just to reissue. Restore only independently verified matching credentials through the Project secret boundary and retry Setup. If persistence fails, repair the private Project secret store before retrying; an issuance marker without a recoverable secret requires review, not automatic rotation.

Sources: [Immich key controller](https://github.com/immich-app/immich/blob/main/server/src/controllers/api-key.controller.ts), [key service](https://github.com/immich-app/immich/blob/main/server/src/services/api-key.service.ts) and [response schema](https://github.com/immich-app/immich/blob/main/server/src/dtos/api-key.dto.ts).
