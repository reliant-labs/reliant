// Copyright (c) 2025 Reliant Labs

/**
 * The sidebar's project filter and switcher: "All projects" or one project.
 *
 * The project is a filter on the chat list, not a gate in front of it. Picking
 * a project narrows the list (and moves the main pane into that project);
 * "All projects" lists every project's chats, grouped by project. Adding a
 * project opens the Add project dialog; managing them is Settings → Projects.
 */

import { ChevronDown, FolderGit2, FolderPlus, Layers, Settings2 } from "lucide-react";

import DropdownMenu, { type MenuGroup } from "../forge-ui/dropdown_menu";
import type { Project } from "../../hooks/project-queries";
import { cn } from "../../lib/utils";

export const ALL_PROJECTS_LABEL = "All projects";

interface ProjectFilterMenuProps {
  /** The filtered project's id, or null for All projects. */
  filterId: string | null;
  projects: readonly Project[];
  onChange: (projectId: string | null) => void;
  onAddProject: () => void;
  onManageProjects: () => void;
  /** Chats waiting on the user, per project id, shown beside each project. */
  needsYouByProject?: ReadonlyMap<string, number>;
}

/** Projects, most recently used first. */
function byRecent(projects: readonly Project[]): Project[] {
  return [...projects].sort(
    (a, b) => (b.last_active ?? "").localeCompare(a.last_active ?? "") || a.name.localeCompare(b.name),
  );
}

export function ProjectFilterMenu({
  filterId,
  projects,
  onChange,
  onAddProject,
  onManageProjects,
  needsYouByProject,
}: ProjectFilterMenuProps) {
  const filtered = filterId ? projects.find((p) => p.id === filterId) : undefined;
  const label = filtered?.name ?? ALL_PROJECTS_LABEL;

  const needsYouHint = (count: number | undefined) =>
    count && count > 0 ? (
      <span className="rounded-full bg-primary/10 px-1.5 text-2xs font-medium text-primary">{count}</span>
    ) : undefined;

  const totalNeedsYou = needsYouByProject
    ? Array.from(needsYouByProject.values()).reduce((sum, n) => sum + n, 0)
    : 0;

  const groups: MenuGroup[] = [
    {
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
      ],
    },
    {
      label: "Projects",
      selection: "single",
      items: byRecent(projects).map((project) => ({
        label: project.name,
        checked: project.id === filterId,
        hint: needsYouHint(needsYouByProject?.get(project.id)),
        onClick: () => onChange(project.id),
        testId: `project-filter-${project.id}`,
      })),
    },
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

  return (
    <DropdownMenu
      align="left"
      density="compact"
      label="Switch project"
      menuClassName="max-h-96 w-64 overflow-y-auto"
      trigger={(props) => (
        <button
          type="button"
          {...props}
          data-testid="project-filter-trigger"
          aria-label={`Switch project. Showing ${label}`}
          className={cn(
            "flex h-7 min-w-0 max-w-full items-center gap-1.5 rounded-md px-2 text-left",
            "text-xs font-semibold uppercase tracking-wide text-muted-foreground",
            "transition-colors hover:bg-muted/60 hover:text-foreground",
            "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
          )}
        >
          {filterId === null ? (
            <Layers className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          ) : (
            <FolderGit2 className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          )}
          <span className="min-w-0 truncate" data-testid="project-filter-label">
            {label}
          </span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        </button>
      )}
      groups={groups}
    />
  );
}
