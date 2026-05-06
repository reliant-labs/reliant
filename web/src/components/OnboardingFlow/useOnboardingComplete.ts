import { useCallback } from "react";
import type { LaunchPlan } from "./types";
import { logger } from "@/lib/logger";

type OnboardingCompleteHandler = (plan: LaunchPlan) => Promise<void>;
type LaunchTempParams = Record<string, unknown> & {
  __selectedPresets?: Record<string, string | null>;
};

const handlers: OnboardingCompleteHandler[] = [];

/**
 * Register a handler that runs when onboarding completes.
 * Cloud-specific actions (e.g. CompleteOnboarding RPC) register via this.
 */
export function registerOnboardingCompleteHandler(handler: OnboardingCompleteHandler) {
  handlers.push(handler);
}

/**
 * Hook that handles what happens when onboarding finishes.
 * Runs registered handlers, then applies the selected launch defaults.
 */
export function useOnboardingComplete() {
  const completeOnboarding = useCallback(
    async (plan: LaunchPlan) => {
      // Run all registered handlers (cloud steps register theirs)
      for (const handler of handlers) {
        try {
          await handler(plan);
        } catch (err) {
          logger.error("[OnboardingComplete] Handler failed", err);
          throw err;
        }
      }

      // Set workflow + params for the FIRST chat only (not as the user's global default).
      // These temp params are consumed by transferTempToChat() and cleared afterwards.
      if (plan.workflowId || plan.workflowParams || plan.selectedPresets) {
        const { useChatParamsStore } = await import("@/store/chatParamsStore");
        const launchParams: LaunchTempParams = {
          ...(plan.workflowParams ?? {}),
          ...(plan.workflowId
            ? { __selectedWorkflow: plan.workflowId }
            : {}),
          ...(plan.selectedPresets
            ? { __selectedPresets: plan.selectedPresets }
            : {}),
        };
        useChatParamsStore.getState().setTempNewChatParams(launchParams);
      }

      if (plan.initialPrompt) {
        const { useProjectStore } = await import("@/store/projectStore");
        const projectId = useProjectStore.getState().currentProject?.id;
        if (projectId) {
          const { useWorkspaceStateStore } = await import("@/store/workspaceStateStore");
          useWorkspaceStateStore
            .getState()
            .setNewChatDraft(projectId, plan.initialPrompt);
        } else {
          logger.warn("[OnboardingComplete] No current project for initial prompt draft");
        }
      }

      if (plan.launchTour) {
        const { useOnboardingChecklistStore } = await import(
          "@/store/onboardingChecklistStore"
        );
        await useOnboardingChecklistStore.getState().startWizard();
      }
    },
    [],
  );

  return { completeOnboarding };
}