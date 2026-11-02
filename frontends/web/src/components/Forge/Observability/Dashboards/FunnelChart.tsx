// Copyright (c) 2025 Reliant Labs

/**
 * A funnel: journeys through ordered steps. Each step is a bar as wide as its
 * share of the first step, with its count and conversion from the start;
 * between two steps, the drop-off — how many did not continue, and what share
 * of the step before that is. The biggest drop is called out, since that is
 * the question a funnel answers, and the headline is the overall conversion.
 *
 * Drawn for the funnel kind (product events through ClickHouse windowFunnel,
 * which yields counts only: the time between steps is not in the answer) and
 * for a sql panel with `display.style: "funnel"`, whose rows ARE the steps
 * (funnelFromRows), which is how a funnel stitched across identities, or one
 * that computes its own step times, is still drawn as a funnel.
 */

import type { FunnelStepResult, PanelResult } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatCount, formatPercent } from "../format";
import { CHART_PALETTE, tokenColor } from "./seriesModel";

/** One step: a name and how many journeys reached it. */
export type FunnelStep = Pick<FunnelStepResult, "event" | "count">;

export function FunnelChart({ steps: raw, compact, label = "journeys" }: { steps: FunnelStep[]; compact: boolean; label?: string }) {
  const steps = withRates(raw);
  let worst = -1;
  steps.forEach((step, index) => {
    if (index === 0 || steps[index - 1].count === 0) return;
    if (worst < 0 || step.rateFromPrevious < steps[worst].rateFromPrevious) worst = index;
  });
  const first = steps[0]?.count ?? 0;
  const overall = steps.length > 1 ? steps[steps.length - 1].rateFromFirst : 1;
  const shown = compact ? steps.slice(0, 3) : steps;
  return (
    <div data-testid="obs-panel-funnel">
      {!compact && (
        <p className="mb-3 text-xs text-ink-muted">
          <span className="text-base font-semibold tabular-nums text-ink">{formatPercent(overall)}</span> of {formatCount(first)} {label} finished
          {worst > 0 && (
            <>
              {" "}
              · the biggest drop is into <span className="font-mono text-ink">{steps[worst].event}</span> ({formatPercent(1 - steps[worst].rateFromPrevious)} drop off)
            </>
          )}
        </p>
      )}
      <ol className={cn(!compact && "max-h-96 overflow-y-auto")}>
        {shown.map((step, index) => {
          const lost = index > 0 ? Math.max(steps[index - 1].count - step.count, 0) : 0;
          return (
            <li key={`${step.event}-${index}`} data-step={step.event}>
              {index > 0 && !compact && (
                <p
                  className={cn("flex items-center gap-1.5 py-1 pl-5 text-2xs tabular-nums text-ink-muted", index === worst && "text-warning-ink")}
                  data-testid="obs-funnel-dropoff"
                >
                  <span aria-hidden="true">↓</span>
                  {formatPercent(step.rateFromPrevious)} continued · {formatCount(lost)} dropped ({formatPercent(1 - step.rateFromPrevious)})
                </p>
              )}
              <div className={cn("grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1", compact ? "py-0.5" : "")}>
                <span className="truncate text-xs text-ink">
                  <span className="mr-1.5 tabular-nums text-ink-muted">{index + 1}</span>
                  <span className="font-mono">{step.event}</span>
                </span>
                <span className="text-right text-xs tabular-nums text-ink">
                  {formatCount(step.count)}
                  {index > 0 && !compact && <span className="ml-2 text-ink-muted">{formatPercent(step.rateFromFirst)} of first</span>}
                  {index > 0 && <span className="sr-only">, {formatPercent(step.rateFromPrevious)} of previous</span>}
                </span>
                <span className={cn("col-span-2 block overflow-hidden rounded-sm bg-ink/5", compact ? "h-1.5" : "h-3")} aria-hidden="true">
                  <span
                    className="block h-full rounded-sm"
                    style={{ width: `${Math.max(step.rateFromFirst * 100, step.count > 0 ? 1 : 0)}%`, backgroundColor: tokenColor(CHART_PALETTE[0], 1 - Math.min(index, 4) * 0.12) }}
                  />
                </span>
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

/** Conversion from the first step and from the previous one; 0 where the base is 0. */
export function withRates(steps: FunnelStep[]): FunnelStepResult[] {
  const first = steps[0]?.count ?? 0;
  return steps.map((step, index) => {
    const before = index > 0 ? steps[index - 1].count : step.count;
    return {
      event: step.event,
      count: step.count,
      rateFromFirst: first > 0 ? step.count / first : 0,
      rateFromPrevious: before > 0 ? step.count / before : 0,
    };
  });
}

/**
 * A sql result as funnel steps, in the order the statement returned them: the
 * first text column names a step, the first numeric column counts it. Null when
 * the rows have no name or no count to read.
 */
export function funnelFromRows(data: Pick<PanelResult, "rows" | "columns">): FunnelStep[] | null {
  if (data.rows.length < 1 || data.columns.length < 1) return null;
  const steps: FunnelStep[] = [];
  for (const row of data.rows) {
    const name = Object.values(row.group)[0];
    const count = row.values[0];
    if (name === undefined || count === null || !Number.isFinite(count)) return null;
    steps.push({ event: name, count });
  }
  return steps;
}
