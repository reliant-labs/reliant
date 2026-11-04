/**
 * The project list for tests, where the app keeps it: the ListProjects cache
 * entry under `projectsKey()` (hooks/project-queries.ts) that `useProjects`,
 * `useProject`, `fetchProjects` and `getCachedProjects` all read.
 *
 * `seedProjects(client, projects)` writes that entry in the client a test
 * renders with (and `queryClient` from lib/query-client for code that reads
 * it imperatively). Rows are written in the frontend `Project` shape the
 * tests already have and converted to the proto row ListProjects answers.
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import type { QueryClient } from "@tanstack/react-query";

import { ListProjectsResponseSchema, type ProjectSchema } from "@/gen/reliant/v1/project_pb";
import { projectsKey, type Project } from "@/hooks/project-queries";

export type ProjectFixture = Partial<Project> & { id: string };

/** A frontend Project as the proto row it is read from. */
export function projectRow(project: ProjectFixture): MessageInitShape<typeof ProjectSchema> {
  return {
    id: project.id,
    name: project.name ?? "",
    path: project.path ?? "",
    description: project.description ?? "",
    isGitRepo: project.is_git_repo ?? false,
    defaultBranch: project.default_branch ?? "",
    lastActive: project.last_active ?? "",
    createdAt: project.created_at ?? "",
    updatedAt: project.updated_at ?? "",
    remoteUrl: project.remote_url ?? "",
    isForge: project.is_forge ?? false,
    forgeProjectName: project.forge_project_name ?? "",
  };
}

/** A ListProjects answer holding `projects`. */
export function listProjectsResponse(projects: readonly ProjectFixture[]) {
  return create(ListProjectsResponseSchema, {
    projects: projects.map(projectRow),
    total: projects.length,
  });
}

/**
 * Put `projects` in the project-list entry of `client`. The entry is kept
 * for the client's lifetime (a test client's gcTime of 0 would otherwise
 * drop it before the first render subscribes).
 */
export function seedProjects(client: QueryClient, projects: readonly ProjectFixture[]): void {
  client.setQueryDefaults(projectsKey(), { gcTime: Infinity });
  client.setQueryData(projectsKey(), listProjectsResponse(projects));
}
