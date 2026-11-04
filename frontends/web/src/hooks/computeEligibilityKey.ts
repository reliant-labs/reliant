/**
 * The one React Query key for GetCurrentUserComputeEligibility, in a module of
 * its own so the Add machine sheet, onboarding and Settings share the cache
 * entry (a purchase refetches it once, for every reader) without importing
 * each other's hooks.
 */
export const computeEligibilityQueryKey = ["onboarding", "computeEligibility"] as const;
