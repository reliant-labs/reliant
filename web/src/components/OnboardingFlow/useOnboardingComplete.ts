/**
 * Shared helpers for onboarding completion.
 */

import { logger } from "@/lib/logger";
import { onboardingService } from "@/services/controlPlane/onboarding";
import type { LaunchPlan, ModelProvider } from "./types";

const DEFAULT_PROJECT_NAME = "first_project";
const CLOUD_DEFAULT_PROJECT_PATH = `/home/workspace/projects/${DEFAULT_PROJECT_NAME}`;

async function defaultProjectPath(isCloud: boolean): Promise<string> {
  if (isCloud) return CLOUD_DEFAULT_PROJECT_PATH;
  try {
    const { listDirectory } = await import("@/api/filesystem-grpc");
    const result = await listDirectory("");
    const home = result.path || "~";
    return `${home}/Projects/${DEFAULT_PROJECT_NAME}`;
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
  const projectName = DEFAULT_PROJECT_NAME;

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

/** Options for {@link finalizeOnboardingSideEffects}. */
export interface FinalizeOptions {
  /**
   * Whether to navigate to `/` once the side effects are done.
   *
   * Cloud callers pass `false`: they still have to render
   * `DaemonConnectingGate`, which only exists to tell the user their machine
   * is still provisioning. Navigating here unmounted the step before the gate
   * could render, dropping the user on `/` with onboarding marked complete and
   * no ACTIVE daemon — the state ModernApp answers with ProjectPicker's
   * "Connect a daemon" / "Resume a daemon" screens.
   */
  navigate?: boolean;
}

export async function finalizeOnboardingSideEffects(
  modelProvider: ModelProvider | undefined,
  { navigate = true }: FinalizeOptions = {},
): Promise<void> {
  if (modelProvider === "reliant_credits") {
    onboardingService.provisionManagedKey().then(
      (result) => logger.info("[OnboardingComplete] Reliant provider synced", { synced: result.synced }),
      (err) => logger.warn("[OnboardingComplete] Reliant provider sync failed", err),
    );
  }

  // Tour state lives in the URL now — clear any prior progress and push the
  // user to the home route with the first step in the search params. The
  // wizard is mounted at the root and will render the step on arrival.
  const [{ useTourStore }, { router }, { ONBOARDING_STEPS }] = await Promise.all([
    import("@/store/tourStore"),
    import("@/routes"),
    import("@/components/Onboarding/constants"),
  ]);
  await useTourStore.getState().resetTourProgress();
  if (!navigate) return;
  void router.navigate({
    to: "/",
    search: { tour: ONBOARDING_STEPS[0].id },
  });
}

/**
 * Leave onboarding for the app, starting the post-onboarding tour.
 *
 * This is the deferred half of {@link finalizeOnboardingSideEffects}: callers
 * that passed `navigate: false` so they could show `DaemonConnectingGate` call
 * this from the gate's Continue. Setting the tour param HERE rather than
 * relying on it surviving in `prev` is what keeps the tour starting for cloud
 * users, whose finalize never touched the URL.
 */
export async function navigateAfterOnboarding(): Promise<void> {
  const [{ router }, { ONBOARDING_STEPS }] = await Promise.all([
    import("@/routes"),
    import("@/components/Onboarding/constants"),
  ]);
  void router.navigate({ to: "/", search: { tour: ONBOARDING_STEPS[0].id } });
}
