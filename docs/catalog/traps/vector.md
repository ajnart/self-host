# Vector traps

## Pitfalls

- Broad Docker or host log access can collect unintended sensitive content.
- The self-only recipe does not collect host or application logs; a running process or healthy API does not establish real ingestion.
- Its only pipeline is `internal_logs` to the console, not Loki. Review `apps/vector/config`, mounted at `/etc/vector/vector.yaml`, before adding approved sources, redaction and destinations; no App folders or Docker socket are mounted for log collection.

## Security

- Redact credentials and personal data before routing telemetry.
- The unauthenticated observability API must remain on loopback or another private trusted network when enabled.

## Source

- [Vector internal logs source](https://vector.dev/docs/reference/configuration/sources/internal_logs/).
