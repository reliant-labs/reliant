// How the SDK gets a bearer token. Two methods, injected: the host owns
// storage, refresh and sign-out (a browser session, a server-held API key, a
// native keychain) and the SDK only asks.

/** Where the transport gets bearer tokens and session-liveness answers. */
export interface AuthTokenProvider {
  /**
   * Bearer token for the `Authorization` header, or null when unauthenticated.
   * May refresh under the hood; called on every outbound RPC, so
   * implementations should cache.
   */
  getToken(): Promise<string | null>;

  /**
   * Whether a session is *believed* active, used to decide if a 401 should
   * trigger auto-sign-out. Must NOT trigger a token refresh — a 401 storm
   * would otherwise stampede the refresh endpoint.
   */
  hasSession(): Promise<boolean>;

  /**
   * Exchange the session for a fresh access token after the backend rejected
   * the current one. Returns the new token, or null when the session cannot
   * be refreshed (a long-lived API key, a revoked refresh token, no session).
   * Called at most once per burst of rejections.
   */
  refresh(): Promise<string | null>;

  /**
   * Whether the credential is a long-lived API key rather than a session.
   * A key cannot be refreshed, so a 401 that only says a person-only RPC
   * refused a machine credential must not sign it out; every other 401 does.
   * Optional: a provider without it is treated as a session.
   */
  isApiKey?(): boolean;
}

/** A provider for a fixed credential: an API key or a token the host rotates by replacing the provider. */
export function staticTokenProvider(token: string, options: { apiKey?: boolean } = {}): AuthTokenProvider {
  return {
    getToken: async () => token,
    hasSession: async () => true,
    refresh: async () => null,
    isApiKey: () => options.apiKey ?? true,
  };
}
