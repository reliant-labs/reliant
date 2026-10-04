// Copyright (c) 2025 Reliant Labs

/**
 * Sidebar footer pill: "N automations running" (WORKFLOW_UI.md §4.3, §6.6).
 *
 * The chat list holds conversations only, so automation runs that are live
 * right now have no row. This is the chat list's one nod to them: present
 * while something is happening, gone when nothing is. Modelled on
 * BackgroundWorkPill, which does the same job for one chat's spawns.
 *
 * It counts runs NOT already in the sidebar (an adopted live run has its own
 * row and activity dot) and does not poll: see useLiveRuns.
 */

import { useMemo } from "react";

import type { RunsSearch } from "@/routeSchemas";
import { summarizeLiveAutomations, useLiveRuns } from "@/hooks/run-queries";
import { cn } from "@/lib/utils";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { RunStatusDot } from "../ui/RunStatusIndicator";

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
const NEEDS_YOU_SEARCH: RunsSearch = { state: ["needs_you"], allProjects: true, range: "all" };

const RUNNING_STATUS = runStatusFromDisplayState(RunDisplayState.RUNNING);
const NEEDS_YOU_STATUS = runStatusFromDisplayState(RunDisplayState.NEEDS_INPUT);

export function AutomationActivityPill({ listedChatIds, onOpenRuns }: AutomationActivityPillProps) {
  const { data } = useLiveRuns();

  const summary = useMemo(
    () => summarizeLiveAutomations(data?.runs ?? [], listedChatIds, !!data?.nextPageToken),
    [data, listedChatIds],
  );

  if (summary.running === 0) return null;

  const more = summary.truncated ? "+" : "";
  const runningLabel = `${summary.running}${more} ${summary.running === 1 ? "automation" : "automations"} running`;
  const needsYouLabel = `${summary.needsYou} ${summary.needsYou === 1 ? "needs" : "need"} you`;

  return (
    <div className="flex flex-wrap items-center gap-1.5 px-1 pb-1.5" data-testid="automation-activity-pill">
      <button
        type="button"
        onClick={() => onOpenRuns?.(LIVE_SEARCH)}
        disabled={!onOpenRuns}
        className={cn(
          "inline-flex h-6 min-w-0 items-center gap-1.5 rounded-full border border-border/60 bg-background px-2.5 text-2xs font-medium text-foreground",
          "transition-colors hover:border-border hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
          "disabled:cursor-default",
        )}
        aria-label={`${runningLabel}. Open live runs`}
        data-testid="automation-activity-pill-running"
      >
        <RunStatusDot status={RUNNING_STATUS} size="sm" />
        <span className="truncate">{runningLabel}</span>
      </button>
      {summary.needsYou > 0 && (
        <button
          type="button"
          onClick={() => onOpenRuns?.(NEEDS_YOU_SEARCH)}
          disabled={!onOpenRuns}
          className={cn(
            "inline-flex h-6 items-center gap-1.5 rounded-full border border-warning/40 bg-warning/10 px-2.5 text-2xs font-medium text-warning",
            "transition-colors hover:bg-warning/15 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
            "disabled:cursor-default",
          )}
          aria-label={`${needsYouLabel}. Open runs that need you`}
          data-testid="automation-activity-pill-needs-you"
        >
          <RunStatusDot status={NEEDS_YOU_STATUS} size="sm" />
          <span>{needsYouLabel}</span>
        </button>
      )}
    </div>
  );
}
