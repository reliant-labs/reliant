// The forge login broker's session as the auth store's identity.
//
// In "forge" mode (lib/auth/forge-api.ts) nothing in the browser holds a
// token: the broker answers /auth/session from the HttpOnly session cookie.
// The auth store still runs every identity through its one serialized
// transition (store/authStore adoptIdentity), so the broker's Identity is
// installed as a store user carrying `app_metadata.provider = "forge"` and an
// empty access token — the same pattern the API-key session uses. Nothing
// outside the store reads that user: lib/auth/use-auth.ts derives the
// provider-neutral AuthIdentity from it, and getBearerToken() answers null for
// it, so no request ever sends `Bearer ` with the empty token.
//
// Transitional by design: the Supabase `User` shape leaves the store with
// Supabase itself (R10), when the store holds AuthIdentity directly.

import type { Session, User } from "@supabase/supabase-js";
import type { Identity } from "@/lib/auth/native-login";
import { FORGE_USER_PROVIDER } from "@/lib/auth/identity";

// The broker signs in only a verified address (an unverified sign-up gets a
// VerifySent answer and no session), so a forge identity is verified. The
// broker does not say when; the epoch stands for "verified, time unknown".
const VERIFIED_AT_UNKNOWN = new Date(0).toISOString();

/** The store user for a broker Identity, or null when it is not signed in. */
export function forgeUserOf(identity: Identity): User | null {
  if (!identity.authenticated || !identity.subject) return null;
  const account = identity.account;
  return {
    id: identity.subject,
    aud: "authenticated",
    email: identity.email || account?.email || undefined,
    email_confirmed_at: VERIFIED_AT_UNKNOWN,
    // The control-plane account's creation (imported users keep theirs), which
    // onboarding's returning-user heal compares daemon creation against.
    created_at: account?.created_at ?? "",
    is_anonymous: false,
    app_metadata: { provider: FORGE_USER_PROVIDER },
    user_metadata: {
      name: identity.name || account?.name || undefined,
      avatar_url: identity.picture || undefined,
    },
    identities: [],
  } as unknown as User;
}

/**
 * The store session for a forge user: no token (the cookie is the
 * credential), the user it belongs to (the store's reader check pairs them).
 */
export function forgeSessionOf(user: User): Session {
  return {
    access_token: "",
    refresh_token: "",
    token_type: "cookie",
    expires_in: 0,
    user,
  } as unknown as Session;
}

/** True for a store user installed from the broker's session. */
export function isForgeUser(user: User | null | undefined): boolean {
  return user?.app_metadata?.provider === FORGE_USER_PROVIDER;
}
