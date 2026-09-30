import { projectGrpc } from "@/api/project-grpc";
import {
  createDaemon,
  listDaemons,
  type Daemon as CloudDaemon,
} from "@/services/controlPlane/daemon";
import { pickCloneTarget } from "@/components/Projects/cloneTargets";

/**
 * Add a GitHub repo as a project during onboarding.
 *
 * # Why this is a module rather than inline in the step
 *
 * The step used to run the clone sequence inline, and its test re-implemented
 * that sequence in the test file so it could assert on it. Two copies of the
 * orchestration meant the test could pass while the component did something
 * else entirely — it was pinning the copy, not the product. This is the one
 * implementation, and the test drives it.
 *
 * # What changed
 *
 * The four-call client chain (CloneRepo → CreateProject → MarkProjectInstalled,
 * plus an already-exists recovery) is gone. CreateProjectFromRepo does all of
 * it server-side in one call, so a failure can no longer leave a project with
 * no checkout or a checkout with no project.
 *
 * # What is still a separate call, and why
 *
 * The CreateDaemon refresh stays. It is not part of adding the project — it
 * writes the picked repo onto the daemon row so the controller's init container
 * sees `git_repo`, which is a control-plane concern about the MACHINE. It is
 * best-effort: the clone below populates the working tree regardless, so a
 * refresh failure must not block onboarding.
 */
export interface AddRepoProjectResult {
  projectId: string | undefined;
  clonedPath: string;
  daemonId: string;
  /** True when the checkout does NOT exist yet — the command is queued. */
  queued: boolean;
  /**
   * The machine the clone is waiting on, for user-facing copy. Named
   * "machine", not "daemon": onboarding must not leak internal vocabulary,
   * and the vocabulary test enforces that in the strings it reaches.
   */
  machineName: string;
}

export const ONBOARDING_DAEMON_NAME = "onboarding-daemon";
const DAEMON_TYPE_MANAGED = 1;
const DAEMON_SIZE_SMALL = 1;

/**
 * Chooses the machine to clone onto during onboarding.
 *
 * Shares `pickCloneTarget` with the project picker so the two surfaces cannot
 * disagree about which machine "the user's machine" means: running first, then
 * most recently used. Falls back to the first daemon of any status, because
 * onboarding's machine is frequently still booting and the clone is durably
 * queued — refusing there would strand the user at the last step.
 */
export function pickOnboardingDaemon(daemons: CloudDaemon[]): CloudDaemon | undefined {
  return pickCloneTarget(daemons) ?? daemons[0];
}

export async function addRepoProject({
  cloneUrl,
  branch,
  path,
  name,
}: {
  cloneUrl: string;
  branch: string;
  path: string;
  name: string;
}): Promise<AddRepoProjectResult> {
  const { daemons } = await listDaemons();
  const daemon = pickOnboardingDaemon(daemons);
  if (!daemon) {
    throw new Error("Your machine is still starting. Try again in a moment.");
  }

  // Best-effort, and deliberately before the clone: it tells the controller
  // which repo this machine is for. A failure here (plan limit, transient)
  // must not stop the clone, which populates the tree by itself.
  try {
    await createDaemon({
      name: ONBOARDING_DAEMON_NAME,
      daemonType: DAEMON_TYPE_MANAGED,
      size: DAEMON_SIZE_SMALL,
      gitRepo: cloneUrl,
      gitBranch: branch,
    });
  } catch (refreshErr) {
    console.warn("CreateDaemon refresh with git_repo failed (continuing with clone):", refreshErr);
  }

  // ONE call: starts the clone, creates the project, records where the
  // checkout will live. Errors propagate — the step shows them inline, and a
  // silent failure here is what left onboarding pointing at a project that
  // did not exist.
  const result = await projectGrpc.createProjectFromRepo({
    cloneUrl,
    daemonId: daemon.id,
    name,
    branch,
    path,
  });

  return {
    projectId: result.project?.id,
    clonedPath: result.projectDaemon?.path || path,
    daemonId: daemon.id,
    queued: result.queued,
    machineName: result.daemonName || daemon.name || daemon.hostname || "",
  };
}
