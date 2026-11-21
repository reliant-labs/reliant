// Copyright (c) 2025 Reliant Labs

/**
 * The sidebar's project filter: "All projects" or one project, plus Add
 * project and Manage projects. The project is a filter on the chat list, not
 * a gate in front of it. Picking a project narrows the list (and moves the
 * main pane into that project); "All projects" lists every project's chats,
 * grouped by project.
 *
 * Its items are a section of the Chats heading's one menu (ChatListMenu,
 * restyle §11.5), with the same test ids they had as a menu of their own.
 * The menu lists All projects and the filtered project; every other project
 * is one "Choose a project…" away, in the project selector
 * (Projects/ProjectSelector), which searches, pins and scales to hundreds.
 */

import { FolderPlus, Layers, Search, Settings2 } from "lucide-react";

import type { MenuGroup } from "../forge-ui/dropdown_menu";
import type { Project } from "../../hooks/project-queries";

export const ALL_PROJECTS_LABEL = "All projects";

export interface ProjectFilterOptions {
  /** The filtered project's id, or null for All projects. */
  filterId: string | null;
  projects: readonly Project[];
  onChange: (projectId: string | null) => void;
  /** Open the project selector to pick any project. */
  onChooseProject: () => void;
  onAddProject: () => void;
  onManageProjects: () => void;
  /** Chats waiting on the user, per project id, shown beside each project. */
  needsYouByProject?: ReadonlyMap<string, number>;
}

/** The filter's name for what the list shows: a project's name, or All projects. */
export function projectFilterLabel(filterId: string | null, projects: readonly Project[]): string {
  const filtered = filterId ? projects.find((p) => p.id === filterId) : undefined;
  return filtered?.name ?? ALL_PROJECTS_LABEL;
}

/** The filter's menu sections: All projects, the filtered project and Choose a project…, then Add and Manage. */
export function projectFilterGroups({
  filterId,
  projects,
  onChange,
  onChooseProject,
  onAddProject,
  onManageProjects,
  needsYouByProject,
}: ProjectFilterOptions): MenuGroup[] {
  // A count of chats waiting on you, as quiet meta text; never a coloured pill.
  const needsYouHint = (count: number | undefined) =>
    count && count > 0 ? <span className="tabular-nums text-warning-ink">{count}</span> : undefined;

  const totalNeedsYou = needsYouByProject
    ? Array.from(needsYouByProject.values()).reduce((sum, n) => sum + n, 0)
    : 0;

  return [
    {
      label: "Project",
      selection: "single",
      items: [
        {
          label: ALL_PROJECTS_LABEL,
          icon: <Layers className="h-3.5 w-3.5" />,
          checked: filterId === null,
          hint: needsYouHint(totalNeedsYou),
          onClick: () => onChange(null),
          testId: "project-filter-all",
        },
        ...projects
          .filter((project) => project.id === filterId)
          .map((project) => ({
            label: project.name,
            checked: true,
            hint: needsYouHint(needsYouByProject?.get(project.id)),
            onClick: () => onChange(project.id),
            testId: `project-filter-${project.id}`,
          })),
      ],
    },
    {
      items: [
        {
          label: "Choose a project…",
          icon: <Search className="h-3.5 w-3.5" />,
          onClick: onChooseProject,
          testId: "project-filter-choose",
        },
        {
          label: "Add project…",
          icon: <FolderPlus className="h-3.5 w-3.5" />,
          onClick: onAddProject,
          testId: "project-filter-add",
        },
        {
          label: "Manage projects",
          icon: <Settings2 className="h-3.5 w-3.5" />,
          onClick: onManageProjects,
          testId: "project-filter-manage",
        },
      ],
    },
  ];
}
