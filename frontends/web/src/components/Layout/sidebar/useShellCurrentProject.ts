// Copyright (c) 2026 Reliant Labs

/**
 * The current project of a page whose route names no project — the Inbox,
 * Settings › Billing & usage: the one the shell's nav shows there, so the
 * page and the nav always agree (reliant#3445).
 *
 * On such a route the nav's resolution (useShellProject) has no route to read,
 * so it is the shell's scope alone: the focused project of a choice of several
 * (`?projects=`), else the last pick, else the context project, else the most
 * recently active one. This hook is that same resolution, read with no route
 * (so a page rendered outside the router, as Settings can be, still has it).
 *
 * The context project (projectStore.currentProject) is set on a page load only
 * by ModernApp, which mounts on the chat routes alone, and since #3419 the nav
 * no longer selects one as a side effect (that selection moved chats' URLs).
 * So a cold load of these pages has no context project while the nav shows
 * one. This hook READS the nav's resolution; it never selects a project.
 *
 * Interim: the shared URL-state module (the `?projects=` scope, #3426) will
 * own this resolution; this hook then reads it from there.
 */

import { useMemo } from "react";

import { useProjects, type Project } from "@/hooks/project-queries";
import { useProjectStore } from "@/store/projectStore";

import { useShellProject } from "./shellProject";

/** A route that names no project: routeProjectOf answers null for it. */
const NO_ROUTE = "";
const NO_SEARCH: Record<string, unknown> = {};

export function useShellCurrentProject(): Project | null {
  const context = useProjectStore((state) => state.currentProject);
  const shell = useShellProject(NO_ROUTE, NO_SEARCH);
  const { projects } = useProjects();

  return useMemo(() => {
    // The shell has nothing at all to show (no catalog row, nothing
    // remembered): keep whatever the context holds.
    if (!shell.key) return context;
    // The nav shows an app with no code here: no Reliant project to scope to.
    if (!shell.projectId) return null;
    if (context?.id === shell.projectId) return context;
    if (shell.row?.kind === "project") return shell.row.project;
    return projects.find((project) => project.id === shell.projectId) ?? null;
  }, [context, shell.key, shell.projectId, shell.row, projects]);
}
