/**
 * `/m/runs` — the run log, on a phone: every run in every project over the
 * last week, newest first, and the trigger events that started none
 * (Skipped, Couldn't start), so a link to Runs — a notification, a pasted
 * URL — lands on the runs rather than the chat list.
 *
 * Read-only and unfiltered on purpose: filtering and run controls are the
 * desktop Runs tab's. A run IS a chat, so a row opens it as one.
 */

import { Link } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";

import { useRunList, type RunSummary, type UnlaunchedEventSummary } from "@/hooks/run-queries";
import { useProjects } from "@/hooks/project-queries";
import { formatRelativeTime } from "@/lib/relativeTime";
import { runStatusFromDisplayState, triggerEventOutcomeDisplay } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import ErrorBoundary from "../ErrorBoundary";
import { RunStatusDot } from "../runs/RunStatusIndicator";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { MobileMenuButton } from "./MobileMenuButton";
import { MobileScreenBody, MobileScreenHeader } from "./MobileChrome";

const FILTERS = { range: "7d" as const, includeUnlaunched: true };

type Row = { kind: "run"; at: number; run: RunSummary } | { kind: "event"; at: number; event: UnlaunchedEventSummary };

/** Runs and events, newest first: both lists already are, so this is a merge. */
export function mobileRunRows(runs: readonly RunSummary[], events: readonly UnlaunchedEventSummary[]): Row[] {
  return [
    ...runs.map((run): Row => ({ kind: "run", at: run.createdAt, run })),
    ...events.map((event): Row => ({ kind: "event", at: event.occurredAt, event })),
  ].sort((a, b) => b.at - a.at);
}

export function MobileRunsScreen() {
  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="mobile-runs-screen">
      <MobileScreenHeader title="Runs" subtitle="Last 7 days, every project" leading={<MobileMenuButton />} />
      <MobileScreenBody>
        <ErrorBoundary layout="panel">
          <MobileRuns />
        </ErrorBoundary>
      </MobileScreenBody>
    </div>
  );
}

function MobileRuns() {
  const list = useRunList(FILTERS);
  const { projects } = useProjects();
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name;

  if (list.isLoading) {
    return (
      <div className="flex justify-center py-10 text-muted-foreground">
        <Loader2 className="h-5 w-5 animate-spin" aria-label="Loading runs" />
      </div>
    );
  }
  if (list.isError) {
    return (
      <p className="py-10 text-center text-sm text-muted-foreground" role="alert">
        Runs could not be loaded.
      </p>
    );
  }
  const rows = mobileRunRows(list.runs, list.events);
  if (rows.length === 0) {
    return <p className="py-10 text-center text-sm text-muted-foreground">No runs in the last 7 days.</p>;
  }

  return (
    <div className="space-y-3">
      <ul aria-label="Runs" className="divide-y divide-border/60 overflow-hidden rounded-lg border border-border bg-card">
        {rows.map((row) =>
          row.kind === "run" ? (
            <li key={`run-${row.run.chatId}`}>
              <Link
                to="/m/chats/$chatId"
                params={{ chatId: row.run.chatId }}
                className="flex min-h-[56px] items-center gap-3 px-4 py-2.5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/40"
              >
                <RunStatusDot status={runStatusFromDisplayState(row.run.displayState, row.run.outcome)} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium text-foreground">{row.run.title || "Untitled run"}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {[
                      runStatusFromDisplayState(row.run.displayState, row.run.outcome).label,
                      row.run.triggerName || undefined,
                      projectName(row.run.projectId),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">{formatRelativeTime(new Date(row.at).toISOString())}</span>
              </Link>
            </li>
          ) : (
            <li key={`event-${row.event.id}`} className="flex min-h-[56px] items-center gap-3 px-4 py-2.5 text-muted-foreground" data-testid={`mobile-run-event-${row.event.id}`}>
              <span
                aria-hidden="true"
                className={cn("h-2.5 w-2.5 shrink-0 rounded-full border-2", row.event.outcome === "failed" ? "border-destructive" : "border-warning")}
              />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm text-foreground/80">
                  {triggerEventOutcomeDisplay(row.event.outcome).label}
                  {row.event.outcomeDetail ? `: ${row.event.outcomeDetail}` : ""}
                </span>
                <span className="block truncate text-xs">
                  {[row.event.triggerName, getWorkflowDisplayName(row.event.workflowName, true), projectName(row.event.projectId)]
                    .filter(Boolean)
                    .join(" · ")}
                </span>
              </span>
              <span className="shrink-0 text-xs">{formatRelativeTime(new Date(row.at).toISOString())}</span>
            </li>
          ),
        )}
      </ul>
      {list.hasNextPage && (
        <button
          type="button"
          onClick={() => void list.fetchNextPage()}
          disabled={list.isFetchingNextPage}
          className="flex min-h-[44px] w-full items-center justify-center rounded-lg border border-border text-sm font-medium text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {list.isFetchingNextPage ? "Loading…" : "Load more"}
        </button>
      )}
    </div>
  );
}
