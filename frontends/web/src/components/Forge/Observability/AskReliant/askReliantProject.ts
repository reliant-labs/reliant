// Copyright (c) 2025 Reliant Labs

/**
 * WHICH RELIANT PROJECT an "Ask Reliant" chat belongs to.
 *
 * The console addresses a forge project by name; a chat belongs to a Reliant
 * project, the checkout a daemon can reach. The mapping is the project's
 * persisted `forge_project_name` (ConsoleLayout's `projectDeclaring`), which
 * the console already resolves into `ForgeConsole.projectId`. This turns that
 * answer into a decision:
 *
 *   project   exactly one project owns it (or the reader's current one does, of
 *             several): start the chat there with no questions asked.
 *   choose    the console cannot say. Either several checkouts declare the
 *             forge project and none is current ("ambiguous"), or none does
 *             ("unmapped": an org admin opening a teammate's link, a project no
 *             daemon has read yet). The reader picks, with the current project
 *             preselected, so the fallback is one click rather than a guess:
 *             starting an investigation in the wrong checkout reads the wrong
 *             code, and the reader would not know until the agent had spent a
 *             run on it.
 */

import type { Project } from "@/store/projectStore";

export interface AskProjectInput {
  /** The forge project the console is about, when it knows. */
  forgeProject?: string;
  /** ForgeConsole.projectId: what ConsoleLayout resolved from the forge name. */
  consoleProjectId?: string | null;
  projects: readonly Project[];
  currentProject: Project | null;
}

export type AskProjectResolution =
  | { kind: "project"; project: Project }
  | { kind: "choose"; reason: "ambiguous" | "unmapped"; candidates: Project[]; preselected: string | null };

const declares = (project: Project, forgeProject: string) => (project.forge_project_name ?? "").trim() === forgeProject;

export function resolveAskProject({ forgeProject, consoleProjectId, projects, currentProject }: AskProjectInput): AskProjectResolution {
  const name = forgeProject?.trim() ?? "";
  const declaring = name ? projects.filter((project) => declares(project, name)) : [];

  if (declaring.length === 1) return { kind: "project", project: declaring[0] };
  if (declaring.length > 1) {
    const current = currentProject && declaring.find((project) => project.id === currentProject.id);
    if (current) return { kind: "project", project: current };
    const resolved = declaring.find((project) => project.id === consoleProjectId);
    return { kind: "choose", reason: "ambiguous", candidates: declaring, preselected: (resolved ?? declaring[0]).id };
  }

  // Nothing declares the name. The console may still have resolved a project
  // (the list it read could lag the one this holds); trust a project that is here.
  const resolved = consoleProjectId ? projects.find((project) => project.id === consoleProjectId) : undefined;
  if (resolved) return { kind: "project", project: resolved };
  return { kind: "choose", reason: "unmapped", candidates: [...projects], preselected: currentProject?.id ?? null };
}
