---
name: account
description: Account settings on the login broker (forge/pkg/devidp AccountRoutes) — the account page, re-authentication and the recent-proof rule, changing the address (with a code to the new one) and the password (signs other devices out), connecting and disconnecting Google/GitHub, removing passkeys, listing and revoking sessions; the last-credential rule, the issuer calls behind each, and adopting it in an existing project.
---

# Account settings on the login broker

A project with server-side sessions (`auth/sessions`) serves a signed-in person's account settings from its own login broker, as JSON routes the app's settings page calls. They are `devidp.AccountRoutes`, mounted by `mountAccount` in `internal/app/login_broker.go`. Without server-side sessions they are not mounted: the routes read who is signed in, and when they last proved a credential, from the session record.

| Route | Does | Needs a recent proof |
|---|---|---|
| `GET /auth/account` | address and whether it is verified, whether there is a password, social links, declared providers (connected or not), passkeys, sessions (the current one flagged), `provedAt`, `reauthRequired` | no |
| `POST /auth/reauth` `{password}` | proves the password again; opens the window | — |
| `POST /auth/account/email` `{email}` | the issuer mails a code to the NEW address | yes |
| `POST /auth/account/email/confirm` `{code}` | proves that code: the new address is verified | no (the code is the proof) |
| `POST /auth/account/password` `{currentPassword?, newPassword}` | sets or changes the password, held to the password policy; signs every OTHER session out | yes, or `currentPassword` |
| `POST /auth/account/links/start` `{provider, returnTo?}` | `{redirect}` to the provider; the callback connects it | yes |
| `DELETE /auth/account/links/{provider}` | disconnects it | yes |
| `DELETE /auth/account/passkeys/{id}` | removes a passkey | yes |
| `GET /auth/account/sessions` | where the account is signed in | no |
| `DELETE /auth/account/sessions/{handle}` | signs one OTHER session out | no |
| `POST /auth/account/sessions/revoke-others` | signs every other session out | yes |

**Only a cookie session counts.** The subject every route acts on is the session record's own, never a request body or a token's claims. A bearer token (an `fs_` id included), an API key, or a cookie holding an access token instead of a session id gets 401 `not_signed_in`. Every mutating route is cross-site protected (`cross_site`, 403) and takes JSON only.

**Recent proof.** A route marked "yes" needs a credential proved within `ReauthWindow` (10 minutes by default): the sign-in itself, or `POST /auth/reauth`. Otherwise it answers 401 `reauth_required` with `methods`: `password` when the account has one, and `sign_in` (sign out and in again), which works for every account. So a stolen session past the window can read the page but cannot change how the account is reached. Re-authentication is rate limited per account and per client address; a wrong password is `wrong_password`.

**The last credential.** A link or passkey is removed only while another USABLE way in remains: otherwise 409 `last_credential`. Usable means usable under the LOGIN_POLICY in force: a password only where passwords are offered, passkeys only where passkeys are, a link only to a provider the policy declares, and a magic link to the account's verified address where magic links are offered. A stored password under a passkey-only policy does not keep the account reachable. The count and the removal run under the account's lock in the session store (`session.Manager.LockSubject`: a Postgres advisory lock for `PostgresStore`), so two removals on two replicas cannot each count the other's credential as the one that remains. A custom `session.Store` that does not implement `session.SubjectLocker` makes removals and address changes fail closed (502).

## What each one does at the issuer

- **Address** (`Client.ChangeEmail`, Zitadel `POST /v2/users/{id}/email` with `sendCode`). The issuer's semantics: the account's address IS the new one from this call on, unverified until the code is confirmed (`POST /v2/users/{id}/email/verify`), so a policy requiring a verified address refuses new sign-ins until then, while open sessions keep working. An address another account has is refused (`address_in_use`). Confirming renames the username when it was the address that named the account before the change, so the old address stops naming it. That address is recorded once (`forge.email_change_from` metadata) and kept across a second change and a failed one (A→B, then B→C: confirming C still renames from A). It is cleared only once the rename, or the decision not to rename (a username of its own), is settled. The mail links to `<app>/auth/account/email/confirm?code=…` (`EmailConfirmURL`), a page that posts the code signed in. In development no mail is sent: the link is logged, never returned. Under a LOGIN_POLICY with no mail delivery the change is refused (`mail_unavailable`). No mail goes to the old address: the issuer has no such notification.
- **Password** (`Client.SetPassword`, after the proof). Then `session.Manager.RevokeOthers` ends every other session; this one stays. If that fails (the store is unreachable), the answer is NOT a success: 502 `sessions_not_revoked` with `changed: true`, `otherSessionsRevoked: false`, and `retry` naming `/auth/account/sessions/revoke-others`, which finishes it. The ended sessions' refresh tokens are not revoked at the issuer, but nobody holds the ids they are sealed under any more.
- **Connect** (`StartIDPIntent` → the provider → the same `/auth/social/callback` page a sign-in uses). `AccountRoutes.SocialCallback` wraps the sign-in's callback handler: a callback for a link begun in this browser is completed as a link to THE SESSION THAT BEGAN IT (the binding cookie names its subject and handle), and anything else goes to the sign-in unchanged. A link signs nobody in, never switches account, refuses a provider account connected to another user (`social_taken`) and refuses an account whose address is unverified. A `pass_tokens` provider hands its tokens with Event `devidp.UpstreamEventLink`; under `RequireUpstreamTokens` a link whose tokens were not taken is removed again.
- **Disconnect** (`Client.RemoveIDPLink`), **passkeys** (`Client.RemovePasskey`), **sessions** (`session.Manager.List`, `RevokeHandle`, `RevokeOthers`). Another account's session handle answers 404, exactly like an unknown one; the current session is refused with `current_session` (sign out instead).

## Adopting it in an existing project

`login_broker.go` is yours, so a broker scaffolded earlier has no account routes:

1. Have server-side sessions first (`auth/sessions`).
2. Copy from a freshly scaffolded broker (`forge project new` in a scratch directory): the `Account*Path` / `ReauthPath` constants, the `account` field, `mountAccount`, `socialCallback` and `sessionID`. Call `broker.mountAccount(mux)` right after `broker.mountSignInMethods(mux, cfg)`, and mount the social callback through `b.socialCallback(...)`.
3. Build the settings page against the JSON above. The scaffolded frontend has none: it is yours to design (`frontend/design`).
