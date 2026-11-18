// Copyright (c) 2025 Reliant Labs

/**
 * Dev sign-in: sign the web app in with a locally minted token instead of a
 * real Supabase login (control-plane#2735).
 *
 * WHO IT IS FOR. control-plane's dev stack (`forge env up dev` there) points
 * the app at the shared nonprod Supabase project, so signing in used to need a
 * real person's mailbox. Agents doing browser passes on a cloud machine have
 * none. control-plane's `auth mint-dev-token` already mints HS256 tokens its
 * dev platform-api accepts; with AUTH_DEV_JWT_SECRET the dev reliant api-server
 * accepts the same tokens (internal/auth WithDevSigner). This module is the
 * browser's half: it turns such a token into the app's session.
 *
 *     go run ./cmd/control-plane auth mint-dev-token --web-url http://localhost:<web port>
 *
 * prints `<web url>/#dev_sign_in=<token>`; opening it signs this browser in.
 * The sign-in screen also takes a pasted token.
 *
 * HOW IT IS GATED. Twice, and both at BUILD time:
 *   - `import.meta.env.DEV`: Vite's dev server only. Every production build
 *     (`vite build`, so every deployed site and every packaged desktop app)
 *     compiles this to false.
 *   - VITE_DEV_SIGN_IN=true: the build flag, which control-plane's dev KCL
 *     sets on reliant-web and its KCL refuses to render in any other env.
 * Neither is a runtime setting, and the browser holds no secret: a token is
 * only worth what the servers accept, and they accept the dev signer in the
 * dev tier alone.
 *
 * HOW THE SESSION IS HELD. As an ordinary supabase-js session in the client's
 * own storage, so every `supabase.auth.getSession()` caller, the auth store
 * and the transport see a signed-in user with no special case. supabase-js
 * reads a stored session without asking the server while it is unexpired. It
 * cannot refresh one it did not issue, so the session ends when the token
 * does (mint a longer one with `--ttl`), and signing out simply clears it.
 */
import type { Session } from "@supabase/supabase-js";

/** The URL fragment key a dev sign-in link carries its token in. */
export const DEV_SIGN_IN_FRAGMENT = "dev_sign_in";

/**
 * The refresh token a dev session carries. supabase-js requires one; refreshing
 * with it fails at the token's expiry, which signs the browser out.
 */
export const DEV_REFRESH_TOKEN = "dev-sign-in-cannot-refresh";

/** True only in a Vite dev server started with VITE_DEV_SIGN_IN=true. */
export function devSignInEnabled(): boolean {
  return import.meta.env.DEV && import.meta.env.VITE_DEV_SIGN_IN === "true";
}

interface DevTokenClaims {
  sub?: unknown;
  email?: unknown;
  role?: unknown;
  aud?: unknown;
  exp?: unknown;
  iat?: unknown;
}

function decodeClaims(token: string): DevTokenClaims {
  const parts = token.split(".");
  if (parts.length !== 3) throw new Error("That is not a token: a JWT has three dot-separated parts.");
  try {
    const b64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const json = atob(b64.padEnd(b64.length + ((4 - (b64.length % 4)) % 4), "="));
    const claims: unknown = JSON.parse(json);
    if (typeof claims !== "object" || claims === null) throw new Error("not an object");
    return claims as DevTokenClaims;
  } catch {
    throw new Error("That token's claims could not be read.");
  }
}

/**
 * The supabase-js session a dev token stands for. Nothing here is verified:
 * the servers verify the token on every call. It only refuses a token that
 * could never work (no sub, no exp, already expired), so the mistake is
 * reported here rather than as a signed-in app whose every call fails.
 */
export function sessionFromDevToken(token: string, nowMs: number = Date.now()): Session {
  const trimmed = token.trim();
  const claims = decodeClaims(trimmed);
  if (typeof claims.sub !== "string" || claims.sub === "") throw new Error("That token has no sub claim.");
  if (typeof claims.exp !== "number") throw new Error("That token has no exp claim.");
  const now = Math.floor(nowMs / 1000);
  if (claims.exp <= now) throw new Error("That token has expired; mint a new one.");
  const issuedAt = new Date((typeof claims.iat === "number" ? claims.iat : now) * 1000).toISOString();
  const role = typeof claims.role === "string" ? claims.role : "authenticated";
  return {
    access_token: trimmed,
    refresh_token: DEV_REFRESH_TOKEN,
    token_type: "bearer",
    expires_at: claims.exp,
    expires_in: claims.exp - now,
    user: {
      id: claims.sub,
      aud: typeof claims.aud === "string" ? claims.aud : "authenticated",
      role,
      email: typeof claims.email === "string" ? claims.email : undefined,
      // A dev identity's address is one we chose: verified, as the minted
      // token's email_verified claim says.
      email_confirmed_at: issuedAt,
      confirmed_at: issuedAt,
      app_metadata: { provider: "dev", providers: ["dev"] },
      user_metadata: {},
      created_at: issuedAt,
      updated_at: issuedAt,
      is_anonymous: false,
    },
  };
}

/**
 * Takes a dev sign-in token out of the address bar (`#dev_sign_in=<token>`),
 * scrubbing the fragment so the router, telemetry and history never hold it.
 * Null when the URL carries none.
 */
export function takeDevTokenFromUrl(): string | null {
  const hash = window.location.hash.startsWith("#") ? window.location.hash.slice(1) : window.location.hash;
  const token = new URLSearchParams(hash).get(DEV_SIGN_IN_FRAGMENT);
  if (token === null) return null;
  window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search);
  return token;
}

export interface AdoptDeps {
  /** Writes the session where the supabase client reads it at start. */
  write: (session: Session) => Promise<void>;
  /** Restarts the app on path, so it starts from the stored session. */
  restart: (path: string) => void;
}

const defaultDeps: AdoptDeps = {
  write: async (session) => {
    const { writeStoredSessionForDevSignIn } = await import("./supabase");
    await writeStoredSessionForDevSignIn(session);
  },
  restart: (path) => window.location.replace(path),
};

/**
 * Signs this browser in with token: stores it as the session, then restarts
 * the app on path so everything starts from it. Throws, storing nothing, when
 * dev sign-in is off or the token could never work.
 */
export async function adoptDevToken(token: string, path: string, deps: AdoptDeps = defaultDeps): Promise<void> {
  if (!devSignInEnabled()) throw new Error("Dev sign-in is off in this build (VITE_DEV_SIGN_IN).");
  const session = sessionFromDevToken(token);
  await deps.write(session);
  deps.restart(path);
}
