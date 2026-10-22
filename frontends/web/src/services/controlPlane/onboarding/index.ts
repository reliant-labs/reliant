/**
 * Onboarding service. Picks the cloud or local implementation at module load
 * based on whether a control plane is configured. Components and hooks
 * import `onboardingService` from here and never know which one they got.
 *
 * To add a new method: define it in `cloud.ts` and `local.ts` with the same
 * signature. The compile-time parity check below will fail the build if one
 * impl forgets the other.
 */

import { hasControlPlane } from "../config";
import * as cloud from "./cloud";
import * as local from "./local";

// Compile-time parity check: `local.ts` must structurally match `cloud.ts`.
// If you add a method to one, TypeScript fails until the other catches up.
const _localMatchesCloud: typeof cloud = local;
void _localMatchesCloud;

export const onboardingService = hasControlPlane ? cloud : local;
export type { OnboardingUser } from "./types";

/**
 * Forget any cached current-user record. Sign-out calls this so the next user
 * is never answered with the previous user's `onboardingCompleted`.
 *
 * Exported as a standalone function rather than reached through
 * `onboardingService` so callers that only need to invalidate — authStore —
 * don't pull in the whole service surface.
 */
export function resetUserCache(): void {
  onboardingService.resetUserCache();
}
