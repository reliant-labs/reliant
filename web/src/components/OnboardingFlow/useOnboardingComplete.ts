/**
 * Shared helpers for onboarding completion.
 */

import { logger } from "@/lib/logger";
import { onboardingService } from "@/services/controlPlane/onboarding";
import type { LaunchPlan, ModelProvider } from "./types";

const CLOUD_DEFAULT_PROJECT_PATH = "/home/workspace/projects/reliant-project";

async function defaultProjectPath(isCloud: boolean): Promise<string> {
  if (isCloud) return CLOUD_DEFAULT_PROJECT_PATH;
  try {
    const { listDirectory } = await import("@/api/filesystem-grpc");
    const result = await listDirectory("");
    const home = result.path || "~";
    return `${home}/Projects/reliant-project`;
  } catch {
    return CLOUD_DEFAULT_PROJECT_PATH;
  }
}

/**
 * Ensure a project exists and is selected. Without a project the main UI
 * never renders (ModernApp shows ProjectPicker instead), which breaks the
 * post-onboarding tour spotlight targets.
 */
export async function ensureProject(plan: Partial<LaunchPlan>): Promise<string> {
  const { useProjectStore } = await import("@/store/projectStore");
  const store = useProjectStore.getState();

  if (store.currentProject) {
    return store.currentProject.id;
  }

  await store.loadProjects();
  const refreshed = useProjectStore.getState();
  if (refreshed.projects.length > 0) {
    const first = refreshed.projects[0];
    await refreshed.selectProject(first);
    return first.id;
  }

  const isCloud = plan.compute === "cloud_free_trial";
  const projectPath = await defaultProjectPath(isCloud);
  const projectName = "Reliant Project";

  logger.info("[OnboardingComplete] Creating default project", { projectName, projectPath });

  const created = await store.createProject({
    name: projectName,
    path: projectPath,
    description: "",
    is_git_repo: false,
    default_branch: "main",
  });

  await useProjectStore.getState().selectProject(created);
  await store.loadProjects();

  return created.id;
}

export async function finalizeOnboardingSideEffects(modelProvider: ModelProvider | undefined): Promise<void> {
  if (modelProvider === "reliant_credits") {
    onboardingService.provisionManagedKey().then(
      (result) => logger.info("[OnboardingComplete] Reliant provider synced", { synced: result.synced }),
      (err) => logger.warn("[OnboardingComplete] Reliant provider sync failed", err),
    );
  }

  const { useTourStore } = await import("@/store/tourStore");
  await useTourStore.getState().startWizard();
}
