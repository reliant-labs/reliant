// Copyright (c) 2026 Reliant Labs

/**
 * A test's recent runs as a strip of ticks, oldest left: a pass is a quiet
 * grey tick, so the flakes (amber: failed, then passed on retry) and the
 * failures (red) are what the eye finds. Full size on a test's page, each
 * tick a link to its run with a tooltip (result, commit, lane, when);
 * compact in a Flaky tests row, where the row itself is the link.
 */

import Link from "@/components/forge-ui/link";
import Tooltip from "@/components/forge-ui/tooltip";
import type { TestOutcome } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { formatDuration } from "../Observability/format";
import { agoWords, outcomeStrip, STRIP_LABEL, type StripKind } from "./flakyModel";
import { shortSha } from "./testsModel";

export const STRIP_TICK: Record<StripKind, string> = {
  pass: "bg-ink-muted/30",
  retry: "bg-warning",
  fail: "bg-danger",
  skip: "border border-ink-muted/40 bg-transparent",
  unknown: "bg-ink-muted/15",
};

const ORDER: StripKind[] = ["retry", "fail", "pass", "skip", "unknown"];

/** "3 flaky · 2 failed · 45 passed": the strip in words, flakes first. */
export function stripSummary(counts: Record<StripKind, number>): string {
  const words: Record<StripKind, string> = { pass: "passed", retry: "passed on retry", fail: "failed", skip: "skipped", unknown: "unknown" };
  return ORDER.filter((kind) => counts[kind] > 0)
    .map((kind) => `${counts[kind]} ${words[kind]}`)
    .join(" · ");
}

/** The full strip: one linked tick per run, a tooltip on each. */
export function RunStrip({ outcomes, runHref, now }: { outcomes: TestOutcome[]; runHref: (runId: string) => string; now: number }) {
  const { cells } = outcomeStrip(outcomes);
  return (
    <ol className="flex items-end gap-1" aria-label="Outcomes, oldest first">
      {cells.map((cell) => {
        const words = `${STRIP_LABEL[cell.kind]}: ${cell.lane} on ${cell.branch || "no branch"} ${shortSha(cell.gitCommit)}${cell.atMs !== null ? `, ${agoWords(cell.atMs, now)}` : ""}`;
        return (
          <li key={cell.runId} data-kind={cell.kind} className="flex">
            <Tooltip
              content={
                <span className="block space-y-0.5">
                  <span className="block font-medium">{STRIP_LABEL[cell.kind][0]!.toUpperCase() + STRIP_LABEL[cell.kind].slice(1)}</span>
                  <span className="block text-ink-muted">
                    <span className="font-mono">{shortSha(cell.gitCommit)}</span> · {cell.lane}
                    {cell.durationMs > 0 ? ` · ${formatDuration(cell.durationMs)}` : ""}
                    {cell.atMs !== null ? ` · ${agoWords(cell.atMs, now)}` : ""}
                  </span>
                </span>
              }
              delay={0}
              triggerClassName="flex"
            >
              <Link
                  href={runHref(cell.runId)}
                  aria-label={words}
                  className={cn(
                    "block h-5 w-1.5 rounded-full transition-transform hover:scale-y-125 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    STRIP_TICK[cell.kind]
                  )}
                />
            </Tooltip>
          </li>
        );
      })}
    </ol>
  );
}

/** The compact strip for a list row: the last `max` runs, decoration (the row says it in words). */
export function MiniRunStrip({ outcomes, max = 20 }: { outcomes: TestOutcome[] | undefined; max?: number }) {
  if (!outcomes) return <span className="inline-block h-3.5 w-24 animate-pulse rounded-full bg-ink-muted/10" aria-hidden="true" />;
  const { cells, counts } = outcomeStrip(outcomes.slice(0, max));
  if (cells.length === 0) return <span className="text-meta text-ink-muted">No runs</span>;
  return (
    <span className="inline-flex items-center gap-0.5" role="img" aria-label={`Last ${cells.length} runs: ${stripSummary(counts)}`} data-testid="tests-mini-strip">
      {cells.map((cell) => (
        <span key={cell.runId} data-kind={cell.kind} className={cn("block h-3.5 w-1 rounded-full", STRIP_TICK[cell.kind])} />
      ))}
    </span>
  );
}
