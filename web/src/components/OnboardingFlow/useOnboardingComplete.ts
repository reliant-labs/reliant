import { useCallback } from "react";
import type { LaunchPlan } from "./types";
import { usePreferencesStore } from "../../store/preferencesStore";
import { logger } from "../../lib/logger";

type OnboardingCompleteHandler = (plan: LaunchPlan) => Promise<void>;

const handlers: OnboardingCompleteHandler[] = [];

/**
 * Register a handler that runs when onboarding completes.
 * Cloud-specific actions (e.g. CreateDaemon RPC) register via this.
 */
export function registerOnboardingCompleteHandler(handler: OnboardingCompleteHandler) {
  handlers.push(handler);
}

/**
 * Hook that handles what happens when the user clicks "Start" on the ReadyStep.
 * Runs registered handlers, then sets the selected workflow as the default.
 */
export function useOnboardingComplete() {
  const updatePreferences = usePreferencesStore((s) => s.updatePreferences);

  const completeOnboarding = useCallback(
    async (plan: LaunchPlan) => {
      // Run all registered handlers (cloud steps register theirs)
      for (const handler of handlers) {
        try {
          await handler(plan);
        } catch (err) {
          logger.error("[OnboardingComplete] Handler failed", err);
        }
      }

      // Set the selected workflow as the user's default
      if (plan.workflowId) {
        try {
          await updatePreferences({ defaultWorkflow: plan.workflowId });
        } catch (err) {
          logger.warn("[OnboardingComplete] Failed to set default workflow", err);
        }
      }
    },
    [updatePreferences],
  );

  return { completeOnboarding };
}
