// Copyright (c) 2025 Reliant Labs

/**
 * The chat list's project filter, choose N of M: the Chats heading's filter
 * button, which opens the project selector in multi-select mode
 * (ProjectSelector `multiple`), and the heading's summary of a filter of
 * several ("· 3 projects" with an × that shows every project again).
 *
 * The state is hooks/useChatProjectFilter's: one project is still the
 * single-project filter (`/project/<id>`), several are `?projects=`.
 */

import { ListFilter, X } from "lucide-react";

import Tooltip from "@/components/forge-ui/tooltip";
import type { ChatProjectFilter } from "@/hooks/useChatProjectFilter";
import { projectsSummary } from "@/lib/chatProjectFilter";
import { cn } from "@/lib/utils";

import { ProjectSelector } from "./ProjectSelector";

export interface ChatProjectFilterButtonProps {
  filter: ChatProjectFilter;
  /** Chats waiting on the person, per project id: a hint on each row. */
  needsYouByProject?: ReadonlyMap<string, number>;
  /** The trigger's classes: the heading's icon button (Layout/sidebar/primitives). */
  className?: string;
}

export function ChatProjectFilterButton({ filter, needsYouByProject, className }: ChatProjectFilterButtonProps) {
  const active = filter.mode !== "all";
  const label = active
    ? `Filter chats by project. Showing ${filter.mode === "one" ? "1 project" : projectsSummary(filter.ids.length)}`
    : "Filter chats by project";
  return (
    <ProjectSelector
      mode="chat"
      align="right"
      multiple={{ selected: filter.ids, onChange: filter.set }}
      placeholder="Search projects…"
      rowState={(row) => {
        const waiting = needsYouByProject?.get(row.key);
        return waiting ? { hint: `${waiting} waiting` } : undefined;
      }}
      trigger={(props) => (
        <Tooltip content="Filter by project" side="bottom" delay={300}>
          <button
            type="button"
            {...props}
            aria-label={label}
            data-testid="chat-project-filter-trigger"
            data-active={active || undefined}
            className={cn(className, active && "text-primary-ink")}
          >
            <ListFilter className="size-4" aria-hidden="true" />
          </button>
        </Tooltip>
      )}
    />
  );
}

/** "· 3 projects ×": what the list shows when it is several projects. */
export function ChatProjectFilterScope({ filter, clearClassName }: { filter: ChatProjectFilter; clearClassName?: string }) {
  if (filter.mode !== "many") return null;
  return (
    <>
      <span aria-hidden="true">·</span>
      <span className="min-w-0 truncate text-foreground" data-testid="project-filter-label">
        {projectsSummary(filter.ids.length)}
      </span>
      <Tooltip content="Show all projects" side="bottom" delay={300} type="label">
        <button
          type="button"
          onClick={filter.clear}
          aria-label="Show all projects"
          data-testid="project-filter-clear"
          className={clearClassName}
        >
          <X className="size-3.5" aria-hidden="true" />
        </button>
      </Tooltip>
    </>
  );
}
