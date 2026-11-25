import { useMemo, type ReactNode } from "react";

import { getAuthTokenProvider } from "@/api/authProvider";
import type { AuthIdentity } from "@/lib/auth/identity";
import { SessionProvider, type SessionAuth, type SessionUser } from "@/lib/session";
import { useAuthStore } from "@/store/authStore";

/**
 * The app's session (lib/session's port), backed by the auth store. App-only:
 * it lives in lib/auth/, which the demo bundle forbids, and the demo mounts
 * `fixedSession` instead.
 *
 * It reads the store's provider-neutral identity (lib/auth/identity.ts), so
 * it is the same session whichever provider signed the person in: Supabase,
 * or forge's login broker (AUTH_PROVIDER, lib/auth/forge-api.ts). Signed in
 * means the store has an identity, which it derives from its user on every
 * write.
 */
export function AuthStoreSession({ children }: { children: ReactNode }) {
  const identity = useAuthStore((state) => state.identity);
  const loading = useAuthStore((state) => state.loading);
  const auth = useMemo<SessionAuth>(
    () => ({ user: sessionUserOf(identity), isAuthenticated: identity !== null, isLoading: loading, getToken }),
    [identity, loading],
  );
  return <SessionProvider auth={auth}>{children}</SessionProvider>;
}

// The token the transport sends: an API key, else the Supabase session's.
// In forge mode the session is the broker's cookie, so it is null unless an
// API key is set (api/authProvider.ts cookieSessionTokenProvider).
const getToken = () => getAuthTokenProvider().getToken();

function sessionUserOf(identity: AuthIdentity | null): SessionUser | null {
  if (!identity) return null;
  return { id: identity.id, email: identity.email ?? undefined, name: identity.name ?? undefined };
}
