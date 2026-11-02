# ntfy traps

## Pitfalls

- Guessable or anonymous topics can leak operational alerts when default access is not deny-all.
- Setup seeds an administrator and requests `expires: 0` when first issuing its administrator-level Homarr token; omitting expiry upstream defaults to 72 hours. Missing, null, malformed or nonzero returned expiry becomes Manual, not proof of non-expiration. The returned token is retained privately as `NTFY_HOMARR_TOKEN_PENDING`, never published as the active `NTFY_HOMARR_TOKEN` for Homarr. Valid JSON with corrupt expiry metadata still preserves its nonempty token. Retries and App.Check retain the hold even if topic access would succeed.
- Before issuance, Setup saves an `issuance-unconfirmed` pending placeholder. Ambiguous transport, status, decoding or persistence outcomes cannot trigger automatic reissuance. A recoverable returned token replaces the placeholder before any diagnostic; otherwise the placeholder remains for private reconciliation. Transport failures are reported through the redacting event logger before the Manual result; raw response bodies and decoding errors are not logged.
- Recover through `self-host credentials` privately and ntfy account settings: reconcile the issued token and verify its actual expiry before explicitly approving its use. Only then save the verified token as `NTFY_HOMARR_TOKEN` and remove `NTFY_HOMARR_TOKEN_PENDING` in the same private Project `.env` edit, then run `self-host setup ntfy`. If only the placeholder remains, reconcile the upstream issuance before explicitly approving any necessary replacement and saving it privately. Never clear pending evidence just to make Setup issue another token. Setup itself never promotes uncertain tokens, extends expiry, or replaces/reissues a pending token.
- Existing valid Homarr tokens and topics are preserved, including intentionally expiring tokens. Rejected saved tokens (HTTP 401/403) require Manual recovery; saved-token access transport failures and unexpected statuses remain errors and never trigger replacement. Setup does not extend expiry or rotate credentials. Restore the saved token/topic through the Project secret boundary; any necessary replacement requires explicit approval.
- Separate least-privilege publisher credentials, topic ACLs, and notification delivery are not automated. Setup verifies access by polling without publishing notifications.
- An administrator token grants broad account privileges; ntfy access tokens are not individually scoped to topics.

## Security

- Set auth-file and auth-default-access=deny-all; the documented default is read-write even when authentication is enabled.
- Provision an administrator and separate least-privilege publisher account before enabling notification delivery or public exposure.

## Sources

- [ntfy token issuance and expiry API](https://github.com/binwiederhier/ntfy/blob/v2.28.0/server/server_account.go#L290)
- [ntfy explicit zero-expiry API test](https://github.com/binwiederhier/ntfy/blob/v2.28.0/server/server_account_test.go#L426)
