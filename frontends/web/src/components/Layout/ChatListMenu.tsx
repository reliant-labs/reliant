// Copyright (c) 2025 Reliant Labs

/**
 * The Chats heading's one menu (restyle §11.5): which project the list shows,
 * how it is sorted, grouped or flat, every project or the active ones, and
 * the archived chats. It replaces the project filter, sort and view buttons
 * that used to sit in a row of their own above the list.
 *
 * Its trigger is a hover-only `⋯` on the heading. It keeps the project
 * filter's test id (`project-filter-trigger`): the filter's items live here
 * now, with their own test ids unchanged.
 */

import { useState } from "react";
import { MoreHorizontal } from "lucide-react";

import type { ChatSortOption } from "../../store/chatListPreferencesStore";
import type { Project } from "../../hooks/project-queries";
import { cn } from "../../lib/utils";
import DropdownMenu, { type MenuGroup } from "../forge-ui/dropdown_menu";
import { ProjectSelectorDialog } from "../Projects/ProjectSelector";
import { ALL_PROJECTS_LABEL, projectFilterGroups, projectFilterLabel } from "./ProjectFilterMenu";
import { SIDEBAR_HEADING_HOVER_ACTION, SIDEBAR_ICON_BUTTON } from "./sidebar/primitives";

/** The sort orders, in the menu's order. */
export const CHAT_SORT_OPTIONS: { value: ChatSortOption; label: string }[] = [
  { value: "recent_activity", label: "Recent activity" },
  { value: "needs_attention_first", label: "Needs attention" },
  { value: "newest_first", label: "Newest first" },
  { value: "oldest_first", label: "Oldest first" },
  { value: "alphabetical_asc", label: "A → Z" },
  { value: "alphabetical_desc", label: "Z → A" },
];

export const SHOW_ALL_PROJECTS_LABEL = "Show every project";

interface ChatListMenuProps {
  // The project filter.
  filterId: string | null;
  projects: readonly Project[];
  onFilterChange: (projectId: string | null) => void;
  onAddProject: () => void;
  onManageProjects: () => void;
  needsYouByProject?: ReadonlyMap<string, number>;
  // Sort and view.
  sortOrder: ChatSortOption;
  onSortChange: (sort: ChatSortOption) => void;
  viewMode: "grouped" | "flat";
  onViewChange: (view: "grouped" | "flat") => void;
  /**
   * Every project rather than the active ones (lib/sidebarGroups). Undefined
   * leaves the choice out: nothing it would change, or a filtered list.
   */
  showAll?: { on: boolean; onChange: (on: boolean) => void; disabled?: boolean };
  // The archived list.
  archived: boolean;
  onArchivedChange: (archived: boolean) => void;
}

export function ChatListMenu({
  filterId,
  projects,
  onFilterChange,
  onAddProject,
  onManageProjects,
  needsYouByProject,
  sortOrder,
  onSortChange,
  viewMode,
  onViewChange,
  showAll,
  archived,
  onArchivedChange,
}: ChatListMenuProps) {
  // The project selector, for every project the menu does not list.
  const [choosing, setChoosing] = useState(false);
  const listToggles: MenuGroup["items"] = [];
  if (showAll) {
    listToggles.push({
      label: SHOW_ALL_PROJECTS_LABEL,
      checked: showAll.on,
      disabled: showAll.disabled,
      onClick: () => showAll.onChange(!showAll.on),
      testId: "project-list-toggle",
    });
  }
  listToggles.push({
    label: "Archived chats",
    checked: archived,
    onClick: () => onArchivedChange(!archived),
    testId: "chat-list-archived",
  });

  // Sort and view first: the project list is as long as the person's
  // projects, so it goes last, where its length pushes nothing down.
  const groups: MenuGroup[] = [
    {
      label: "Sort",
      selection: "single",
      items: CHAT_SORT_OPTIONS.map((option) => ({
        label: option.label,
        checked: sortOrder === option.value,
        onClick: () => onSortChange(option.value),
      })),
    },
    {
      label: "View",
      selection: "single",
      items: [
        {
          label: filterId ? "Grouped by workspace" : "Grouped by project",
          checked: viewMode === "grouped",
          onClick: () => onViewChange("grouped"),
        },
        {
          label: "Flat list",
          checked: viewMode === "flat",
          onClick: () => onViewChange("flat"),
        },
      ],
    },
    { selection: "multiple", items: listToggles },
    ...projectFilterGroups({
      filterId,
      projects,
      onChange: onFilterChange,
      onChooseProject: () => setChoosing(true),
      onAddProject,
      onManageProjects,
      needsYouByProject,
    }),
  ];

  const showing = projectFilterLabel(filterId, projects);
  return (
    <>
      <DropdownMenu
        align="right"
        density="compact"
        label="Chat list options"
        menuClassName="w-64 max-h-[min(32rem,70vh)] overflow-y-auto"
        groups={groups}
        trigger={(props) => (
          <button
            type="button"
            {...props}
            data-testid="project-filter-trigger"
            aria-label={`Chat list options. Showing ${showing}`}
            className={cn(SIDEBAR_ICON_BUTTON, SIDEBAR_HEADING_HOVER_ACTION, "aria-expanded:bg-foreground/[0.08]")}
          >
            <MoreHorizontal className="size-4" aria-hidden="true" />
          </button>
        )}
      />
      <ProjectSelectorDialog
        open={choosing}
        onClose={() => setChoosing(false)}
        mode="chat"
        label="Show one project's chats"
        selectedKey={filterId ?? ALL_PROJECTS_KEY}
        leadItems={[
          { key: ALL_PROJECTS_KEY, label: ALL_PROJECTS_LABEL, onSelect: () => onFilterChange(null), testId: "project-selector-all-projects" },
        ]}
        rowState={(row) => {
          const waiting = needsYouByProject?.get(row.key);
          return waiting ? { hint: `${waiting} waiting on you` } : undefined;
        }}
        onSelect={(selection) => {
          if (selection.kind === "project") onFilterChange(selection.row.key);
        }}
      />
    </>
  );
}

/** The selector's key for All projects: no project id is empty. */
const ALL_PROJECTS_KEY = "";
