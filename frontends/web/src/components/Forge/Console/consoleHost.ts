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

import { useEffect, useMemo } from "react";

import { useProjects } from "@/hooks/project-queries";
import { isDemoShell } from "@/lib/shell";
import { useAuthStore } from "@/store/authStore";
import { useProjectStore, type Project } from "@/store/projectStore";

/**
 * Which of the person's checkouts backs the console of `forgeProject`:
 *
 *   one       the project the URL's `?project=` names, when it declares the
 *             app; else the only project that does.
 *   several   no `?project=` (or one naming no checkout of this app), and
 *             several projects declare the name: two checkouts of one repo,
 *             or two repos whose forge.yaml share a name. The console asks
 *             (ConsoleCheckoutPicker) rather than guess: the current project
 *             is the tab's history, not the URL, so it must not decide.
 *   none      no project declares it. Still a scope: the pages read the
 *             control plane by the name, with no daemon.
 */
export type ConsoleCheckout =
  | { kind: "one"; project: Project }
  | { kind: "several"; candidates: Project[] }
  | { kind: "none" };

export function consoleCheckout(forgeProject: string, projects: readonly Project[], pinned?: string | null): ConsoleCheckout {
  const declaring = projects.filter((project) => (project.forge_project_name ?? "").trim() === forgeProject);
  const named = pinned ? declaring.find((project) => project.id === pinned) : undefined;
  if (named) return { kind: "one", project: named };
  if (declaring.length === 1) return { kind: "one", project: declaring[0]! };
  if (declaring.length > 1) return { kind: "several", candidates: declaring };
  return { kind: "none" };
}

export interface ConsoleReliantProject {
  /** The project list has answered (or failed), or there is none to wait for. */
  listed: boolean;
  /** The Reliant project declaring the forge app, once found. */
  projectId: string | null;
  /** Several checkouts declare the app and the URL names none of them: the console asks which. */
  candidates: Project[] | null;
}

/**
 * The Reliant project behind `forgeProject` (consoleCheckout; `pinned` is
 * the URL's `?project=`), made the current project once
 * found when `select` (the page on screen is this app's console). `listed`
 * once the project list has answered (or failed): until then a forge name
 * matches nothing only because nothing is loaded yet. The demo shell
 * (lib/shell.ts) has no Reliant projects: the forge name in the URL is the
 * whole scope, and nothing is waited for. Both app hosts have projects (the
 * forge host mounts the one route tree, U6).
 *
 * `select` is false for the shell's nav, which draws an app's sections on
 * every page: a chat's page among them. There the URL's chat decides the
 * project, never the app the nav happens to show. Selecting the app's
 * declaring project from the nav switched the context away from the chat
 * on screen, and selectProject then moved the URL to that project's chat
 * (the 2026-11-23 "can't open a chat" incident: a project whose app is
 * declared by another project could not keep any of its chats open).
 */
export function useConsoleReliantProject(
  forgeProject: string,
  { select = true, pinned }: { select?: boolean; pinned?: string | null } = {},
): ConsoleReliantProject {
  const demo = isDemoShell();
  const { projects, isLoading: listing } = useProjects();
  const listed = demo || projects.length > 0 || !listing;
  const currentProject = useProjectStore((state) => state.currentProject);
  const selectProject = useProjectStore((state) => state.selectProject);

  const checkout = useMemo(
    () => (listed && !demo ? consoleCheckout(forgeProject, projects, pinned) : ({ kind: "none" } as const)),
    [listed, demo, forgeProject, projects, pinned]
  );
  const match = checkout.kind === "one" ? checkout.project : null;
  useEffect(() => {
    // Context, not the person's pick: it is not remembered as the last
    // project (another tab's bare entry would open it).
    if (select && match && match.id !== currentProject?.id) void selectProject(match, { skipWorkspaceStateSave: true });
  }, [select, match, currentProject?.id, selectProject]);
  return { listed, projectId: match?.id ?? null, candidates: checkout.kind === "several" ? checkout.candidates : null };
}

export interface ConsoleUser {
  name: string;
  email?: string;
}

/** The signed-in user, as the console's rail shows them. */
export function useConsoleUser(): ConsoleUser | undefined {
  const authUser = useAuthStore((state) => state.user);
  // One object per signed-in user: it is a prop of the console's memoized chrome.
  return useMemo(
    () =>
      authUser
        ? {
            name:
              (authUser.user_metadata?.full_name as string | undefined) ??
              (authUser.user_metadata?.name as string | undefined) ??
              authUser.email?.split("@")[0] ??
              "You",
            email: authUser.email ?? undefined,
          }
        : undefined,
    [authUser]
  );
}
