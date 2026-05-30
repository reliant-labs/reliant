/**
 * App-level onboarding-user shape. A minimal projection of
 * `controlplane.v1.User` (and a fallback for local mode that only tracks the
 * onboarding-completed flag). Fields that the public proto doesn't expose
 * (billing/IP gating) live on `ReliantEntitlement` instead and are queried
 * separately via `services/controlPlane/billing.getReliantState`.
 */
export interface OnboardingUser {
  onboardingCompleted: boolean;
  id?: string;
  email?: string;
  name?: string;
}
