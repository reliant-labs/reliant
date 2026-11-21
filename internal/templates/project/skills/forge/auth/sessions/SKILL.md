---
name: sessions
description: Server-side sign-in sessions (forge/pkg/session) — the cookie holds an opaque id, the issuer's tokens stay sealed in auth_sessions and are refreshed before they expire, sign-out deletes the session; lifetimes, revocation, outages, account-page listing, CredentialSession, and adopting it in an existing project.
---

# Server-side sign-in sessions

A project with a database keeps sessions **server-side** (`forge/pkg/session`). The cookie (`__Host-<name>_session` outside development; still HttpOnly, `SameSite=Lax`, host-only) holds an opaque session id, `fs_` and 32 random bytes. The issuer's access, refresh and ID tokens stay in the `auth_sessions` table, which `forge` writes a migration for when it scaffolds the wiring. What you get:

- **Nobody is signed out mid-task.** The access token is renewed with the refresh token before it expires. That happens once per session, however many replicas ask, and the refresh token is rotated. The broker asks for `offline_access` (added to `oidc_scopes`), which is what earns a refresh token. A session ends after 30 days, or after 14 days unused (`session.Config.Lifetime` and `IdleTimeout` in `NewSessions`, `internal/app/providers.go`).
- **Sign-out is a delete.** `POST /auth/logout` deletes the session and revokes its refresh token at the issuer. The same replica refuses the cookie at once; another replica refuses it within `CacheTTL`, 15 seconds by default.
- **A copy of the table signs no one in.** Rows are keyed by a hash of the id, and the tokens are sealed (AES-GCM) under a second key derived from the id. There is no key to configure; the cost is that only a request carrying the cookie can use its tokens.
- **The interceptor resolves the id** (`Authenticator.UseSessions` in `NewUserAuth`): the id becomes its access token, which `SetupAuth`'s validator checks like any bearer token. The claims carry `auth.CredentialSession`, and `claims.IsSession()` is how a handler tells a browser session from a bearer token or an API key.
- **An outage is not a sign-out.** A store or issuer that cannot be reached answers 503 on `/auth/session` and an error on RPCs, and the session survives. While its access token is still valid it keeps working.
- **For an account page**, `(*session.Manager).List(ctx, subject)` lists a person's sessions, `RevokeHandle` signs one out, `RevokeSubject` signs out all of them, and `Info(ctx, id).ProvedAt` says when a credential was last proved.

A project with no database, or a `providers.go` scaffolded before this, keeps the older shape: the cookie holds the access token and ends when it expires. `RegisterLoginBroker`'s sessions are an option (`WithSessions`), so both shapes compile.

**To adopt sessions in an existing project:**

1. Paste `session.SchemaSQL` into a new migration (`forge db migration new create_auth_sessions`, or by hand). Its columns are the library's, so the ORM leaves the table alone.
2. In `internal/app/providers.go`, add the `Sessions` provider. Run `forge scaffold` in a scratch project to see it. Then add a `Sessions *Sessions` field to `UserAuthDeps` and `APIListenerDeps`, call `authenticator.UseSessions(d.Sessions.Manager)` when the Manager is non-nil, and pass `WithSessions(d.Sessions.Manager)` to `RegisterLoginBroker`.
3. If your `login_broker.go` predates `WithSessions`, take the session code from a freshly scaffolded broker: `setSession`, `accessToken`, `handleSession`, `handleLogout`, `SessionRefresher` and `SignedInRequest`.
4. Run `forge generate`. compose.go gains `newSessions` and the new Deps keys.

Cookies issued before the switch hold access tokens and still authenticate until they expire.
