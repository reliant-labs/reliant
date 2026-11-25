// Copyright (c) 2025 Reliant Labs

/**
 * The project half of the Chats heading's one menu (ChatListMenu, restyle
 * §11.5): Add project and Manage projects, with the test ids they always had.
 *
 * Which projects the chat list shows is the shell's project scope, the one
 * project filter: the switcher at the top of the nav
 * (sidebar/ProjectSwitcher.tsx; several are hooks/useChatProjectFilter's).
 * One project chosen is still the single-project filter (`/project/<id>`),
 * so `projectFilterLabel` names it.
 */

import { FolderPlus, Settings2 } from "lucide-react";

import type { MenuGroup } from "../forge-ui/dropdown_menu";
import type { Project } from "../../hooks/project-queries";

export const ALL_PROJECTS_LABEL = "All projects";

export interface ProjectFilterOptions {
  onAddProject: () => void;
  onManageProjects: () => void;
}

/** The filter's name for what the list shows: a project's name, or All projects. */
export function projectFilterLabel(filterId: string | null, projects: readonly Project[]): string {
  const filtered = filterId ? projects.find((p) => p.id === filterId) : undefined;
  return filtered?.name ?? ALL_PROJECTS_LABEL;
}

/** The menu's project section: Add project and Manage projects. */
export function projectFilterGroups({ onAddProject, onManageProjects }: ProjectFilterOptions): MenuGroup[] {
  return [
    {
      items: [
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
