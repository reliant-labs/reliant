"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Session context: the current user, derived from the auth state the app
// hands in and the bearer token's JWT claims. The runtime never imports the
// app's auth module — the app owns which provider it uses and may rename or
// delete that file, so the dependency points app → runtime, never back.
// Mount <SessionProvider auth={useAuth()}> inside your auth provider; read it
// with useSession().
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";

/**
 * The auth surface the session needs, and nothing more. Your app's auth
 * context value satisfies it structurally — pass `useAuth()` straight in.
 * Keeping this a plain prop (not an import) is what lets you swap, rename or
 * delete src/lib/auth/* without breaking a forge-owned file.
 */
export interface SessionAuth {
  user: { id: string; email?: string; name?: string } | null;
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
