/**
 * The session port: who is signed in, as the shell and the keyboard host read
 * it, with no knowledge of how they signed in.
 *
 * The provider, the auth shape it takes, `useSignedIn` and `fixedSession`
 * are @reliantlabs/forge-web-runtime's `./session` (forge#2733), re-exported.
 * What stays here is reliant's `Session` view and the `useSession` that reads
 * it out of forge's session.
 *
 * The app's implementation (lib/auth/authStoreSession.tsx) is backed by the
 * auth store and Supabase; the public demo's is `fixedSession` (./fixed.ts), a
 * visitor who is always signed in. Code that only needs "is someone signed
 * in?" reads this port, so it builds in the demo bundle, where lib/auth,
 * store/authStore and @supabase are forbidden (vite-plugin-demo-bundle.ts).
 * That is why this module imports nothing but React and forge's `./session`,
 * which itself imports only React: __tests__/boundary.test.ts holds it to
 * that.
 *
 * The organization is deliberately not part of the session: org scope has
 * its own demo-safe source (activeOrgStore and the URL), and a second copy
 * here would be a second source of truth.
 */
import { useSession as useForgeSession } from "@reliantlabs/forge-web-runtime/session";
import type { SessionUser } from "@reliantlabs/forge-web-runtime/session";
import { useMemo } from "react";

export { SessionProvider, useSignedIn } from "@reliantlabs/forge-web-runtime/session";
export type { SessionAuth, SessionUser } from "@reliantlabs/forge-web-runtime/session";

/** What a reader sees. */
export interface Session {
  user: SessionUser | null;
  signedIn: boolean;
  loading: boolean;
}

/**
 * The current session. Signed out (and loading) outside a `SessionProvider`.
 * `user` is null unless someone is signed in, whatever a token's claims say.
 */
export function useSession(): Session {
  const { userId, email, name, isAuthenticated, isLoading } = useForgeSession();
  return useMemo(() => {
    const user: SessionUser | null =
      isAuthenticated && userId !== null ? { id: userId, ...(email !== null && { email }), ...(name !== null && { name }) } : null;
    return { user, signedIn: isAuthenticated, loading: isLoading };
  }, [userId, email, name, isAuthenticated, isLoading]);
}
