/**
 * Shared helpers for onboarding completion.
 */

import { logger } from "@/lib/logger";
import { isCloudCompute } from "./types";
import type { LaunchPlan } from "./types";

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

  const projectPath = await defaultProjectPath(isCloudCompute(plan.compute));
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

/**
 * The side effects of finishing onboarding. Does NOT navigate.
 *
 * This used to end with an unconditional `router.navigate({to: "/"})` through
 * the global singleton, which fired before a cloud caller could render
 * `DaemonConnectingGate` and dropped the user on `/` with onboarding complete
 * and no ACTIVE daemon. The fix at the time was a `navigate: false` option
 * threaded down here from four call sites.
 *
 * That option is gone. Navigation is `leaveOnboarding`'s job and only
 * `leaveOnboarding`'s job, so there is no flag left to pass wrongly — a caller
 * that forgets to exit stays on `/onboarding`, which is visible, rather than
 * exiting at the wrong moment, which is not.
 */
export async function finalizeOnboardingSideEffects(): Promise<void> {
  // The managed-key provision used to happen here, fire-and-forget, with its
  // failure going to a log line nobody reads. It is now `grant_ai_access`
  // inside `commitLaunchPlan`, awaited and reported, because a grant that
  // silently failed left the user's first message failing on a zero balance
  // with nothing on screen to explain it.
  //
  // Tour state lives in the URL. Clear any prior progress here; the exit puts
  // the first step in the search params (see leaveOnboarding).
  const { useTourStore } = await import("@/store/tourStore");
  await useTourStore.getState().resetTourProgress();
}

/**
 * Where to send the user when the guided tour ENDS.
 *
 * Distinct from the onboarding exit, which STARTS the tour by putting
 * `?tour=<first-step>` in the URL (see leaveOnboarding) — landing there on
 * finish would restart the tour the user just completed.
 *
 * The tour's last steps spotlight the workflow builder, so simply clearing the
 * `?tour` param left the user sitting on `/workflow/...`. Clearing the active
 * chat (which the finish path already did) only makes sense if we also land
 * them somewhere a chat can be started.
 *
 * Prefers the current project's route, matching CompletionStep's
 * "Let's get started" button, and falls back to `/` when no project is
 * selected — where ModernApp renders the picker.
 *
 * Returns navigate options rather than navigating, so the tour hook can drive
 * the router it is actually mounted under while non-React callers use the
 * global one. Both share this single definition of the destination.
 */
export function chatRouteAfterTour(
  projectId: string | undefined,
):
  | { to: "/project/$projectId"; params: { projectId: string }; search: Record<string, never> }
  | { to: "/"; search: Record<string, never> } {
  if (projectId) {
    return { to: "/project/$projectId", params: { projectId }, search: {} };
  }
  return { to: "/", search: {} };
}

/** Navigate to {@link chatRouteAfterTour} using the app's global router. */
export async function navigateToChatAfterTour(): Promise<void> {
  const [{ router }, { useProjectStore }] = await Promise.all([
    import("@/routes"),
    import("@/store/projectStore"),
  ]);

  const projectId = useProjectStore.getState().currentProject?.id;
  void router.navigate(chatRouteAfterTour(projectId) as never);
}
