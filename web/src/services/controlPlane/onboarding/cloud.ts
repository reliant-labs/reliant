/**
 * Cloud implementation of the onboarding service. Talks to
 * `controlplane.v1.UserService` for the current-user / completion calls.
 */

import { UserService } from "@/gen/controlplane/v1/public/user_service_pb";
import { getControlPlaneClient } from "../client";
import { api } from "@/api/client";
import type { OnboardingUser } from "./types";

/** Cache the in-flight GetCurrentUser promise for 30s so that the dozen-odd
 *  hooks that depend on it during initial render don't fan out into a dozen
 *  identical RPCs. */
let _userPromise: Promise<OnboardingUser | null> | null = null;
const USER_CACHE_TTL_MS = 30_000;

export async function getCurrentUser(): Promise<OnboardingUser | null> {
  if (_userPromise) return _userPromise;
  _userPromise = (async () => {
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
  const result = await api.settings.syncReliantProvider();
  return { synced: !!result.synced };
}
