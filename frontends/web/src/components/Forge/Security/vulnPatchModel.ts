// Copyright (c) 2025 Reliant Labs

/**
 * The Security pages' Patch actions, free of React: whether a patch can be
 * started here at all, what a selection asks it to patch, and the exact run
 * it starts.
 *
 * A patch is the `vuln-patch` builtin workflow (internal/workflow/builtin/
 * vuln-patch.yaml). It makes a worktree of the Reliant project it runs in,
 * bumps the env's vulnerable dependencies in the forge project at `project`
 * inside that worktree, and opens one pull request on GitHub. So it is started
 * with ChatService.RunNow, IN THE REPOSITORY'S RELIANT PROJECT: the "Run now"
 * start, which names its project. RunService.StartRun names none (a headless
 * run lands in the caller's most recently active project), which would patch
 * whichever checkout the reader touched last.
 *
 * Where the forge project is: the Reliant project declaring its name
 * (Project.forge_project_name, ConsoleLayout's projectDeclaring), and the
 * repository of that project whose scanned forge apps (Repo.forge_apps)
 * include it. `project` is that repository's path in the project plus the
 * app's subpath in the repository; "" is the root.
 */

import type { Project } from "@/hooks/project-queries";
import type { FixState } from "@/services/controlPlane/vulnerabilities/types";

/** The builtin the buttons start. */
export const VULN_PATCH_WORKFLOW = "builtin://vuln-patch";

/**
 * vuln-patch's `patch_mode`. deterministic: `forge vuln patch` bumps first and
 * an agent takes over only when the bump fails verification. agent: the agent
 * does the whole patch.
 */
export type PatchMode = "deterministic" | "agent";

/** Whether a patch can be started from this project at all, before anything is selected. */
export type VulnPatchAccess =
  | { state: "checking" }
  | { state: "blocked"; reason: string }
  | { state: "ready"; projectId: string; projectPath: string };

/** What the access check reads of a repository (reliant.v1.Repo). */
export interface PatchRepo {
  name: string;
  relativePath: string;
  remoteUrl: string;
  forgeApps: readonly { name: string; subpath: string }[];
}

export interface VulnPatchAccessInput {
  forgeProject: string;
  /** The project list has answered (or failed). */
  projectsListed: boolean;
  /** The Reliant project declaring the forge project; null when none does. */
  project: Project | null;
  repos: { data: readonly PatchRepo[] | undefined; isLoading: boolean; isError: boolean };
  github: { connected: boolean; isLoading: boolean };
}

const joinPath = (...parts: string[]) =>
  parts
    .map((part) => part.replace(/^\/+|\/+$/g, ""))
    .filter(Boolean)
    .join("/");

const isGitHubRemote = (url: string) => /(^|[@/.])github\.com[:/]/i.test(url);

/** Where in the project's repositories the forge project is, or why it cannot be patched there. */
function locate(forgeProject: string, project: Project, repos: readonly PatchRepo[]): { path: string } | { blocked: string } {
  for (const repo of repos) {
    const app = repo.forgeApps.find((candidate) => candidate.name === forgeProject);
    if (!app) continue;
    if (!isGitHubRemote(repo.remoteUrl)) {
      return { blocked: `${repo.name || project.name}'s origin is not a GitHub repository, so a patch has nowhere to open its pull request.` };
    }
    return { path: joinPath(repo.relativePath, app.subpath) };
  }
  // No repository was scanned to hold it, but the project itself declares the forge project: its root.
  if ((project.forge_project_name ?? "").trim() === forgeProject) {
    if (project.remote_url && !isGitHubRemote(project.remote_url)) {
      return { blocked: `${project.name}'s origin is not a GitHub repository, so a patch has nowhere to open its pull request.` };
    }
    return { path: "" };
  }
  return { blocked: `No repository of ${project.name} holds the forge project ${forgeProject}, so there is nothing to patch it in.` };
}

/**
 * Whether a patch can start, in the order a reader fixes things: the
 * repository link, their permission in it, where the forge project is in it,
 * then GitHub. While a read is still out the answer is "checking", never a
 * blocked reason that the read would have contradicted.
 */
export function vulnPatchAccess({ forgeProject, projectsListed, project, repos, github }: VulnPatchAccessInput): VulnPatchAccess {
  if (!projectsListed) return { state: "checking" };
  if (!project) {
    return {
      state: "blocked",
      reason: `No repository is linked to ${forgeProject}: add the repository that holds its forge.yaml as a Reliant project to patch from here.`,
    };
  }
  if (project.viewer_access) {
    return { state: "blocked", reason: `${project.name} is shared with you to read: only its owner can start a patch in it.` };
  }
  if (repos.isLoading) return { state: "checking" };
  if (repos.isError || !repos.data) {
    return { state: "blocked", reason: `Could not read the repositories of ${project.name}, so where to patch ${forgeProject} is unknown.` };
  }
  const where = locate(forgeProject, project, repos.data);
  if ("blocked" in where) return { state: "blocked", reason: where.blocked };
  if (github.isLoading) return { state: "checking" };
  if (!github.connected) {
    return { state: "blocked", reason: "Connect GitHub to patch: a patch pushes a branch and opens its pull request with your GitHub account." };
  }
  return { state: "ready", projectId: project.id, projectPath: where.path };
}

/** What a selection reads of a finding. */
export interface PatchCandidate {
  advisoryId: string;
  fixState: FixState;
}

/** What a selection asks a patch to do, or why it cannot. */
export type PatchPlan = { ok: true; env: string; advisories: string[]; withoutFix: number } | { ok: false; reason: string };

/**
 * `env`: the environment whose release is patched, null when the scope is
 * every environment. Only findings with a fixed version are sent (a patch
 * moves a package to its fix; one with none has nowhere to go), each advisory
 * once.
 */
export function patchPlan(env: string | null, candidates: readonly PatchCandidate[], noun: "selection" | "advisory" = "selection"): PatchPlan {
  if (!env) return { ok: false, reason: "Choose one environment in the top bar: a patch fixes the release one environment runs." };
  if (candidates.length === 0) {
    return { ok: false, reason: noun === "advisory" ? `It is not found in what ${env} runs.` : "Select the findings to patch." };
  }
  const fixable = candidates.filter((candidate) => candidate.fixState === "fixed");
  if (fixable.length === 0) {
    return {
      ok: false,
      reason: noun === "advisory" ? `No fixed version of it exists for what ${env} runs.` : "None of the selected findings has a fixed version to move to.",
    };
  }
  return { ok: true, env, advisories: [...new Set(fixable.map((candidate) => candidate.advisoryId))], withoutFix: candidates.length - fixable.length };
}

/** vuln-patch's inputs, by their names in the workflow. */
export function vulnPatchInputs(projectPath: string, env: string, advisories: readonly string[], mode: PatchMode): Record<string, unknown> {
  return { project: projectPath, env, advisories: [...advisories], patch_mode: mode };
}

/** The run's first message: what the run is about, as its title and transcript say. */
export function vulnPatchMessage(env: string, advisories: readonly string[], mode: PatchMode): string {
  return `Patch ${advisories.join(", ")} in ${env}${mode === "agent" ? " with an agent" : ""}.`;
}
