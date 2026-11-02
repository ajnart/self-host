# ntfy traps

## Pitfalls

- Guessable or anonymous topics can leak operational alerts when default access is not deny-all.
- Setup seeds an administrator and requests `expires: 0` when first issuing its administrator-level Homarr token; omitting expiry upstream defaults to 72 hours. A missing or nonzero returned expiry becomes Manual, not proof of non-expiration. The returned token is retained to avoid duplicate issuance; inspect its expiry privately before retrying Setup.
- Existing valid Homarr tokens and topics are preserved, including intentionally expiring tokens. Rejected saved tokens (HTTP 401/403) require Manual recovery; transport failures and unexpected statuses remain errors and never trigger replacement. Setup does not extend expiry or rotate credentials. Restore the saved token/topic through the Project secret boundary; any necessary replacement requires explicit approval.
- Separate least-privilege publisher credentials, topic ACLs, and notification delivery are not automated. Setup verifies access by polling without publishing notifications.
- An administrator token grants broad account privileges; ntfy access tokens are not individually scoped to topics.

## Security

- Set auth-file and auth-default-access=deny-all; the documented default is read-write even when authentication is enabled.
- Provision an administrator and separate least-privilege publisher account before enabling notification delivery or public exposure.

## Sources

- [ntfy token issuance and expiry API](https://github.com/binwiederhier/ntfy/blob/v2.28.0/server/server_account.go#L290)
- [ntfy explicit zero-expiry API test](https://github.com/binwiederhier/ntfy/blob/v2.28.0/server/server_account_test.go#L426)
