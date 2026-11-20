// Copyright (c) 2026 Reliant Labs

/**
 * The Runs page's headline: how many runs started in the window, as the one
 * big number, then one chart of them over the window. Success rate, live and
 * needs-you counts are a quiet row beside it.
 *
 * Every figure is cut from the rows the list already loaded (useRunList), so
 * the headline costs no request and never disagrees with the list under it.
 * The list pages, so while more pages remain the count says "N+" and the
 * chart and rate are of the runs loaded, never presented as the window's
 * total. There is no previous-window read, so it names the window and makes
 * no comparison.
 */

import { useMemo } from "react";

import type { RunSummary } from "@/hooks/run-queries";
import { isLiveRunStatus, runStatusFromDisplayState } from "@/lib/runStatus";
import { HeadlineBars } from "../Forge/Observability/charts/HeadlineBars";
import { RUN_RANGE_LABELS } from "./runFilterState";
import type { RunRangeKey } from "@/routeSchemas";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const RANGE_MS: Record<Exclude<RunRangeKey, "all">, number> = { "24h": DAY, "7d": 7 * DAY, "30d": 30 * DAY };

export interface RunsHeadlineFigures {
  started: number;
  /** Succeeded (or completed with no declared verdict) over every finished run; null when none finished. */
  successRate: number | null;
  live: number;
  needsYou: number;
}

/** The headline's figures from the loaded runs. Pure, for the tests. */
export function runsHeadlineFigures(runs: readonly RunSummary[]): RunsHeadlineFigures {
  let finished = 0;
  let ok = 0;
  let live = 0;
  let needsYou = 0;
  for (const run of runs) {
    const status = runStatusFromDisplayState(run.displayState, run.outcome);
    if (status.key === "needs_you") needsYou++;
    else if (isLiveRunStatus(status)) live++;
    if (status.key === "succeeded" || status.key === "completed") {
      finished++;
      ok++;
    } else if (status.key === "failed") {
      finished++;
    }
  }
  return { started: runs.length, successRate: finished ? ok / finished : null, live, needsYou };
}

/** Runs started per bucket over the window: hours for a day, days beyond. */
export function runsSeries(runs: readonly RunSummary[], range: RunRangeKey, now: number): { timestamps: number[]; values: number[] } {
  if (runs.length === 0) return { timestamps: [], values: [] };
  const span = range === "all" ? Math.max(DAY, now - Math.min(...runs.map((run) => run.createdAt))) : RANGE_MS[range];
  const step = span <= DAY ? HOUR : DAY;
  const end = Math.ceil(now / step) * step;
  const buckets = Math.max(2, Math.ceil(span / step));
  const start = end - buckets * step;
  const values = new Array<number>(buckets).fill(0);
  for (const run of runs) {
    const index = Math.floor((run.createdAt - start) / step);
    if (index >= 0 && index < buckets) values[index]++;
  }
  return { timestamps: values.map((_, i) => (start + i * step) / 1000), values };
}

function percent(rate: number): string {
  const value = rate * 100;
  return `${value >= 99.95 || value === 0 ? value.toFixed(0) : value.toFixed(1)}%`;
}

export function RunsHeadline({ runs, range, more }: { runs: readonly RunSummary[]; range: RunRangeKey; more: boolean }) {
  const figures = useMemo(() => runsHeadlineFigures(runs), [runs]);
  const series = useMemo(() => runsSeries(runs, range, Date.now()), [runs, range]);
  const window = RUN_RANGE_LABELS[range];
  return (
    <section className="min-w-0 space-y-5" aria-label={`Runs started, ${window.toLowerCase()}`} data-testid="runs-headline">
      <div className="flex flex-wrap items-end justify-between gap-x-12 gap-y-6">
        <div className="min-w-0">
          <div className="flex items-baseline gap-2 text-ui">
            <span className="font-medium text-foreground">Runs started</span>
            <span className="text-meta text-muted-foreground">{window}</span>
          </div>
          <div className="mt-1 text-figure tabular-nums text-foreground" data-testid="runs-headline-count">
            {figures.started.toLocaleString()}
            {more ? "+" : ""}
          </div>
        </div>
        <dl className="flex flex-wrap gap-x-10 gap-y-5">
          <Figure label="Success rate" value={figures.successRate === null ? "—" : percent(figures.successRate)} hint="Of the runs that finished: succeeded, against failed." />
          <Figure label="Live" value={figures.live.toLocaleString()} />
          <Figure label="Needs you" value={figures.needsYou.toLocaleString()} />
        </dl>
      </div>
      {series.values.length > 1 && <HeadlineBars values={series.values} timestamps={series.timestamps} label={`Runs started, ${window.toLowerCase()}`} testId="runs-headline-chart" />}
    </section>
  );
}

function Figure({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="min-w-0" title={hint}>
      <dt className="text-meta text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-2xl font-semibold tabular-nums text-foreground">{value}</dd>
    </div>
  );
}
