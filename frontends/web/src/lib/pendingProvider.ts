import { readSetting, upsertStringSetting } from "@/lib/settingsPersistence";

import type { RedirectOAuthProvider } from "./oauth-providers";

/**
 * A subscription sign-in the user chose during onboarding but could not finish
 * there: the sign-in runs through a helper on their own computer, and a cloud
 * user reaches the model step before they have one running.
 *
 * Kept as a per-user setting (the same store the onboarding checklist uses), so
 * it follows the account and not the browser. Cleared by writing "": the
 * settings API has no delete a caller may rely on, and "" reads as "none".
 */
export const PENDING_PROVIDER_KEY = "onboarding.pending_provider";

const PENDING_IDS: readonly RedirectOAuthProvider[] = ["claude", "codex", "antigravity"];

export async function readPendingProvider(): Promise<RedirectOAuthProvider | null> {
  const read = await readSetting(PENDING_PROVIDER_KEY);
  if (read.status !== "found") return null;
  return PENDING_IDS.find((id) => id === read.value) ?? null;
}

export function setPendingProvider(provider: RedirectOAuthProvider): Promise<void> {
  return upsertStringSetting(PENDING_PROVIDER_KEY, provider);
}

export function clearPendingProvider(): Promise<void> {
  return upsertStringSetting(PENDING_PROVIDER_KEY, "");
}
