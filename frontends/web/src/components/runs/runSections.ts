// Copyright (c) 2025 Reliant Labs

/**
 * How the Runs list arranges what the server returns (WORKFLOW_UI.md §5.2 and
 * §5.3). Pure, so the arrangement is testable without rendering.
 */

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import type { RunSummary } from "@/api/run-grpc";

export interface RunSections {
  /** Awaiting input, or blocked on a machine. Rendered only when non-empty. */
  needsYou: RunSummary[];
  /** Queued, running or paused. */
  live: RunSummary[];
  /** Everything that has stopped for good. */
  finished: RunSummary[];
}

const NEEDS_YOU = new Set([RunDisplayState.NEEDS_INPUT, RunDisplayState.WAITING_FOR_MACHINE]);
const LIVE = new Set([RunDisplayState.QUEUED, RunDisplayState.RUNNING, RunDisplayState.PAUSED]);

/** Split runs into the three sections, keeping the server's newest-first order. */
export function sectionRuns(runs: RunSummary[]): RunSections {
  const sections: RunSections = { needsYou: [], live: [], finished: [] };
  for (const run of runs) {
    if (NEEDS_YOU.has(run.displayState)) sections.needsYou.push(run);
    else if (LIVE.has(run.displayState)) sections.live.push(run);
    else sections.finished.push(run);
  }
  return sections;
}

export type RunListItem =
  | { kind: "run"; run: RunSummary }
  | { kind: "group"; triggerId: string; triggerName: string; runs: RunSummary[] };

/** More than this many repeats of one automation collapse into a group. */
const GROUP_THRESHOLD = 3;

/**
 * Collapse repeats of one automation in a section of FINISHED runs (§5.2):
 * when an automation produced more than three runs and every one of them
 * completed, they render as one group row at the position of the newest.
 *
 * A run that did not complete (failed, cancelled) never hides inside a
 * group: it renders on its own, above the group, so a failure in an hourly
 * sweep cannot be buried under 23 green repeats.
 */
export function groupRuns(runs: RunSummary[]): RunListItem[] {
  const completedByTrigger = new Map<string, RunSummary[]>();
  for (const run of runs) {
    if (!run.triggerId || run.displayState !== RunDisplayState.COMPLETED) continue;
    const list = completedByTrigger.get(run.triggerId) ?? [];
    list.push(run);
    completedByTrigger.set(run.triggerId, list);
  }
  const grouped = new Set(
    [...completedByTrigger].filter(([, list]) => list.length > GROUP_THRESHOLD).map(([id]) => id),
  );
  if (grouped.size === 0) return runs.map((run) => ({ kind: "run", run }));

  // Broken-out runs of a grouped automation render above their group.
  const brokenOut = new Map<string, RunSummary[]>();
  for (const run of runs) {
    if (grouped.has(run.triggerId) && run.displayState !== RunDisplayState.COMPLETED) {
      const list = brokenOut.get(run.triggerId) ?? [];
      list.push(run);
      brokenOut.set(run.triggerId, list);
    }
  }

  const items: RunListItem[] = [];
  const emitted = new Set<string>();
  for (const run of runs) {
    if (!grouped.has(run.triggerId)) {
      items.push({ kind: "run", run });
      continue;
    }
    if (emitted.has(run.triggerId)) continue;
    emitted.add(run.triggerId);
    for (const loose of brokenOut.get(run.triggerId) ?? []) items.push({ kind: "run", run: loose });
    const members = completedByTrigger.get(run.triggerId)!;
    items.push({
      kind: "group",
      triggerId: run.triggerId,
      triggerName: members[0]!.triggerName,
      runs: members,
    });
  }
  return items;
}

/** "3m 5s", "1h 4m", "12s". Empty for a negative or unknown span. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "";
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}
