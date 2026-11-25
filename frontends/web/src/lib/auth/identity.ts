// The provider-neutral identity the auth store derives on every write
// (store/authStore withIdentity) and lib/auth/use-auth.ts serves. Its own
// module, importing no store, so the store can import it without a cycle.

import type { User } from "@supabase/supabase-js";
import { API_KEY_USER_ID } from "@/lib/auth/apiKeyUser";
import type { AuthProviderName } from "@/lib/auth/forge-api";

/** Who is signed in, the same shape under either provider. */
export interface AuthIdentity {
  /** The account id (`sub`): the Supabase UUID, preserved by the forge import. */
  id: string;
  email: string | null;
  /** Display name, when the provider has one. */
  name: string | null;
  avatarUrl: string | null;
  /** A Supabase guest session. Never true in forge mode (guests are deferred, G9). */
  isAnonymous: boolean;
  /** Whether the address is verified. forge's broker signs in only verified addresses. */
  emailVerified: boolean;
  /** When the account was created, ms since the epoch; null when unknown. */
  createdAtMs: number | null;
  /** An API-key (machine credential) session (store setApiKeySession). */
  isApiKey: boolean;
  /** Which provider signed this identity in. */
  provider: AuthProviderName;
}

/** The marker forge-session puts on the user it installs (app_metadata.provider). */
export const FORGE_USER_PROVIDER = "forge";

function str(value: unknown): string | null {
  return typeof value === "string" && value !== "" ? value : null;
}

function ms(value: unknown): number | null {
  const s = str(value);
  if (!s) return null;
  const t = Date.parse(s);
  return Number.isFinite(t) ? t : null;
}

/** The provider-neutral identity of a store user (null when signed out). */
export function identityOf(user: User | null | undefined): AuthIdentity | null {
  if (!user) return null;
  const meta = (user.user_metadata ?? {}) as Record<string, unknown>;
  const provider: AuthProviderName = user.app_metadata?.provider === FORGE_USER_PROVIDER ? "forge" : "supabase";
  return {
    id: user.id,
    email: str(user.email),
    name: str(meta.full_name) ?? str(meta.name),
    avatarUrl: str(meta.avatar_url) ?? str(meta.picture),
    isAnonymous: !!user.is_anonymous,
    emailVerified: !!user.email_confirmed_at,
    createdAtMs: ms(user.created_at),
    isApiKey: user.id === API_KEY_USER_ID,
    provider,
  };
}

/** Same identity, field for field: keeps the derived object stable across token refreshes. */
export function sameIdentity(a: AuthIdentity | null, b: AuthIdentity | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return (
    a.id === b.id &&
    a.email === b.email &&
    a.name === b.name &&
    a.avatarUrl === b.avatarUrl &&
    a.isAnonymous === b.isAnonymous &&
    a.emailVerified === b.emailVerified &&
    a.createdAtMs === b.createdAtMs &&
    a.isApiKey === b.isApiKey &&
    a.provider === b.provider
  );
}
