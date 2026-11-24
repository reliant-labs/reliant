import { useMemo, type ReactNode } from "react";
import type { User } from "@supabase/supabase-js";

import { getAuthTokenProvider } from "@/api/authProvider";
import { SessionProvider, type SessionAuth, type SessionUser } from "@/lib/session";
import { useAuthStore } from "@/store/authStore";

/**
 * The app's session (lib/session's port), backed by the auth store. App-only:
 * it lives in lib/auth/, which the demo bundle forbids, and the demo mounts
 * `fixedSession` instead.
 *
 * Signed in means the store has a user, exactly the check the shell and the
 * keyboard host made against the store before they read the port.
 */
export function AuthStoreSession({ children }: { children: ReactNode }) {
  const user = useAuthStore((state) => state.user);
  const loading = useAuthStore((state) => state.loading);
  const auth = useMemo<SessionAuth>(
    () => ({ user: sessionUserOf(user), isAuthenticated: user !== null, isLoading: loading, getToken }),
    [user, loading],
  );
  return <SessionProvider auth={auth}>{children}</SessionProvider>;
}

// The token the transport sends (an API key, else the Supabase session's).
const getToken = () => getAuthTokenProvider().getToken();

function sessionUserOf(user: User | null): SessionUser | null {
  if (!user) return null;
  return {
    id: user.id,
    email: user.email ?? undefined,
    name: user.user_metadata?.full_name ?? user.user_metadata?.name ?? undefined,
  };
}
