---
name: handoff
description: Sign-in handoff (devidp.HandoffRoutes) — a person signed in to the web app gives a desktop/CLI app (RFC 8252 loopback or private-use redirect) or a sibling host (app.acme.com → console.acme.com) a NEW, independent session with PKCE one-time codes, never a parent-Domain cookie; the handoff_redirects allowlist, the consent page, native vs web redeem, oauth2.HandoffLogin, security properties, and adopting it in an existing project.
---

# Sign-in handoff

The session cookie is `__Host-` and host-only (`auth/sessions`), on purpose: no sibling site can read or plant it. So a desktop app driving the system browser, or a sibling host of the same product, cannot share it. A **handoff** gives that client a session of its OWN: the person, signed in on the web app, approves it once, and the client redeems a one-time code for a new server-side session with its own tokens. Nothing is shared: revoking either session leaves the other working.

It is OFF until `handoff_redirects` lists at least one redirect. It needs server-side sessions (`WithSessions`) and the `auth_handoffs` table.

## The flow

```
client   POST /auth/handoff/start   {code_challenge, code_challenge_method: "S256", redirect_uri, state}
         → {authorize_url: "<app>/auth/handoff?req=<id>", expires_in: 300}
browser  <app>/auth/handoff?req=<id>          your consent page, on the origin that holds the cookie
page     GET  /auth/handoff/authorize?req=…   → {account: {subject, email, name}, destination, redirect, kind}
                                               signed out → 401 {code: "not_signed_in", sign_in: "<app>/auth/sign-in?returnTo=…"}
page     POST /auth/handoff/authorize {req}   the explicit "Continue" (JSON → {redirect}; an HTML form post → 303)
browser  → <redirect_uri>?code=<one-time code>&state=<state>
client   POST /auth/handoff/redeem   {code, code_verifier, redirect_uri}
         https redirect   → a new session, its cookie set on the redeeming host: {authenticated: true, kind: "web"}
         native redirect  → {kind: "native", session: "fs_…", token_type: "Bearer", expires_at}
```

The routes are `devidp.HandoffStartPath`, `HandoffAuthorizePath`, `HandoffConsentPath` and `HandoffRedeemPath`; `internal/app/login_broker.go`'s `mountHandoff` mounts them through the same `devidp.GuardMux` as every sign-in route.

**How the new session gets its own tokens.** Every sign-in through `forge/pkg/devidp` finalizes the issuer's session (Zitadel v2) and seals it with the server-side session (`session.Tokens.IssuerSessionID/IssuerSessionToken`; only the id is in the clear, as `issuer_session`). At consent that issuer session finalizes a second auth request for the broker's client (`devidp.Client.ReissueTokens`): a fresh token set, with its own refresh token. A session created before this change kept no issuer session, so consent answers **401 `reauth_required`**: send the person to sign in again, then approve.

## The allowlist: `handoff_redirects`

Comma-separated, set per environment like any config field. Each entry is **exact**:

| Entry | Kind | Redeems into |
|---|---|---|
| `https://console.acme.com/auth/handoff/done` | web (a sibling host) | a session cookie on that host |
| `http://127.0.0.1/callback`, `http://[::1]/callback` | native (loopback, RFC 8252 §7.3: **any port** matches) | a bearer `fs_…` credential |
| `com.acme.desktop:/callback` | native (private-use scheme, which must contain a dot) | a bearer `fs_…` credential |

Refused: wildcards, fragments, queries, user info, upper-case schemes, plain http off loopback, and `localhost` (it can resolve off the device). One bad entry turns the whole handoff off, logged at ERROR. `devidp.ValidateHandoffRedirect` is the rule. A start whose `redirect_uri` is not listed is a 400 that does not echo it.

The allowlist is the broker's, not the issuer's: no handoff redirect is registered at the identity provider, because the issuer only ever redirects to the broker's own `oidc_redirect_uri`.

## Native apps: `oauth2.HandoffLogin`

```go
s, err := oauth2.HandoffLogin{
	Base:    "https://api.acme.com",          // where /auth/handoff/* is served
	OpenURL: openBrowser,                    // your launcher: the system browser, never a webview
	Out:     os.Stderr,
}.Run(ctx)
// s.Session is "fs_…": send it as Authorization: Bearer; keep it in the OS keychain.
```

It binds `127.0.0.1:0`, starts with an S256 challenge, opens the consent page, checks `state` before anything else on the callback, and redeems. List `http://127.0.0.1/callback` (or your `CallbackPath`). The credential is a session like any other: the server refreshes its tokens, `Manager.List` shows it, `RevokeHandle` ends it, and the app signs out with `POST /auth/logout` carrying `Authorization: Bearer fs_…`. The raw refresh token never leaves the server.

## Sibling hosts

The redeeming host must be served by the SAME broker (the same session store): two hostnames of one API, or a site whose `/_api` proxies to it. Its page calls `start` and `redeem` on its own origin (same-origin, so `CrossOriginProtection` passes), keeps the verifier in `sessionStorage` for the round trip, checks `state`, and on redeem receives its own `__Host-` cookie. No credential is in the JSON.

## The consent page is yours

`authorize_url` is `<app>` + `devidp.HandoffPagePath` (`/auth/handoff`), a page of your frontend behind the route guard. It reads `GET /auth/handoff/authorize?req=…` with credentials, shows "Continue as {email} to {destination}" (`kind: "native"` is "an app on this device"), and on the click posts the consent and navigates to the returned `redirect`:

```ts
const res = await fetch(`${api}/auth/handoff/authorize`, { method: "POST", credentials: "include",
  headers: { "Content-Type": "application/json" }, body: JSON.stringify({ req }) });
const body = await res.json();
if (res.ok) window.location.assign(body.redirect);
else if (body.code === "reauth_required") /* sign in again, then come back */;
```

A plain `<form method="post">` with a `req` field works too (the answer is a 303). Never auto-submit the form: the click is the consent.

## Security properties

- **Exact redirects**, from the allowlist; only the loopback port is free.
- **S256 PKCE only** (`plain` is refused); the verifier is compared in constant time.
- **One-time code**: 32 random bytes, 60 seconds, taken by the FIRST redeem attempt, so a wrong verifier or redirect burns it. Bound to the request's challenge and redirect, the broker's client, the subject and the approving session. Requests last 5 minutes and are consumed by consent.
- **Consent is a POST** behind `CrossOriginProtection` (exact origin; a same-site sibling is refused), carrying the primary's `SameSite=Lax` cookie. A GET never mints a code, so the consent is not login CSRF.
- **Revocation**: a signed-out primary cannot consent, and a pending code whose approving session was revoked is refused at redeem.
- **No fresh proof is inherited**: the new session is stamped `session.ChannelHandoff` (`Info(ctx, id).Channel`) and its `ProvedAt` is the primary's, so an action that wants a recent proof asks the new client for one.
- **Nothing usable at rest**: an `auth_handoffs` row is keyed by a hash of the request id or code and sealed (AES-GCM) under a key derived from it.
- **Rate limits**: start and redeem, per client address (`trusted_proxy_hops`).

## Adopting it in an existing project

1. Paste `session.HandoffSchemaSQL` into a new migration (`forge db migration new create_auth_handoffs`). `auth_sessions` is unchanged.
2. Declare `string handoff_redirects` (env `HANDOFF_REDIRECTS`) on the config your broker reads.
3. Copy from a freshly scaffolded `login_broker.go`: the `Handoff*Path` constants, `mountHandoff`, its call in `RegisterLoginBroker` after `mountSignInMethods`, `endPreviousSession` and `writeSessionCookie` (split out of `setSession`), and the bearer branch of `handleLogout`. Set `IssuerSession: tokens.IssuerSession.ID` in `setSession`'s `session.Meta`.
4. Add the consent page at `/auth/handoff`.
5. Set `handoff_redirects` in the environments that serve it.

Sessions created before the upgrade cannot hand off until the person signs in again (`reauth_required`); `devidp.SessionTokens` seals the issuer session on every sign-in from then on, with no other change.
