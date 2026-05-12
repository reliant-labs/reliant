/**
 * Cloud implementation of the onboarding service. Talks to the control-plane
 * admin server (controlplane.v1.UserService) and the local Reliant gRPC
 * SettingsService for managed-key provisioning.
 */

import {
  getCurrentUser as fetchCurrentUser,
  completeOnboardingRPC,
  type ControlPlaneUser,
} from "@/components/OnboardingFlow/api";

export async function getCurrentUser(): Promise<ControlPlaneUser | null> {
  const { user } = await fetchCurrentUser();
  return user ?? null;
}

export async function completeOnboarding(
  data: Record<string, unknown>,
): Promise<void> {
  await completeOnboardingRPC(data);
}

export async function provisionManagedKey(): Promise<{ synced: boolean }> {
  // Reliant provider now uses JWT auth directly — no key provisioning needed.
  return { synced: true };
}