# Homarr and external llama.cpp

Status: foundation only (refs #163). Native and Stats data are held.

## Connect

```
self-host connections add llamacpp --file input.json --yes
```

`input.json` is `{"endpoint":"http://<host>:<port>"}`: an explicit existing llama-server origin with no path, query, fragment or embedded credentials. llama-server defaults to port 8080; the endpoint must state its port, and self-host adds no port fallback or localhost substitute.

## What happens

- Only the exact Homarr kind `llamacpp` is accepted, and only without credentials. Homarr's llamacpp integration advertises no secrets and sends no authentication, so a server protected by an API key stays a Manual connection in Homarr. Supplying any credential is rejected before anything is stored.
- Setup stops at a Manual hold before creating a Homarr integration. Homarr's data reads call `/slots` and router `/metrics`, which can wake a sleeping model or autoload models; `/health` alone proves neither authentication nor model readiness.
- Self-host never loads, unloads or downloads models, runs inference, or changes the server's authentication, sleep or autoload settings.

## Recovery

- Status and `debug` show the hold hint. The saved connection is preserved.
- Rerun `self-host setup homarr` after an audited Homarr adapter reads without waking or loading models.
- To use the server now, add the integration in Homarr yourself and accept the wake and autoload effects.

## Disconnect

`self-host connections remove <id> --yes` removes only self-host's local record and any Homarr bindings it owns. The llama.cpp server is never contacted.

## Unblocking decisions

- A Homarr adapter with non-waking, non-autoloading reads, or an approved read-only surface limited to `/health`, `/props` and `/v1/models`.
- A controlled existing endpoint and an approved runtime slot for acceptance evidence.
