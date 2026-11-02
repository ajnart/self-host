# Your Spotify

## App-only implementation boundary

This is the first App slice for #190, not native Homarr or live-ingestion
acceptance. Catalog/target registration, generated site/DCM data and counts are
coordinator-owned and deferred. No service has been started for this slice.

The official split images are `yooooomi/your_spotify_client:latest` and
`yooooomi/your_spotify_server:latest`, with the required `mongo:8` database.
The browser client listens on 3000 and the API on 8080. The App's main port is
the browser port; `api-port` is allocated independently and both published
ports retain `${BIND_IP}`. Homarr must eventually use the internal API Address
`http://your-spotify-server:8080`, never the browser client's HTML.

Mongo is not published. Its App-local `data` and `configdb` binds cover
`/data/db` and `/data/configdb`; ordinary non-purge removal keeps these files.
Do not attach another App's Mongo or adopt existing history automatically.
There are no media/download mounts, OAuth-token exports, database inspections
or unsupported PUID/PGID settings. The Project owns canonical Compose, private
credentials and backup/lifecycle operations.

## Server startup is held

The API service always remains deferred by self-host and has the
`your-spotify-manual` Compose profile with `restart: "no"`. The client has no
dependency that implicitly starts it. Client/Mongo startup does not establish a
working account. Setup and Check never start/restart the server or write its
database, and no boolean, token or empty-folder observation releases this hold.

This is necessary because the upstream image entrypoint runs migrations;
startup includes database repair and duplicate-history deletion, and the
server then polls Spotify for registered accounts. Reusing existing history
requires a separate reviewed backup/migration/startup decision, not an install
retry. Deliberately enabling the profile or directly starting that service is
an explicit operator action outside this slice. Automatic backend activation
is not implemented and no preservation claim covers upstream startup writes.

## Four different prerequisites

1. **Developer configuration:** existing `spotify_public` and `spotify_secret`
   inputs are private and optional, with missing values remaining Manual.
   They are never generated or passed to Homarr. Optional `client_endpoint`
   and `api_endpoint` are browser-reachable HTTP/HTTPS origins without embedded
   credentials, paths, queries or fragments; they do not configure a proxy or
   change Access. Defaults use the selected local/home-network Address and
   separately allocated API port. CORS stays restricted to the client origin.
2. **Spotify consent and eligibility:** personally configure the exact
   `<API_ENDPOINT>/oauth/spotify/callback`. Spotify requires HTTPS except
   literal loopback HTTP, not `localhost` or ordinary HTTP LAN Addresses.
   Development-mode apps require the owner's existing Premium eligibility and
   user allowlisting. Upstream requests profile/history read scopes **and**
   playback/playlist write scopes. No account is linked, subscription purchased,
   quota upgraded, Admin login substituted or OAuth consent inferred by Setup.
3. **Ingestion:** starting a consented account's backend polls recent history,
   initially the previous 24 hours and subsequently about every 120 seconds.
   That activity needs its own explicit review. A local identity, empty Stats
   or positive count does not prove provider entitlement, consent or a healthy
   ingestion grant. Never import private history or manufacture playback.
4. **Public-token sharing:** `homarr_api_key` is the existing raw public token
   from Settings > Account > Public token; `homarr_user_id` is the independently
   selected Account ID from Settings > Account infos. Both inputs are private.
   This token grants indefinite read-only guest access beyond aggregate counts;
   it is not a Spotify client/access/refresh secret or an aggregate-only scope.
   There is one such token per account: generating another revokes existing
   sharing links. Setup never generates, rotates, deletes or adopts a token.

OAuth/eligibility, ingestion and startup remain separate actionable Manual
steps even when local identity verification succeeds. No input in this initial
slice converts those person-owned prerequisites into automatic acceptance.

## Private read-only identity verification

`VerifyAccount` checks the explicitly selected Account ID and existing token.
It first reads only `LOG_LEVEL` through the owned API service. `warn` or `error`
is required: the upstream default `info`/`debug` Morgan logs expose the query
token. Generated Compose uses `warn` and bounded local logging; unsafe or
unreadable existing logging stops as Manual before any token request. Do not
silently overwrite a person's logging settings or start a held service to make
this guard pass.

The sole HTTP request is a bounded loopback `GET /me?token=...` against the API
port. Cookie fallback is disabled, redirects are not followed, and errors are
fixed text without raw transport details, response bodies, tokens or account
data. HTTP 200 alone is insufficient: require `status: true`, the exact selected
`user._id`, a nonempty `spotifyId` and the matching `publicToken`. A revoked key,
different account, missing fields or HTTP-200 `status: false` is not acceptance.
The response is never persisted or printed; no credential/identity journal,
database query, JWT minting or token fingerprint is introduced.

Recover only the known matching private inputs using existing owner-only
`--set-file` input transport. Review preservation first: re-installing can
replace Compose and is not a safe automatic recovery command. Keep saved
OAuth credentials, history, account settings and sharing links intact, then
retry `self-host setup your-spotify`. Missing credentials do not authorize
registering another account or generating another public token.

## Deferred connector work

Homarr's exact kind is `yourSpotify`, requiring `apiKey` as query `token`.
The supported surface is shared Stats: `songs`/`artists` in `count` and `time`
in `seconds` (upstream recorded duration milliseconds divided by 1000).
The connector, count/media envelope and issuance-helper integration await
reviewed #147/#150 contracts. No Stats/source aggregate reader, native widget,
freshness claim, rendered proof or installation-order/lifecycle acceptance is
implemented here. Runtime requires its own centrally assigned bounded slot;
provider/account actions remain separately consented.

## Primary sources

- [Official deployment and environment](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/README.md)
  and [Compose example](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/docker-compose-example.yml).
- [Official server entrypoint](https://github.com/Yooooomi/your_spotify/blob/fdd81ef89119159eac375fa2422d83e1765cff5e/apps/server/scripts/run/run.sh),
  [migration startup](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/server/src/migrations.ts),
  [repair/duplicate deletion](https://github.com/Yooooomi/your_spotify/blob/fdd81ef89119159eac375fa2422d83e1765cff5e/apps/server/src/tools/database.ts)
  and [automatic ingestion](https://github.com/Yooooomi/your_spotify/blob/fdd81ef89119159eac375fa2422d83e1765cff5e/apps/server/src/spotify/looper.ts).
- [Account and public-token routes](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/server/src/routes/index.ts),
  [guest/cookie middleware](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/server/src/tools/middleware.ts),
  [public-token UI](https://github.com/Yooooomi/your_spotify/blob/fdd81ef89119159eac375fa2422d83e1765cff5e/apps/client/src/scenes/Settings/PublicToken/PublicToken.tsx)
  and [Account ID UI](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/client/src/scenes/Settings/AccountInfos/AccountInfos.tsx).
- [Request logging/CORS](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/server/src/app.ts),
  [log-level behavior](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/server/src/tools/logger.ts)
  and [upstream OAuth scopes](https://github.com/Yooooomi/your_spotify/blob/4a48e06df4da97559239a886b3715088f49a7b7b/apps/server/src/tools/oauth/credentials.ts).
- [Mongo 8 persistent paths](https://github.com/docker-library/mongo/blob/master/8.0/Dockerfile).
- [Spotify redirect requirements](https://developer.spotify.com/documentation/web-api/concepts/redirect_uri)
  and [2026 development-mode eligibility](https://developer.spotify.com/documentation/web-api/tutorials/february-2026-migration-guide).
- [Homarr Your Spotify documentation](https://homarr.dev/docs/integrations/your-spotify/)
  and [Stats provider](https://github.com/homarr-labs/homarr/blob/0f0987d2ed013c93ce30c1c00786983d29f4b956/packages/integrations/src/stats/providers/your-spotify.ts).
