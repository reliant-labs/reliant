// The credential seam for a platform that must hold its own credential: React Native, which attaches
// a bearer token (src/lib/connect.ts, setAuthTokenGetter). Browser frontends never use it — they sign
// in natively with an HttpOnly cookie (native-login.ts). Depth: `forge skill load auth/frontend`.

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
