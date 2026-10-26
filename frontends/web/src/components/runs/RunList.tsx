// Copyright (c) 2025 Reliant Labs

/**
 * The Runs list body (WORKFLOW_UI.md §5.3): Needs you, Live, then Finished,
 * each a section label over one divided list. Finished collapses repeats of
 * one automation into a group row unless grouping is turned off, and
 * interleaves — in time order, as muted rows — the trigger events that
 * started no run (Skipped, Couldn't start), so a run that never happened
 * is in the log too.
 *
 * One Card for the whole list, never a card per section: sections are rows
 * inside one surface, separated by the section-label rung of the heading
 * ladder (forge-ui/card.tsx).
 */

import { useMemo, useState, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";

import type { RunSummary, UnlaunchedEventSummary } from "@/hooks/run-queries";
import { formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import Card from "../forge-ui/card";
import { LaunchKindIcon, RunListHeader, RunRow, UnlaunchedEventRow } from "./RunRow";
import { groupRuns, sectionRuns, type RunListItem } from "./runSections";

interface RunListProps {
  runs: RunSummary[];
  /** Trigger events that started no run, newest first; shown among Finished. */
  events?: UnlaunchedEventSummary[];
  /** Collapse repeated automation runs in Finished. */
  groupRepeats: boolean;
  /** Project names by id; when set, each row names its project. */
  projectNames?: Map<string, string>;
  /** Rendered after the last row: Load more, or a page error. */
  footer?: ReactNode;
  /**
   * Render the sections without their own Card, for a host that already is
   * one (workflow detail's Recent runs) — a card in a card is the one nesting
   * the elevation rule forbids.
   */
  bare?: boolean;
}

type FinishedItem = RunListItem | { kind: "event"; event: UnlaunchedEventSummary };

function itemTime(item: FinishedItem): number {
  if (item.kind === "event") return item.event.occurredAt;
  return item.kind === "run" ? item.run.createdAt : item.runs[0]!.createdAt;
}

/** Interleave events (newest first) into finished items (newest first), keeping both orders. */
export function interleaveEvents(items: RunListItem[], events: readonly UnlaunchedEventSummary[]): FinishedItem[] {
  if (events.length === 0) return items;
  const out: FinishedItem[] = [];
  let j = 0;
  for (const item of items) {
    while (j < events.length && events[j]!.occurredAt > itemTime(item)) out.push({ kind: "event", event: events[j++]! });
    out.push(item);
  }
  while (j < events.length) out.push({ kind: "event", event: events[j++]! });
  return out;
}

const NO_EVENTS: UnlaunchedEventSummary[] = [];

export function RunList({ runs, events = NO_EVENTS, groupRepeats, projectNames, footer, bare = false }: RunListProps) {
  const sections = useMemo(() => sectionRuns(runs), [runs]);
  const finishedItems = useMemo<FinishedItem[]>(
    () =>
      interleaveEvents(
        groupRepeats ? groupRuns(sections.finished) : sections.finished.map((run) => ({ kind: "run" as const, run })),
        events,
      ),
    [groupRepeats, sections.finished, events],
  );
  const projectName = (run: RunSummary) => projectNames?.get(run.projectId);

  const body = (
    <>
      <RunListHeader />
      {sections.needsYou.length > 0 && (
        <RunSection label="Needs you" id="runs-needs-you">
          {sections.needsYou.map((run) => (
            <RunRow key={run.chatId} run={run} projectName={projectName(run)} />
          ))}
        </RunSection>
      )}
      {sections.live.length > 0 && (
        <RunSection label="Live" id="runs-live">
          {sections.live.map((run) => (
            <RunRow key={run.chatId} run={run} projectName={projectName(run)} />
          ))}
        </RunSection>
      )}
      {finishedItems.length > 0 && (
        <RunSection label="Finished" id="runs-finished">
          {finishedItems.map((item) =>
            item.kind === "event" ? (
              <UnlaunchedEventRow key={`event-${item.event.id}`} event={item.event} projectName={projectNames?.get(item.event.projectId)} />
            ) : item.kind === "run" ? (
              <RunRow key={item.run.chatId} run={item.run} projectName={projectName(item.run)} />
            ) : (
              <RepeatGroup key={`group-${item.triggerId}`} item={item} projectName={projectName} />
            ),
          )}
        </RunSection>
      )}
      {footer}
    </>
  );
  if (bare) return body;
  return (
    <Card padding="none" className="overflow-hidden bg-card">
      {body}
    </Card>
  );
}

function RunSection({ label, id, children }: { label: string; id: string; children: ReactNode }) {
  return (
    <section aria-labelledby={id} className="border-b border-border/60 last:border-b-0">
      <h2
        id={id}
        className="px-4 pb-1 pt-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground"
      >
        {label}
      </h2>
      <ul aria-label={`${label} runs`} className="divide-y divide-border/60">
        {children}
      </ul>
    </section>
  );
}

/** "Hourly sweep · 23 runs · all completed · last 12 min ago", expandable. */
function RepeatGroup({
  item,
  projectName,
}: {
  item: Extract<RunListItem, { kind: "group" }>;
  projectName: (run: RunSummary) => string | undefined;
}) {
  const [expanded, setExpanded] = useState(false);
  const newest = item.runs[0]!;
  const listId = `run-group-${item.triggerId}`;
  const summary = `${item.triggerName || "Automation"} · ${item.runs.length} runs · all completed · last ${formatRelativeTime(new Date(newest.createdAt).toISOString())}`;

  return (
    <li>
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={listId}
        onClick={() => setExpanded((open) => !open)}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-sm text-muted-foreground transition-colors hover:bg-muted/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/40"
      >
        <ChevronRight
          className={cn("h-4 w-4 shrink-0 transition-transform motion-reduce:transition-none", expanded && "rotate-90")}
          aria-hidden="true"
        />
        <LaunchKindIcon kind="schedule" />
        <span className="min-w-0 truncate">{summary}</span>
      </button>
      {expanded && (
        <ul id={listId} aria-label={`${item.triggerName} runs`} className="divide-y divide-border/60 border-t border-border/60">
          {item.runs.map((run) => (
            <RunRow key={run.chatId} run={run} projectName={projectName(run)} className="pl-11" />
          ))}
        </ul>
      )}
    </li>
  );
}
