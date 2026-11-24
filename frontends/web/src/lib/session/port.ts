/**
 * The session port: who is signed in, as the shell and the keyboard host read
 * it, with no knowledge of how they signed in.
 *
 * The app's implementation (lib/auth/authStoreSession.tsx) is backed by the
 * auth store and Supabase; the public demo's is `fixedSession` (./fixed.ts), a
 * visitor who is always signed in. Code that only needs "is someone signed
 * in?" reads this port, so it builds in the demo bundle, where lib/auth,
 * store/authStore and @supabase are forbidden (vite-plugin-demo-bundle.ts).
 * That is why this module imports nothing but React:
 * __tests__/boundary.test.ts holds it to that.
 *
 * `SessionAuth` has the shape of @reliantlabs/forge-web-runtime's
 * `SessionAuth`, so an implementation of one satisfies the other and this
 * module can later re-export forge's. The organization is deliberately not
 * part of the session: org scope has its own demo-safe source (activeOrgStore
 * and the URL), and a second copy here would be a second source of truth.
 */
import { createContext, createElement, useContext, type ReactElement, type ReactNode } from "react";

/** The signed-in person, as much as the shell shows. */
export interface SessionUser {
  id: string;
  email?: string;
  name?: string;
}

/** The auth state a session needs, and nothing more (the shape of forge-web-runtime's `SessionAuth`). */
export interface SessionAuth {
  user: SessionUser | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  /** The bearer token, or null when there is none. */
  getToken: () => Promise<string | null>;
}

/** What a reader sees. */
export interface Session {
  user: SessionUser | null;
  signedIn: boolean;
  loading: boolean;
}

// No provider above the reader: nobody is known to be signed in yet, the
// same answer forge-web-runtime's useSession gives outside its provider.
const NO_SESSION: SessionAuth = {
  user: null,
  isAuthenticated: false,
  isLoading: true,
  getToken: async () => null,
};

const SessionContext = createContext<SessionAuth>(NO_SESSION);

/** Hands `auth` to every `useSession` / `useSignedIn` below it. */
export function SessionProvider({ auth, children }: { auth: SessionAuth; children: ReactNode }): ReactElement {
  return createElement(SessionContext.Provider, { value: auth }, children);
}

/** The current session. Signed out (and loading) outside a `SessionProvider`. */
export function useSession(): Session {
  const auth = useContext(SessionContext);
  return { user: auth.user, signedIn: auth.isAuthenticated, loading: auth.isLoading };
}

/** Whether someone is signed in: what the shell's frame and the keyboard host decide on. */
export const useSignedIn = (): boolean => useContext(SessionContext).isAuthenticated;
