/**
 * Local-only fallback for the onboarding service. Used when no control plane
 * is configured (`VITE_CONTROL_PLANE_API_URL` unset). The onboarding gate is
 * tracked in localStorage so the OSS flow can complete without ever calling
 * the cloud admin server.
 */

import type { OnboardingUser } from "./types";

const ONBOARDING_COMPLETED_KEY = "reliant-local-onboarding-completed";

function readCompleted(): boolean {
  try {
    if (localStorage.getItem(ONBOARDING_COMPLETED_KEY) === "true") return true;
    // Legacy: older versions stored onboarding state under a different key
    const legacy = localStorage.getItem('reliant-onboarding');
    if (legacy) {
      try {
        const parsed = JSON.parse(legacy);
        if (parsed?.state?.state === 'completed') return true;
      } catch { /* ignore */ }
    }
    return false;
  } catch {
    // localStorage unavailable (private mode, sandboxed iframe). Treat as
    // not-yet-completed so the user is sent through the onboarding flow.
    return false;
  }
}

function writeCompleted(value: boolean): void {
  try {
    if (value) localStorage.setItem(ONBOARDING_COMPLETED_KEY, "true");
    else localStorage.removeItem(ONBOARDING_COMPLETED_KEY);
  } catch {
    // No-op if localStorage is unavailable.
  }
}

export async function getCurrentUser(): Promise<OnboardingUser | null> {
  return { onboardingCompleted: readCompleted() };
}

export async function completeOnboarding(
  _data: Record<string, unknown>,
): Promise<void> {
  writeCompleted(true);
  // Clean up legacy key
  try { localStorage.removeItem('reliant-onboarding'); } catch { /* no-op */ }
}

export async function provisionManagedKey(): Promise<{ synced: boolean }> {
  // No control plane → there's nothing to provision. The user picked a model
  // provider that requires cloud-side key issuance, but in local-only mode
  // they need to bring their own key (or pick a different provider).
  return { synced: false };
}
