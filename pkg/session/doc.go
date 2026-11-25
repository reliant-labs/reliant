// Package session keeps browser sign-in sessions SERVER-SIDE: the cookie holds
// an opaque session id, and the tokens an identity provider issued (access,
// refresh, ID token) stay on the server, where they are refreshed before the
// access token expires.
//
// # Why
//
// A cookie that holds the access token itself dies when the token does, so a
// signed-in person is signed out mid-task once an hour (or whatever the
// issuer's access-token lifetime is), and the refresh token that could have
// renewed it is thrown away because there is nowhere safe to keep it. Holding
// both tokens behind an opaque id fixes that and gives two more properties
// for free: signing out is a DELETE that takes effect on the next request
// rather than a wait for a token to expire, and a person's sessions can be
// listed and revoked.
//
// # The id is the only secret
//
// [NewID] mints "fs_" followed by 32 random bytes (base64url). Nothing else
// about a session is secret on its own, and the store never sees the id:
//
//   - a row is keyed by a hash derived from the id (HKDF-SHA256), so reading
//     the store does not yield a cookie that works;
//   - the tokens are sealed (AES-256-GCM) under a second key derived from the
//     same id, so reading the store does not yield a usable token either.
//
// There is therefore no encryption key to configure, rotate or leak. The cost
// is deliberate: only a request that carries the cookie can read its
// session's tokens, so no background job can refresh or use them. Expired
// rows are swept without opening them ([Manager.Sweep]).
//
// # Pieces
//
//   - [Store] holds sealed rows: [NewMemoryStore] for development and tests
//     (one process, lost on restart), [NewPostgresStore] for everything else
//     (its table is [SchemaSQL]; the project owns the migration).
//   - [Refresher] redeems a refresh token at the issuer. forge/pkg/devidp
//     supplies one for a forge identity; any OAuth 2.0 issuer fits.
//   - [Manager] ties them together: [Manager.Create] at sign-in,
//     [Manager.AccessToken] on every request (refreshing before expiry, once
//     per session across all replicas, rotating the refresh token),
//     [Manager.Revoke] at sign-out, [Manager.List] and [Manager.RevokeHandle]
//     for an account's "where am I signed in" page.
//
// forge/pkg/auth's Authenticator.UseSessions routes a credential shaped like a
// session id through a Manager to the access token it stands for, which is
// then validated exactly like a bearer token. The scaffolded login broker
// (internal/app/login_broker.go) sets the cookie.
package session
