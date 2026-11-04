# Plant-it traps

## Pitfalls

- Upstream removed the server in [1.0.0](https://github.com/MDeLuise/plant-it/releases/tag/1.0.0); this App runs the official 0.10.0 server, which gets no further updates. Do not upgrade it to 1.x: there is no migration.
- The documented `mysql:8.0` is absent from the current supported MySQL tags; the App is hidden until a maintainer accepts the legacy image and its database major.
- The web client asks for the API address once and keeps it in that browser. Enter the API Address (default port 8080), not the web UI Address. Behind HTTPS, the API must be separately reachable and trusted.
- Plants, photos and history live in `./db` and `./upload-dir`. Keep both in Backup; removing the App must not delete them.
- There is no way to tell an empty instance from a rejected login, so the owner is registered by a person in the web client. Setup never signs up, resets passwords or triggers email codes.
- Homarr's Stats read the owner's own counts (plants, species, photos, diary entries) with the owner's login. It is an account-wide credential, not a scoped read-only key.

## Recovery

- A lost or rejected owner login needs an explicit ownership decision: restore `PLANT_IT_HOMARR_USERNAME`, `PLANT_IT_HOMARR_PASSWORD` and `PLANT_IT_OWNER_ID` through the Project secret boundary, then run `self-host setup plant-it` and `self-host setup homarr`.
- Never create another owner to make Homarr show data: it would read an empty garden.

## Security

- Protect the owner login, the JWT secret and the database passwords; keep SMTP, ntfy and Gotify unset.
