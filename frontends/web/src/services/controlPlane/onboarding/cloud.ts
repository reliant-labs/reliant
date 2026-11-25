/**
 * Cloud implementation of the onboarding service. Talks to
 * `controlplane.v1.UserService` for the current-user / completion calls.
 *
 * forge sign-in (AUTH_PROVIDER "forge") reads the current user from the login
 * broker's /auth/session instead: reliant's broker adds the control-plane
 * account to it (`account`), and the control plane's Connect chain does not
 * read the session cookie yet. The account's `created_at` is the control-plane
 * account's creation (an imported user keeps theirs, not the import's), which
 * is what onboarding's returning-user heal compares daemons against.
 */

import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { getControlPlaneClient } from "../client";
import { syncReliantProvider } from "@/hooks/settings-queries";
import { isForgeAuth } from "@/lib/auth/forge-api";
import { fetchIdentity, type SessionAccount } from "@/lib/auth/native-login";
import type { OnboardingUser } from "./types";

/** Cache the in-flight GetCurrentUser promise for 30s so that the dozen-odd
 *  hooks that depend on it during initial render don't fan out into a dozen
 *  identical RPCs. */
let _userPromise: Promise<OnboardingUser | null> | null = null;
const USER_CACHE_TTL_MS = 30_000;

export async function getCurrentUser(): Promise<OnboardingUser | null> {
  if (_userPromise) return _userPromise;
  _userPromise = (async () => {
    if (isForgeAuth()) {
      const identity = await fetchIdentity();
      if (!identity.authenticated) return null;
      // A broker that names no account (an older reliant-api) falls through
      // to the rpc, which answers once the control plane reads the cookie.
      if (identity.account) return fromSessionAccount(identity.account);
    }
    const res = await getControlPlaneClient(UserService).getCurrentUser({});
    if (!res.user) return null;
    return {
      onboardingCompleted: res.user.onboardingCompleted,
      id: res.user.id,
      email: res.user.email,
      name: res.user.name,
      createdAtMs: res.user.createdAt
        ? Number(res.user.createdAt.seconds) * 1000
        : undefined,
    };
  })().finally(() => {
    setTimeout(() => {
      _userPromise = null;
    }, USER_CACHE_TTL_MS);
  });
  return _userPromise;
}

/** The onboarding user of the control-plane account the broker's session names. */
function fromSessionAccount(account: SessionAccount): OnboardingUser {
  const createdAtMs = account.created_at ? Date.parse(account.created_at) : NaN;
  return {
    onboardingCompleted: account.onboarding_completed,
    id: account.id,
    email: account.email,
    name: account.name,
    createdAtMs: Number.isFinite(createdAtMs) ? createdAtMs : undefined,
  };
}

/**
 * Forget the cached current user.
 *
 * Called on sign-out. `_userPromise` is module state keyed on nothing, so it
 * would otherwise answer the NEXT user with the previous user's record for up
 * to USER_CACHE_TTL_MS — including `onboardingCompleted`, which decides whether
 * a new account is onboarded at all.
 */
export function resetUserCache(): void {
  _userPromise = null;
}

/**
 * Mark onboarding done. Always the rpc, forge sign-in included: there it needs
 * the control plane's Connect chain to accept the session cookie.
 */
export async function completeOnboarding(
  data: Record<string, unknown>,
): Promise<void> {
  await getControlPlaneClient(UserService).completeOnboarding({
    // protobuf-es expects `JsonObject` for google.protobuf.Struct fields; the
    // shape is structurally identical to `Record<string, unknown>` as long as
    // the values themselves are JSON-serializable. Onboarding only stores
    // primitive strings (compute / modelProvider), so this round-trips fine.
    onboardingData: data as never,
  });
  _userPromise = null; // Force the next getCurrentUser to refetch.
}

export async function provisionManagedKey(): Promise<{ synced: boolean }> {
  // Provisions (or fetches) the per-user managed Reliant LLM key and writes it
  // to the local provider_api_keys store. Idempotent on the server.
  const result = await syncReliantProvider();
  return { synced: !!result.synced };
}
