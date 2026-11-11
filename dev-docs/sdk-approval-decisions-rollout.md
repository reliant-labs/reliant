# Go SDK approval decisions: post-deploy check and rollback

`Approvals.Approve`, `Deny`, `BatchApprove` and `BatchDeny` in the Go SDK
(`sdk/go/reliant/inbox.go`) are client-only: they call
`reliant.v1.ApprovalService` and turn the server's refusal reasons
(`x-forge-error-reason`) into `ErrScopeMissing`, `ErrMachineCredential` and
`ErrApprovalNotPending`. Whether a call is allowed is decided by the server
alone. The unit tests (`sdk/go/reliant/approvals_decide_test.go`) run against a
stub server, so they prove the mapping, not what a real API server answers.

## Against a server without the approvals:decide enforcement

Until the server maps the four rpcs to `approvals:decide`
(`internal/grpc/interceptors/procedure_scopes.go`; today
`TestApprovalDecisionsStayOnReliantAPI` pins them to `reliant:api`) and the
person-only rule (`dev-docs/human-only-approvals-verification.md`) admits an
explicitly scoped token, an SDK decision is refused with `permission_denied`
(reason `person_required`, or `scope_missing` naming `reliant:api`). The SDK
reports `ErrPermissionDenied` and the approval stays pending: an older server
fails closed, so this SDK can ship before the server change.

## Post-deploy check (first environment that ships the server change, then prod)

Start a run in a test chat that pauses on an approval, so one is pending, and
note its request id. With a Go program built against the released SDK:

1. **Explicit scope decides.** Mint a token holding `approvals:decide` and call
   `Approvals.Approve(ctx, id)`. Expect success, the run continues, and
   `Approvals.List` no longer lists it as pending.
2. **Already resolved.** Call `Approve` on the same id again. Expect
   `errors.Is(err, reliant.ErrApprovalNotPending)`.
3. **`reliant:api` is not enough.** With a second pending approval, call
   `Approve` with a token holding only `reliant:api`. Expect
   `ErrScopeMissing` (and `ErrPermissionDenied`) with
   `Error.Scopes == ["approvals:decide"]`; the approval stays pending.
4. **Machine credential refused.** Repeat with a daemon (machine-bound)
   credential that also holds `approvals:decide`. Expect
   `ErrMachineCredential`; the approval stays pending.
5. Repeat 1-4 with `Deny`, and with `BatchApprove` / `BatchDeny` over two ids.

Any step that is allowed where a refusal is expected is a server defect: roll
back the server change (below), not the SDK.

## Rollback

- **The SDK.** It writes no server state and carries no migration. Revert the
  PR's commit on `main` (or pin consumers to the previous SDK release); calls
  already made decided real approvals and are not undone.
- **The server.** If enforcement misbehaves, revert the server change that
  mapped the rpcs to `approvals:decide`; decisions go back to the person-only
  rule and every SDK decision is refused again (fail closed, see above).
- **Tokens.** Revoke any access token granted `approvals:decide` for the check.
