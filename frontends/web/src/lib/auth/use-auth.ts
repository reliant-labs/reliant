// useAuth — the ONE way the app reads who is signed in, whichever provider
// signs them in (lib/auth/forge-api.ts: "supabase" or "forge").
//
// store/authStore stays the single identity state machine for both: every
// sign-in, refresh and sign-out goes through its serialized transition
// (adoptIdentity), which clears the previous account's state before the next
// is installed. What differs is only where the identity comes from: supabase-js
// in "supabase" mode, the login broker's /auth/session in "forge" mode
// (lib/auth/forge-session.ts). The store derives a provider-neutral
// AuthIdentity on every write, and this module is how the rest of the app
// reads it — never `useAuthStore((s) => s.user)`, whose Supabase `User` exists
// only for the Supabase-specific screens (linking an identity, the anonymous
// upgrade) and disappears with Supabase (R10).
//
// What a caller may do is decided by the servers, never by this: the identity
// is presentation and routing only.

import type { Session } from "@supabase/supabase-js";
import { useAuthStore } from "@/store/authStore";
import { authProviderName, type AuthProviderName } from "@/lib/auth/forge-api";
import type { AuthIdentity } from "@/lib/auth/identity";

export { identityOf, sameIdentity, FORGE_USER_PROVIDER, type AuthIdentity } from "@/lib/auth/identity";

/** What useAuth returns. */
export interface AuthView {
  identity: AuthIdentity | null;
  /** Signed in (any identity, a guest or an API key included). */
  signedIn: boolean;
  /** The store is restoring or switching an identity. */
  loading: boolean;
  /** The first restore finished. */
  initialized: boolean;
  authError: string | null;
  provider: AuthProviderName;
  signOut: () => Promise<void>;
}

/** The signed-in identity, or null. Re-renders only when it changes. */
export function useAuthIdentity(): AuthIdentity | null {
  return useAuthStore((s) => s.identity);
}

/** The signed-in account's id, or null. */
export function useAuthUserId(): string | null {
  return useAuthStore((s) => s.identity?.id ?? null);
}

/** Who is signed in and the auth lifecycle, for components. */
export function useAuth(): AuthView {
  const identity = useAuthStore((s) => s.identity);
  const loading = useAuthStore((s) => s.loading);
  const initialized = useAuthStore((s) => s.initialized);
  const authError = useAuthStore((s) => s.authError);
  const signOut = useAuthStore((s) => s.signOut);
  return { identity, signedIn: identity !== null, loading, initialized, authError, provider: authProviderName(), signOut };
}

/** The signed-in identity outside React (stores, services, interceptors). */
export function getAuthIdentity(): AuthIdentity | null {
  return useAuthStore.getState().identity;
}

/**
 * The signed-in identity once the first restore has finished, whichever
 * provider signs people in: the forge broker's /auth/session (the HttpOnly
 * cookie) or Supabase's stored session. For a page that must decide "is
 * anyone signed in in this browser?" before it navigates, and so cannot read
 * {@link getAuthIdentity} before the store has restored. The restore is the
 * store's one shared initialize, so this joins a run already in flight.
 */
export async function restoredAuthIdentity(): Promise<AuthIdentity | null> {
  const store = useAuthStore.getState();
  if (!store.initialized) await store.initialize();
  return getAuthIdentity();
}

/** Subscribe to identity changes outside React. Returns the unsubscribe. */
export function onAuthIdentityChange(listener: (next: AuthIdentity | null, previous: AuthIdentity | null) => void): () => void {
  return useAuthStore.subscribe((state, prev) => {
    if (state.identity !== prev.identity) listener(state.identity, prev.identity);
  });
}

/**
 * The bearer credential a direct `fetch` (not a Connect RPC) attaches as
 * `Authorization`, or null: always null in forge mode, where the session is
 * the cookie and the request must instead use `credentials: "include"`
 * (credentialedFetch, lib/auth/forge-api.ts). Callers branch on null, never
 * send `Bearer ` with nothing.
 */
export function getBearerToken(): string | null {
  const { identity, session } = useAuthStore.getState() as { identity: AuthIdentity | null; session: Session | null };
  if (!identity) return null;
  if (identity.provider === "forge" && !identity.isApiKey) return null;
  return session?.access_token || null;
}
