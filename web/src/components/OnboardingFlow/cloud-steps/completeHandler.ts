/**
 * Cloud completion handler — registers an onboarding completion handler
 * that calls control-plane RPCs when the user finishes onboarding.
 *
 * When injected into the OSS repo (via inject-cloud-onboarding.sh), this
 * file lives at OnboardingFlow/cloud-steps/completeHandler.ts and imports
 * relative to that position.
 */

import type { JsonObject } from "@bufbuild/protobuf";
import { registerOnboardingCompleteHandler } from "../useOnboardingComplete";
import type { LaunchPlan } from "../types";
import { getControlPlaneClient } from "./api";
import { UserService, DaemonService, DaemonType, DaemonSize } from "./gen/admin_pb";

function planToStruct(plan: LaunchPlan): JsonObject {
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
        ...(plan.repo.branch !== undefined && { branch: plan.repo.branch }),
      },
    }),
    ...(plan.localPath && { localPath: plan.localPath }),
    ...(plan.presetId && { presetId: plan.presetId }),
    ...(plan.useForge !== undefined && { useForge: plan.useForge }),
  };
}

registerOnboardingCompleteHandler(async (plan: LaunchPlan) => {
  const userClient = getControlPlaneClient(UserService);
  await userClient.completeOnboarding({
    onboardingData: planToStruct(plan),
  });

  if (plan.compute === "cloud_free_trial") {
    const daemonClient = getControlPlaneClient(DaemonService);
    await daemonClient.createDaemon({
      name: "onboarding-workspace",
      daemonType: DaemonType.MANAGED,
      size: DaemonSize.SMALL,
      gitRepo: plan.repo?.url ?? "",
      gitBranch: plan.repo?.branch ?? "main",
    });
  }
});
