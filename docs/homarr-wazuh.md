# Homarr and an external Wazuh

Homarr can show agent counts from an existing Wazuh server through an external connection (`self-host connections add wazuh`). self-host installs nothing for Wazuh: no Catalog App, image, ports, volumes or enrollment.

## Profile

First slice is the server API only: username and password of a dedicated account with `agent:read` on `agent:id:*`. Counts reflect that account's visible agent scope. It feeds three Stats on one `stats` widget: `agents`, `activeAgents`, `disconnectedAgents` (unit `count`).

- Endpoint: the HTTPS server API origin (default port 55000), without path, query, fragment or credentials. Not the indexer (9200) or dashboard.
- `manager:read` is not required. Indexer credentials, alert Stats, the native Agents widget and notifications are out of scope: they expose agent identities or findings and need separate approval.
- Credentials go only through the owner-only private input; they never appear in output.

## Checks

- Before any Homarr change, self-host authenticates without following redirects and reads the raw agent summary. It rejects a nonzero `error`, missing, null, negative or fractional counters and counters that do not add up to the total. Explicit zeros are valid.
- Stats must be genuinely advancing refreshes with `count` units, finite integral values and `activeAgents + disconnectedAgents <= agents`.

## Hold

Homarr's native Wazuh adapter's redirect and trusted-TLS behavior has not been audited, so Setup stops as Manual after the probe and does not create the integration. Same as Proxmox: preserve the connection, do not bypass TLS, use a proxy, or rotate credentials. Retry `self-host setup homarr` once an audited adapter establishes fail-closed behavior.

## Recovery

Confirm the endpoint is the server API, the account's `agent:read` scope and certificate trust, then rerun `self-host setup homarr`. Removing the connection releases only self-host's ownership; Wazuh is untouched.

## Evidence

- Source/fake: validators and input contract tested here.
- Native, rendered, lifecycle: not run (needs a controlled endpoint and the hold above resolved).
