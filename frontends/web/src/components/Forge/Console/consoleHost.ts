// Copyright (c) 2025 Reliant Labs

/**
 * What the forge console's layouts (ConsoleLayout, OrgConsoleLayout) take from
 * the app they are mounted in: the Reliant project that declares the forge app,
 * and the signed-in user shown in the rail.
 *
 * One module so the forge demo's bundle can replace it whole: its twin,
 * consoleHost.demo.ts, has no Reliant projects and no sign-in, and the demo
 * build substitutes it (vite-plugin-demo-bundle.ts, "demo twins"), which keeps
 * the auth store, Supabase and the project and chat stores out of that bundle.
 * Keep the two files' exports the same.
 */

import { useEffect } from "react";

import { useProjects } from "@/hooks/project-queries";
import { isForgeShell } from "@/lib/shell";
import { useAuthStore } from "@/store/authStore";
import { useProjectStore, type Project } from "@/store/projectStore";

/**
 * The Reliant project that declares `forgeProject`: the current one when it
 * does (two checkouts of one repo both declare it), else the first that does.
 */
export function projectDeclaring(
  forgeProject: string,
  projects: readonly Project[],
  current: Project | null
): Project | null {
  const declares = (project: Project) => (project.forge_project_name ?? "").trim() === forgeProject;
  if (current && declares(current)) return current;
  return projects.find(declares) ?? null;
}

export interface ConsoleReliantProject {
  /** The project list has answered (or failed), or there is none to wait for. */
  listed: boolean;
  /** The Reliant project declaring the forge app, once found. */
  projectId: string | null;
}

/**
 * The Reliant project behind `forgeProject`, made the current project once
 * found. `listed` once the project list has answered (or failed): until then a
 * forge name matches nothing only because nothing is loaded yet. The forge
 * shell has no Reliant projects (useProjects is off there): the forge name in
 * the URL is the whole scope, and nothing is waited for.
 */
export function useConsoleReliantProject(forgeProject: string): ConsoleReliantProject {
  const forgeShell = isForgeShell();
  const { projects, isLoading: listing } = useProjects();
  const listed = forgeShell || projects.length > 0 || !listing;
  const currentProject = useProjectStore((state) => state.currentProject);
  const selectProject = useProjectStore((state) => state.selectProject);

  const match = listed && !forgeShell ? projectDeclaring(forgeProject, projects, currentProject) : null;
  useEffect(() => {
    if (match && match.id !== currentProject?.id) void selectProject(match);
  }, [match, currentProject?.id, selectProject]);
  return { listed, projectId: match?.id ?? null };
}

export interface ConsoleUser {
  name: string;
  email?: string;
}

/** The signed-in user, as the console's rail shows them. */
export function useConsoleUser(): ConsoleUser | undefined {
  const authUser = useAuthStore((state) => state.user);
  if (!authUser) return undefined;
  return {
    name:
      (authUser.user_metadata?.full_name as string | undefined) ??
      (authUser.user_metadata?.name as string | undefined) ??
      authUser.email?.split("@")[0] ??
      "You",
    email: authUser.email ?? undefined,
  };
}
