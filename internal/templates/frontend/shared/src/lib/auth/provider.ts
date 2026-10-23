// The credential seam, for a platform that has to hold a credential itself.
//
// ── Who this is for ───────────────────────────────────────────────────
//
// REACT NATIVE. The browser frontends do not use this file: they sign in
// natively (POST to the app's own API, HttpOnly session cookie back), so
// there is no token in JavaScript to hand around and nothing to implement.
// See src/lib/auth/native-login.ts.
//
// React Native has neither half of that — no automatic cookie jar on fetch,
// and no same-origin — so a mobile app carries its own credential and
// attaches it as a bearer token. This interface is where that credential
// comes from, and src/lib/connect.ts is where it gets attached
// (`setAuthTokenGetter`).
//
// ── What used to be here, and why it is gone ──────────────────────────
//
// This interface was written for a browser-side OIDC redirect flow, and
// most of it existed to serve that flow rather than the app:
//
//   - `login()` opened the redirect to the identity provider. The browser
//     never contacts the provider now, and a native sign-in screen calls
//     its own SDK, so nothing in the scaffold has a use for it.
//   - `onAuthStateChange()` let the OIDC provider push a session in
//     asynchronously after a silent restore, or push a sign-out after a
//     refresh failure. Nothing subscribes; the context reads state
//     directly and re-reads it with `refresh()`.
//   - `isAuthenticated()` / `isLoading()` duplicated state the context
//     already derives, and the loading half only existed because the
//     redirect flow had an async restore to wait on.
//   - `AuthUser.claims` carried the id token's decoded claims. There is no
//     id token in this tree to decode.
//
// Each one removed was a method whose only honest implementation had
// become a no-op — and a no-op on a seam is worse than an absent method,
// because callers build on it.

export interface AuthUser {
  id: string;
  email?: string;
  name?: string;
}

export interface AuthProvider {
  /**
   * The bearer token to present, or null when there is nothing to present.
   *
   * Resolved per call rather than cached by the transport, so a token that
   * rotates after sign-in or a refresh takes effect on the next request
   * with nothing to invalidate.
   */
  getToken(): Promise<string | null>;
  /** The current user, or null when signed out. Presentation only. */
  getUser(): AuthUser | null;
  /** Sign out. */
  logout(): Promise<void>;
}
