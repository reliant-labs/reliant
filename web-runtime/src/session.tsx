"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Session context: the current user, derived from the auth state the app
// hands in and the bearer token's JWT claims. The runtime never imports the
// app's auth module — the app owns which provider it uses and may rename or
// delete that file, so the dependency points app → runtime, never back.
// Mount <SessionProvider auth={useAuth()}> inside your auth provider; read it
// with useSession(). A bundle with no auth code at all (a demo, a preview, a
// test) mounts <SessionProvider auth={fixedSession(user)}> instead.
//
// Also published as the "./session" subpath, so such a bundle can import the
// session API without the barrel. Keep this module's imports to react alone.
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";

/** The signed-in user, as the auth state hands it in. Presentation only. */
export interface SessionUser {
  id: string;
  email?: string;
  name?: string;
}

/**
 * The auth surface the session needs, and nothing more. Your app's auth
 * context value satisfies it structurally — pass `useAuth()` straight in.
 * Keeping this a plain prop (not an import) is what lets you swap, rename or
 * delete src/lib/auth/* without breaking a forge-owned file.
 */
export interface SessionAuth {
  user: SessionUser | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  getToken: () => Promise<string | null>;
}

/** Decoded JWT claims. Untyped beyond the fields the runtime reads. */
export interface JwtClaims {
  sub?: string;
  email?: string;
  name?: string;
  exp?: number;
  [key: string]: unknown;
}

export interface Session {
  userId: string | null;
  email: string | null;
  name: string | null;
  claims: JwtClaims | null;
  isAuthenticated: boolean;
  isLoading: boolean;
}

const EMPTY_SESSION: Session = {
  userId: null,
  email: null,
  name: null,
  claims: null,
  isAuthenticated: false,
  isLoading: true,
};

const SessionContext = createContext<Session>(EMPTY_SESSION);

/** base64url → string, tolerant of missing padding. Browser + jsdom safe. */
function base64UrlDecode(input: string): string {
  const pad = input.length % 4 === 0 ? "" : "=".repeat(4 - (input.length % 4));
  const base64 = input.replace(/-/g, "+").replace(/_/g, "/") + pad;
  // atob is a global in browsers and in Node 18+ (used during Next SSR).
  return atob(base64);
}

/**
 * decodeJwt extracts the claims from a JWT WITHOUT verifying the signature.
 * This is for display/routing only — the backend is the sole authority.
 * Returns null for malformed tokens.
 */
export function decodeJwt(token: string | null | undefined): JwtClaims | null {
  if (!token) {
    return null;
  }
  const parts = token.split(".");
  if (parts.length !== 3) {
    return null;
  }
  try {
    return JSON.parse(base64UrlDecode(parts[1] ?? "")) as JwtClaims;
  } catch {
    return null;
  }
}

export function SessionProvider({
  auth,
  children,
}: {
  auth: SessionAuth;
  children: ReactNode;
}): ReactElement {
  const { user, isAuthenticated, isLoading, getToken } = auth;
  const [claims, setClaims] = useState<JwtClaims | null>(null);

  useEffect(() => {
    let live = true;
    void (async () => {
      const token = await getToken();
      if (live) {
        setClaims(decodeJwt(token));
      }
    })();
    return () => {
      live = false;
    };
  }, [getToken, isAuthenticated]);

  const value = useMemo<Session>(() => {
    return {
      userId: user?.id ?? claims?.sub ?? null,
      email: user?.email ?? claims?.email ?? null,
      name: user?.name ?? claims?.name ?? null,
      claims,
      isAuthenticated,
      isLoading,
    };
  }, [user, claims, isAuthenticated, isLoading]);

  return (
    <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
  );
}

/** Current session — the signed-in user. Always defined (empty when logged out). */
export function useSession(): Session {
  return useContext(SessionContext);
}

/**
 * Whether someone is signed in: `useSession().isAuthenticated`, for the
 * components that need nothing else (a shell choosing its signed-in chrome, a
 * keyboard-shortcut scope). False while the session is still loading, and
 * outside a SessionProvider.
 */
export function useSignedIn(): boolean {
  return useContext(SessionContext).isAuthenticated;
}

export interface FixedSessionOptions {
  /**
   * The token getToken resolves to. Default null: a fixed session is not a
   * credential, and with no token no claims are decoded. Pass one only when
   * something that never reaches a real server (a mock transport) reads it.
   */
  token?: string | null;
}

/**
 * fixedSession is a SessionAuth whose user never changes: signed in as `user`,
 * never loading, and getToken resolving `opts.token` (null by default). It is
 * the session of a bundle that must carry no auth code at all: a public demo,
 * a guarded preview, a test, or a scaffold's mock mode.
 *
 *   <SessionProvider auth={fixedSession({ id: "demo", email: "demo@example.com" })}>
 *
 * It decides what to SHOW, never what is ALLOWED: nothing it returns is a
 * credential, and a backend must not accept it as one.
 *
 * The result is frozen and its getToken is one function for its lifetime, so
 * build it once (at module scope, or in useMemo/useState) and SessionProvider
 * resolves the token once instead of on every render.
 */
export function fixedSession(
  user: SessionUser,
  opts: FixedSessionOptions = {},
): SessionAuth {
  const token = opts.token ?? null;
  return Object.freeze({
    user: Object.freeze({ ...user }),
    isAuthenticated: true,
    isLoading: false,
    getToken: () => Promise.resolve(token),
  });
}
