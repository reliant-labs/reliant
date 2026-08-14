/**
 * `?onboarding-credits=eligible|ineligible` — the deliberate escape hatch for
 * exercising either branch of the onboarding eligibility UI on purpose.
 *
 * WHY THIS EXISTS AT ALL: both onboarding steps used to force eligibility to
 * `true` whenever `getIsDev()` was set. That made a dev build claim the user
 * was funded when the server said otherwise — so the "start" control was
 * enabled, the click proceeded, and provisioning failed at the daemon gate
 * instead of being refused up front. It also hid the coupon field behind the
 * same false claim, which is exactly the state a coupon exists to rescue.
 *
 * A URL parameter is the right shape for this and an environment flag is not:
 * it is per-tab and explicit, so it can never be the ambient default that
 * silently diverges dev from production. When it is absent — the normal case,
 * including in dev — the UI shows what the SERVER reports.
 */
export type ForcedEligibility = "eligible" | "ineligible" | null;

export function getForcedEligibility(): ForcedEligibility {
  if (typeof window === "undefined") return null;
  const value = new URLSearchParams(window.location.search).get(
    "onboarding-credits",
  );
  if (value === "eligible") return "eligible";
  if (value === "ineligible") return "ineligible";
  return null;
}
