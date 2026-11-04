# Human-only approvals: post-deploy verification

`ApprovalService.Approve`, `Deny`, `BatchApprove` and `BatchDeny` refuse any
credential software can hold (`auth.RequirePerson`, reliant #2123, #2175). For
identity-provider JWTs the split rests on one claim: `JWTValidator.ValidateToken`
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

Start a run in a test chat that pauses on an approval, so one approval is
pending, and note its request id.

1. Call `reliant.v1.ApprovalService/Approve` with the OAuth-client token
   (`Authorization: Bearer <token>`, body `{"requestId": "<id>"}`). Expect
   `permission_denied` with reason `person_required`; the api-server log has
   `[Approval] Refused a decision made with a credential software can hold`
   with `credential="OAuth client token"`; `ListApprovalsByChat` still lists
   the approval as pending.
2. Repeat with the CLI login's `rlat_` token (`reliant auth login`). Expect the
   same refusal with `credential="access token"`.
3. Approve it from the web app, then repeat with a second approval from the
   desktop app. Expect each to succeed and the run to continue.

## 3. If a token classifies differently

- **An OAuth-client token has no `client_id`, or step 2.1 succeeds** (fails
  open: software can answer approvals). Do not promote the release; in prod,
  roll back or fix forward immediately. The fix classifies on a claim only an
  app session carries (Supabase's `session_id`, checked against step 1's
  tokens) instead of on the absence of `client_id`, with a test signing both
  real payload shapes from step 1.
- **The app session carries `client_id`, or step 2.3 is refused** (fails
  closed: nobody can decide an approval, and every gated run waits until it
  times out). Same response: hold or roll back the release (the previous one
  has no gate), then fix the classifier the same way.

In both cases record the decoded claim sets (claims only, never the token) on
the issue that tracks the fix.
