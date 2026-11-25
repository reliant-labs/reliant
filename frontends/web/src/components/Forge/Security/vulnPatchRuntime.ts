// Copyright (c) 2025 Reliant Labs

/**
 * The Patch actions' reads and their one write, against Reliant: the project
 * list, the project's repositories, the caller's GitHub credential or
 * connection, and ChatService.RunNow (the "Run now" start, as the workflow
 * page's Run now dialog makes it). The decisions are vulnPatchModel.ts's.
 *
 * One module so the forge demo's bundle can replace it whole: its twin,
 * vulnPatchRuntime.demo.ts, has no Reliant projects, no GitHub and no runs
 * (vite-plugin-demo-bundle.ts, "demo twins"). Keep the two files' exports the
 * same.
 */

import * as chatActions from "@/hooks/chat-actions";
import { useConnections } from "@/hooks/connection-queries";
import { useProjects } from "@/hooks/project-queries";
import { useListRepos } from "@/hooks/repo-service-hooks_gen";
import { useGitHubCredential } from "@/hooks/useGitHubCredential";
import { runPath } from "@/lib/paths";

import { VULN_PATCH_WORKFLOW, vulnPatchAccess, vulnPatchInputs, vulnPatchMessage, type PatchMode, type VulnPatchAccess } from "./vulnPatchModel";

/**
 * Whether a patch of `forgeProject` can start, from the Reliant project the
 * console resolved for it (ForgeConsole.projectId; null when none declares it).
 */
export function useVulnPatchAccess(forgeProject: string, reliantProjectId: string | null): VulnPatchAccess {
  const { projects, isLoading: listing } = useProjects();
  const project = reliantProjectId ? (projects.find((candidate) => candidate.id === reliantProjectId) ?? null) : null;
  const repos = useListRepos({ projectId: project?.id ?? "" }, { enabled: !!project && !project.viewer_access });
  // The caller's GitHub matters only once a project could be patched: an app no
  // project here declares (the forge tour's sample app among them) reads none of it.
  const patchable = !!project && !project.viewer_access;
  const credential = useGitHubCredential({ enabled: patchable });
  const connections = useConnections(patchable ? "github" : undefined);
  // A personal connection counts; an org's is used only where a run names it, which this one does not.
  const connected = credential.hasToken || (connections.data ?? []).some((c) => c.status === "active" && c.ownerKind === "user");
  return vulnPatchAccess({
    forgeProject,
    projectsListed: projects.length > 0 || !listing,
    project,
    repos: { data: repos.data?.repos, isLoading: repos.isPending, isError: repos.isError },
    github: { connected, isLoading: !connected && (credential.isLoading || connections.isLoading) },
  });
}

export interface VulnPatchStart {
  projectId: string;
  projectPath: string;
  env: string;
  advisories: readonly string[];
  mode: PatchMode;
}

export interface StartedVulnPatch {
  chatId: string;
  /** The run's page. */
  href: string;
}

/** Start vuln-patch in the repository's project, attended (manual.run), and say where its run is. */
export async function startVulnPatch({ projectId, projectPath, env, advisories, mode }: VulnPatchStart): Promise<StartedVulnPatch> {
  const { chat } = await chatActions.runNow({
    projectId,
    workflow: VULN_PATCH_WORKFLOW,
    message: vulnPatchMessage(env, advisories, mode),
    inputs: vulnPatchInputs(projectPath, env, advisories, mode),
  });
  const search = new URLSearchParams({ project: chat.projectId || projectId });
  return { chatId: chat.id, href: `${runPath(chat.id)}?${search.toString()}` };
}
