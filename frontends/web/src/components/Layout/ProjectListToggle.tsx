// Copyright (c) 2025 Reliant Labs

/**
 * The sidebar's show-all-projects toggle. All projects lists the active
 * projects (chats, recent use, pinned) and folds the rest under "More
 * projects"; on, it lists every project, hidden ones included
 * (lib/sidebarGroups listProjectGroups).
 *
 * It sits beside the machine filter and looks like it: at rest an icon, and
 * on it names what the list shows, so a list longer than usual says why.
 */

import { Folders } from "lucide-react";

import { cn } from "../../lib/utils";
import Tooltip from "../forge-ui/tooltip";

export const SHOW_ALL_PROJECTS_LABEL = "Every project";

interface ProjectListToggleProps {
  showAll: boolean;
  onChange: (showAll: boolean) => void;
}

export function ProjectListToggle({ showAll, onChange }: ProjectListToggleProps) {
  const tip = showAll ? "Showing every project, hidden ones too" : "Show every project";
  return (
    <Tooltip content={tip} side="bottom" delay={300} triggerClassName="flex min-w-0 shrink">
      <button
        type="button"
        onClick={() => onChange(!showAll)}
        aria-pressed={showAll}
        aria-label="Show every project"
        data-testid="project-list-toggle"
        className={cn(
          "flex h-6 min-w-0 shrink-0 items-center gap-1 rounded-md transition-colors",
          "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          showAll
            ? "max-w-[8rem] bg-primary/10 px-1.5 text-xs font-medium text-primary-ink hover:bg-primary/15"
            : "w-6 justify-center text-muted-foreground hover:bg-muted/60 hover:text-foreground",
        )}
      >
        <Folders className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        {showAll && <span className="min-w-0 truncate">{SHOW_ALL_PROJECTS_LABEL}</span>}
      </button>
    </Tooltip>
  );
}
