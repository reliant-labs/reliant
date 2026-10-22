import { projectGrpc } from "@/api/project-grpc";
import { cloudPathForRepo, repoNameFromUrl } from "@/lib/cloudProjectPath";
import {
  createEnvironment,
  describeError,
  type CreateEnvironmentArgs,
} from "@/services/controlPlane/environments";
import type { GitRepo } from "@/services/controlPlane/git";

/**
 * Create a cloud machine and, when the user picked one, add a repository to it.
 *
 * # Why the repo is a second call, not a CreateDaemon field
 *
 * CreateDaemon accepts `git_repo`, and this dialog used to send a free-text
 * URL there — but nothing clones from that field. The workspace controller
 * stopped consuming it when cloning moved to the daemon's `git.clone` command,
 * so the dialog had to say "Automatic cloning is coming in a follow-up
 * release" next to a box that did nothing. The field is still sent, as a
 * record of which repo the machine was made for; the CLONE goes through
 * CreateProjectFromRepo, the one path every other add-a-repo surface uses
 * (onboarding's addRepoProject, the project picker's Clone dialog).
 *
 * # Why it does not wait for the machine to boot
 *
 * CreateProjectFromRepo enqueues the clone durably against the daemon id and
 * returns `queued` — the command waits for the machine however long it takes
 * to attach. So "create, then add the repo" is two calls back to back, with no
 * polling and no second mechanism for "once it's ready".
 *
 * # Why a failed repo add does not fail the whole action
 *
 * By the time the add runs, the machine EXISTS. Reporting the pair as one
 * failure would invite the user to press Create again and get a second
 * machine. So the machine's creation is the result, and the repo's outcome is
 * reported beside it.
 */
export type RepoOutcome =
  | { kind: "none" }
  | { kind: "added"; projectName: string; projectId?: string; queued: boolean }
  | { kind: "failed"; projectName: string; message: string };

export interface CreateMachineResult {
  daemonId: string;
  machineName: string;
  repo: RepoOutcome;
}

export async function createMachine({
  machine,
  repo,
}: {
  machine: Omit<CreateEnvironmentArgs, "gitRepo" | "gitBranch">;
  repo: GitRepo | null;
}): Promise<CreateMachineResult> {
  const branch = repo ? repo.defaultBranch || "main" : "";
  const daemon = await createEnvironment({
    ...machine,
    gitRepo: repo?.cloneUrl,
    gitBranch: repo ? branch : undefined,
  });
  const daemonId = daemon?.id ?? "";
  const machineName = daemon?.name || machine.name;

  if (!repo) {
    return { daemonId, machineName, repo: { kind: "none" } };
  }

  const projectName = repoNameFromUrl(repo.cloneUrl) || repo.fullName;
  if (!daemonId) {
    return {
      daemonId,
      machineName,
      repo: {
        kind: "failed",
        projectName,
        message: "the new machine's id was not returned",
      },
    };
  }

  try {
    const result = await projectGrpc.createProjectFromRepo({
      cloneUrl: repo.cloneUrl,
      daemonId,
      name: projectName,
      branch,
      path: cloudPathForRepo(repo),
    });
    return {
      daemonId,
      machineName: result.daemonName || machineName,
      repo: {
        kind: "added",
        projectName,
        projectId: result.project?.id,
        queued: result.queued,
      },
    };
  } catch (err) {
    return {
      daemonId,
      machineName,
      repo: {
        kind: "failed",
        projectName,
        message: describeError(err, "the repository could not be added"),
      },
    };
  }
}
