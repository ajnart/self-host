# Unbound traps

## Pitfalls

- An open recursive resolver creates abuse and availability risk.
- Port 5335 serves DNS over TCP and UDP, not a web page. The App declares `NoWebUI`, so its protocol endpoint must not become a browser Address, proxy route or Homarr browser tile. This does not change DNS ports or configure any client's resolver.

## Security

- Restrict recursion to intended clients and keep controls private.

## Source

- [Publisher's recursive DNS image documentation](https://github.com/madnuttah/unbound-docker/blob/main/README.md).
