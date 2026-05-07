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
 * Ensure a project exists and is selected. Some onboarding paths (e.g. cloud
 * with daemonProvisioning, or github-connect when daemon isn't ready) skip
 * ProjectLocationStep's project creation. Without a project the main UI never
 * renders because hasProjects stays false.
 */
async function ensureProject(plan: LaunchPlan): Promise<string> {
  const { useProjectStore } = await import("@/store/projectStore");
  const store = useProjectStore.getState();

  // If a project is already selected, we're good
  if (store.currentProject) {
    return store.currentProject.id;
  }

  // Try to find an existing project matching the plan path
  if (plan.localPath) {
    await store.loadProjects();
    const existing = useProjectStore.getState().projects.find(
      (p) => p.path === plan.localPath,
    );
    if (existing) {
      await useProjectStore.getState().selectProject(existing);
      return existing.id;
    }
  }

  // Create a new project with plan data or sensible defaults
  const projectPath = plan.localPath || "/home/workspace/projects/reliant-project";
  const projectName = plan.projectName || "Reliant Project";

  logger.info("[OnboardingComplete] Creating project since none exists", {
    projectName,
    projectPath,
  });

  const created = await store.createProject({
    name: projectName,
    path: projectPath,
    description: "",
    is_git_repo: Boolean(plan.repo),
    default_branch: plan.repo?.branch || "main",
  });

  // createProject already sets currentProject, but ensure selection is complete
  await useProjectStore.getState().selectProject(created);
  return created.id;
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

      // Ensure a project exists and is selected before proceeding.
      // This covers cloud paths that skip ProjectLocationStep or defer creation.
      const projectId = await ensureProject(plan);

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

      if (plan.initialPrompt && projectId) {
        const { useWorkspaceStateStore } = await import("@/store/workspaceStateStore");
        useWorkspaceStateStore
          .getState()
          .setNewChatDraft(projectId, plan.initialPrompt);
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