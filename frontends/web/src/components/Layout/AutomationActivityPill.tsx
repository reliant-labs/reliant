// Copyright (c) 2025 Reliant Labs

/**
 * "N running", the trail of the sidebar's Workflows row (WORKFLOW_UI.md §4.3,
 * §6.6; restyle §11.3: it was a footer pill).
 *
 * The chat list holds conversations only, so automation runs that are live
 * right now have no row. This is the chat list's one nod to them: present
 * while something is happening, gone when nothing is. Modelled on
 * BackgroundWorkPill, which does the same job for one chat's spawns.
 *
 * It counts runs NOT already in the sidebar (an adopted live run has its own
 * row and activity dot) and does not poll: see useLiveRuns.
 *
 * It says nothing about what needs you: the Inbox is the one place that does,
 * and its sidebar badge is the one count (WORKFLOW_UI.md §8).
 */

import { useMemo } from "react";

import type { RunsSearch } from "@/routeSchemas";
import { summarizeLiveAutomations, useLiveRuns } from "@/hooks/run-queries";
import { cn } from "@/lib/utils";

interface AutomationActivityPillProps {
  /** Chats the sidebar already lists; their runs are not counted again. */
  listedChatIds: ReadonlySet<string>;
  /** Open the Runs list with these filters. */
  onOpenRuns?: (search: RunsSearch) => void;
}

/**
 * Live runs can be in any project and can have started any time ago, so the
 * links widen both: "Live" scoped to the last 24 hours of one project would
 * hide exactly the long-running automation the pill just counted.
 */
const LIVE_SEARCH: RunsSearch = { state: ["live"], allProjects: true, range: "all" };

export function AutomationActivityPill({ listedChatIds, onOpenRuns }: AutomationActivityPillProps) {
  const { data } = useLiveRuns();

  const summary = useMemo(
    () => summarizeLiveAutomations(data?.runs ?? [], listedChatIds, !!data?.nextPageToken),
    [data, listedChatIds],
  );

  if (summary.running === 0) return null;

  const more = summary.truncated ? "+" : "";
  const runningLabel = `${summary.running}${more} ${summary.running === 1 ? "automation" : "automations"} running`;

  // Beside the Workflows row's button, not in it: its own target, Runs filtered to live.
  return (
    <span className="flex min-w-0" data-testid="automation-activity-pill">
      <button
        type="button"
        onClick={() => onOpenRuns?.(LIVE_SEARCH)}
        disabled={!onOpenRuns}
        className={cn(
          "inline-flex h-6 min-w-0 items-center gap-1.5 rounded-md px-1.5 text-meta text-muted-foreground transition-colors",
          "hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          "disabled:cursor-default",
        )}
        aria-label={`${runningLabel}. Open live runs`}
        title={runningLabel}
        data-testid="automation-activity-pill-running"
      >
        <span aria-hidden="true" className="size-1.5 shrink-0 animate-pulse rounded-full bg-primary-ink motion-reduce:animate-none" />
        <span className="truncate tabular-nums">
          {summary.running}
          {more} running
        </span>
      </button>
    </span>
  );
}
