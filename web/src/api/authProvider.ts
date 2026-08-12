/**
 * Pluggable auth-token source for the Connect interceptor chain.
 *
 * Why this exists
 * ---------------
 * `authInterceptor` and `unauthInterceptor` in `transport.ts` used to read
 * `localStorage` and call `supabase.auth.getSession()` directly. That hard-wires
 * two browser-only assumptions into the transport layer, which blocks three
 * things we want:
 *
 *   1. **Mobile web (`/m/*`)** — same browser APIs, so this is a no-op today,
 *      but it's the seam the other two need and it's cheapest to add now.
 *   2. **A native shell (React Native / Expo)** — no `localStorage`, no DOM;
 *      tokens live in Keychain / SecureStore behind an async API.
 *   3. **An embeddable, white-labeled chat/workflow widget** — the host app owns
 *      auth entirely and must be able to hand us a token getter. A module-global
 *      `localStorage` read is not something an embedder can override.
 *
 * The contract is deliberately two methods, not one. `unauthInterceptor` needs
 * to answer "is there a session at all?" *without* forcing a token refresh —
 * it only auto-signs-out when a 401 arrives while a session is believed active,
 * and using `getToken()` for that would trigger a refresh on every 401.
 *
 * Default behavior is byte-for-byte the previous inline logic (API key first,
 * then Supabase), so nothing changes for web/Electron until a caller opts in.
 */

import { logger } from "../lib/logger";

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
}

/** Key under which `ApiKeyLogin` stores a long-lived API key. */
const API_KEY_STORAGE_KEY = "reliant-api-key";

function readApiKey(): string | null {
  // `typeof` guard: this module is imported by `grpc-unauth.ts`, which runs in
  // contexts (SSR, tests, RN) where `localStorage` is absent.
  if (typeof localStorage === "undefined") return null;
  try {
    return localStorage.getItem(API_KEY_STORAGE_KEY);
  } catch {
    // Safari in Private Browsing throws on localStorage access rather than
    // returning null. Treat as "no API key" and fall through to Supabase.
    return null;
  }
}

/**
 * Browser/Electron provider: API key first, then the Supabase session.
 *
 * Supabase is imported lazily to preserve the existing cycle-break — a static
 * import would create `supabase → devAuth (grpc-unauth) → transport → supabase`.
 */
export const browserAuthTokenProvider: AuthTokenProvider = {
  async getToken() {
    const apiKey = readApiKey();
    if (apiKey) return apiKey;

    try {
      const { supabase } = await import("../lib/supabase");
      const {
        data: { session },
      } = await supabase.auth.getSession();
      return session?.access_token ?? null;
    } catch (error) {
      logger.error("[auth] Error getting session in interceptor:", {
        error: error instanceof Error ? error.message : String(error),
      });
      return null;
    }
  },

  async hasSession() {
    if (readApiKey()) return true;
    try {
      const { supabase } = await import("../lib/supabase");
      const {
        data: { session },
      } = await supabase.auth.getSession();
      return !!session?.access_token;
    } catch {
      // Fall through as "no session"; nothing to clear.
      return false;
    }
  },
};

// Module-level rather than React context on purpose: interceptors are built at
// transport-construction time, which happens outside React (and before the
// first render) in `grpc-client.ts`. A context would invert that lifecycle.
// `ReliantProvider` sets this during its initialization effect.
let _provider: AuthTokenProvider = browserAuthTokenProvider;

/**
 * Swap the token source. Call once during app/embed bootstrap, before the
 * first RPC. Passing null restores the browser default.
 */
export function setAuthTokenProvider(provider: AuthTokenProvider | null): void {
  _provider = provider ?? browserAuthTokenProvider;
}

/** Current token source. Interceptors call this per-request. */
export function getAuthTokenProvider(): AuthTokenProvider {
  return _provider;
}
