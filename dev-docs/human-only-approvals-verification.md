# Human-only approvals: post-deploy verification

> **Since the managed identity (design-auth-swap R1/R2):** an approval is decided
> by `auth.RequireApprovalDecider`, which admits a **browser session**
> (`CredentialBrowserSession`: a server-side session id read from the
> `__Host-reliant_session` cookie, which forge's `authn.Method.IsBrowserSession`
> also confirms), the **Supabase session** below until the cutover (R8), and the
> self-host shared key. For v1 compatibility, unbound `rlat_` tokens explicitly
> granted `approvals:decide` also succeed. Machine-bound tokens, the managed
> identity's token as a bearer, a session id sent as a bearer, and any token
> sent as the cookie are refused. `internal/grpc/interceptors/approval_session_test.go` is the
> matrix. The checks below still verify the Supabase half until it is removed.

`ApprovalService.Approve`, `Deny`, `BatchApprove` and `BatchDeny` use
`auth.RequireApprovalDecider`: app sessions and explicitly scoped, unbound
access tokens can decide; machine-bound tokens cannot, whatever their scopes.
For identity-provider JWTs the split rests on one claim: `JWTValidator.ValidateToken`
(`internal/auth/jwt.go`) classifies a token carrying `client_id` as
`CredentialOAuthClient` (refused) and one without it as `CredentialSession`
(accepted). Supabase's OAuth server puts `client_id` on the tokens it issues a
third-party client; a session from signing in to the app has none.

The unit tests (`internal/auth/credential_test.go`,
`internal/handlers/approval/approval_person_test.go`) sign their own JWTs, so
they prove the branch, not what the real provider issues. Run this plan once
against the first environment that ships the change, before promoting the
release to prod, and again in prod after it ships.

## 1. Inspect real tokens (no writes)

Decode only the payload (the middle segment) and never paste a token anywhere
it is stored or shared; both tokens act as the user.

- **App session.** Sign in to the web app; in devtools read the
  `access_token` of the `sb-<project-ref>-auth-token` entry in local storage
  (the desktop app holds the same session). Expect: `sub`, `session_id`, `aal`
  and `amr` present; **no `client_id`**.
- **OAuth client.** Connect a third-party OAuth client (an MCP client) through
  the Supabase OAuth server as the same user and take the access token it
  receives. Expect: **`client_id` present** and equal to the registered
  client's id.

```bash
cut -d. -f2 <<<"$TOKEN" | tr '_-' '/+' | base64 -d 2>/dev/null | jq '{sub, client_id, session_id, aal, amr}'
```

## 2. Exercise the gate

Use disposable approvals in a test chat owned by the verifying user. Start
runs that pause on approval and note their request ids. Repeat the matrix below
for `reliant.v1.ApprovalService/Approve`, `Deny`, `BatchApprove` and `BatchDeny`,
using fresh pending requests for each successful decision (and the RPC's
single-request or batch body as appropriate).

| Credential | Expected result |
| --- | --- |
| OAuth-client JWT | `permission_denied`, reason `person_required` |
| Machine-bound `rlat_` token, even with `approvals:decide` | `permission_denied`, reason `machine_credential_cannot_decide` |
| Unbound `rlat_` token without explicit `approvals:decide` (including one with only `reliant:api`) | `permission_denied`, reason `scope_missing` |
| Unbound `rlat_` token explicitly granted `approvals:decide` | Success: retained v1 compatibility path |
| Web or desktop app session | Success |

For bearer credentials use `Authorization: Bearer <token>`. For the managed
browser session use the sign-in cookie, not a session id as a bearer; until
cutover also exercise the Supabase app session described above. Do not infer
admission from a token being a CLI login: inspect its binding and explicit
scopes to choose the expected row.

After every refusal, verify `ListApprovalsByChat` still shows each request as
pending and its waiting run has not resumed. After every successful decision,
verify each request has the intended approved or denied state and its waiting
run resumes along the corresponding path. Check ApprovalService error metrics
and api-server logs for unexpected refusals or failures, without recording
credential values.

## 3. If a token classifies differently

- **An OAuth-client token has no `client_id`, or the OAuth-client row
  succeeds:** hold promotion and investigate the provider classifier. Record
  the decoded claim sets (claims only, never the token) on the tracking issue.
- **Any denied row succeeds or changes pending state, any admitted row is
  refused, or a successful decision does not resume its waiting run:** hold
  promotion; if already in prod, use the release pipeline to restore the
  previous release or ship a verified fix forward, then repeat this matrix.
  Record the credential class, explicit scopes, binding and RPC, never the
  credential value.

An explicitly scoped, unbound token succeeding is expected v1 compatibility,
not a rollback trigger. Judge failures against the matrix above, not a blanket
rule that every credential previously described as refused must still fail.
