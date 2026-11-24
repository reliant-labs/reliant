// Copyright (c) 2025 Reliant Labs

/**
 * The Chats heading's one menu (restyle §11.5): how the list is sorted,
 * grouped or flat, every project or the active ones, the archived chats, and
 * Add / Manage projects. Which projects it shows is the project filter beside
 * it (Projects/ChatProjectFilter: choose N of M).
 *
 * Its trigger is a hover-only `⋯` on the heading. It keeps its old test id
 * (`project-filter-trigger`), and its label still names what the list shows.
 */

import { MoreHorizontal } from "lucide-react";

import type { ChatSortOption } from "../../store/chatListPreferencesStore";
import type { Project } from "../../hooks/project-queries";
import { cn } from "../../lib/utils";
import DropdownMenu, { type MenuGroup } from "../forge-ui/dropdown_menu";
import { projectFilterGroups, projectFilterLabel } from "./ProjectFilterMenu";
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
  // What the list shows (its label): the project filter is its own control
  // beside this menu (Projects/ChatProjectFilter).
  filterId: string | null;
  projects: readonly Project[];
  onAddProject: () => void;
  onManageProjects: () => void;
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
  onAddProject,
  onManageProjects,
  sortOrder,
  onSortChange,
  viewMode,
  onViewChange,
  showAll,
  archived,
  onArchivedChange,
}: ChatListMenuProps) {
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
    ...projectFilterGroups({ onAddProject, onManageProjects }),
  ];

  const showing = projectFilterLabel(filterId, projects);
  return (
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
  );
}

