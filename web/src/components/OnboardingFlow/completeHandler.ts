/**
 * Cloud completion handler — registers an onboarding completion handler
 * that calls control-plane RPCs when the user finishes onboarding.
 */

import { registerOnboardingCompleteHandler } from "./useOnboardingComplete";
import type { LaunchPlan } from "./types";
import { completeOnboardingRPC } from "./api";

function planToStruct(plan: LaunchPlan): Record<string, unknown> {
  return {
    intent: plan.intent,
    compute: plan.compute,
    codeSource: plan.codeSource,
    workflowId: plan.workflowId,
    modelProvider: plan.modelProvider,
    ...(plan.repo && {
      repo: {
        provider: plan.repo.provider,
        url: plan.repo.url,
        branch: plan.repo.branch,
      },
    }),
    ...(plan.localPath && { localPath: plan.localPath }),
    ...(plan.presetId && { presetId: plan.presetId }),
    ...(plan.useForge !== undefined && { useForge: plan.useForge }),
  };
}

// Only register the cloud completion handler if the control-plane API is configured
if (import.meta.env.VITE_CONTROL_PLANE_API_URL) {
  registerOnboardingCompleteHandler(async (plan: LaunchPlan) => {
    await completeOnboardingRPC(planToStruct(plan));
  });
}