// Copyright (c) 2025 Reliant Labs

/**
 * One run in the Runs list (WORKFLOW_UI.md §5.2): what state it is in, what
 * it is, who started it, and when. Status words and colors come from
 * lib/runStatus and launch-kind words from the same module, so a row cannot
 * say something the run's own page does not.
 */

import { useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";

import type { RunSummary } from "@/api/run-grpc";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { isLiveRunStatus, launchKindDisplay, runStatusFromDisplayState } from "@/lib/runStatus";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { daemonLabel } from "../Automations/daemonChoices";
import { RunStatusDot } from "../ui/RunStatusIndicator";
import { formatDuration } from "./runSections";
import { LaunchKindIcon } from "./LaunchKindIcon";

interface RunRowProps {
  run: RunSummary;
  /** Shown on the second line; omitted when the list is scoped to one project. */
  projectName?: string;
  className?: string;
}

export { LaunchKindIcon };

/**
 * The run list's column template — status dot · run · started by · started ·
 * duration — shared by the rows and RunListHeader so every cell lines up under
 * its label. Below md a row folds to two lines and the header hides.
 */
export const RUN_ROW_GRID =
  "grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 px-4 md:grid-cols-[1rem_minmax(0,1fr)_11rem_7rem_4.5rem]";

/** Column labels over the run rows, on the rows' own grid. */
export function RunListHeader() {
  return (
    <div
      aria-hidden="true"
      className={cn(
        RUN_ROW_GRID,
        "hidden border-b border-border/60 bg-background py-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground md:grid",
      )}
    >
      <span />
      <span>Run</span>
      <span>Started by</span>
      <span>Started</span>
      <span className="text-right">Duration</span>
    </div>
  );
}

export function RunRow({ run, projectName, className }: RunRowProps) {
  const status = runStatusFromDisplayState(run.displayState, run.outcome);
  const launch = launchKindDisplay(run.launchKind);
  const live = isLiveRunStatus(status);
  const { daemons } = useDaemonStatus();
  const machine = run.daemonId
    ? daemonLabel(
        daemons.find((d) => d.daemonId === run.daemonId),
        run.daemonId,
      )
    : undefined;
  const startedIso = new Date(run.createdAt).toISOString();
  const detail = [
    run.workflowName ? getWorkflowDisplayName(run.workflowName, true) : undefined,
    projectName,
    machine,
  ].filter(Boolean);

  return (
    <li
      className={cn(RUN_ROW_GRID, "py-2.5 transition-colors hover:bg-muted/40", className)}
      data-testid={`run-row-${run.chatId}`}
    >
      <RunStatusDot status={status} />

      <div className="min-w-0">
        <Link
          to="/workflows/runs/$runId"
          params={{ runId: run.chatId }}
          className="block truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {run.title || "Untitled run"}
        </Link>
        <p className="mt-0.5 truncate text-xs text-muted-foreground">
          <span className="text-foreground/80">{status.label}</span>
          {detail.length > 0 && ` · ${detail.join(" · ")}`}
        </p>
      </div>

      <div className="col-start-2 row-start-2 flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground md:col-start-auto md:row-start-auto">
        <span className="sr-only">Started by: </span>
        <LaunchKindIcon kind={launch.kind} />
        {/* Any automation's run names and links it; only automations set a trigger. */}
        {run.triggerId && run.triggerName ? (
          <Link
            to="/workflows/automations/$triggerId"
            params={{ triggerId: run.triggerId }}
            className="truncate rounded-sm hover:text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {run.triggerName}
          </Link>
        ) : (
          <span className="truncate">{launch.shortLabel}</span>
        )}
      </div>

      <div className="col-start-3 row-start-1 truncate text-right text-xs text-muted-foreground md:col-start-auto md:row-start-auto md:text-left">
        <span className="sr-only">Started </span>
        <time dateTime={startedIso} title={formatAbsoluteTime(startedIso)}>
          {formatRelativeTime(startedIso)}
        </time>
      </div>

      <div className="col-start-3 row-start-2 text-right text-xs tabular-nums text-muted-foreground md:col-start-auto md:row-start-auto">
        <span className="sr-only">Duration </span>
        <RunDuration startedAt={run.createdAt} completedAt={run.completedAt} live={live} />
      </div>
    </li>
  );
}

/**
 * completed - started, or a live clock for a run still going. A stopped run
 * with no completion time (paused, or a row that predates completed_at) shows
 * nothing rather than a duration that keeps growing.
 */
export function RunDuration({
  startedAt,
  completedAt,
  live,
}: {
  startedAt: number;
  completedAt?: number;
  live: boolean;
}) {
  const ticking = live && completedAt === undefined;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!ticking) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [ticking]);

  if (completedAt !== undefined) return <>{formatDuration(completedAt - startedAt)}</>;
  if (ticking) return <>{formatDuration(now - startedAt)}</>;
  return <span aria-hidden="true">—</span>;
}
