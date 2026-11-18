// Copyright (c) 2026 Reliant Labs

/**
 * A funnel as steps across (display.layout: "steps", forge dashspec
 * LayoutSteps): each step a column with its
 * count, and between two steps the share that continued, the weakest step
 * called out. It is the Overview's acquisition funnel, whose steps differ by
 * orders of magnitude (15,000 visitors, 48 sign-ups): a bar scaled to the
 * first step would draw the last two as nothing, so the bars here are scaled
 * on a log axis and the numbers carry the comparison. The rows are a sql
 * funnel's (funnelFromRows) or the funnel kind's steps.
 */

import { ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";

import { formatCount } from "../format";
import { formatPercent, withRates, type FunnelStep } from "./FunnelChart";
import { CHART_PALETTE, tokenColor } from "./seriesModel";

export function StepFunnel({ steps: raw, label = "entrants", compact = false }: { steps: FunnelStep[]; label?: string; compact?: boolean }) {
  const steps = withRates(raw);
  if (steps.length === 0) return null;
  const first = steps[0].count;
  const overall = steps.length > 1 ? steps[steps.length - 1].rateFromFirst : 1;
  let worst = -1;
  steps.forEach((step, index) => {
    if (index === 0 || steps[index - 1].count === 0) return;
    if (worst < 0 || step.rateFromPrevious < steps[worst].rateFromPrevious) worst = index;
  });
  // Log-scaled heights: every step is visible, and a 10x drop still reads as one.
  const top = Math.log10(Math.max(first, 1) + 1);
  const height = (count: number) => (count > 0 ? Math.max(8, (Math.log10(count + 1) / top) * 100) : 0);
  return (
    <div data-testid="obs-panel-step-funnel">
      {!compact && (
        <p className="mb-3 text-xs text-ink-muted">
          <span className="text-base font-semibold tabular-nums text-ink">{formatPercent(overall)}</span> of {formatCount(first)} {label} reached{" "}
          <span className="text-ink">{steps[steps.length - 1].event}</span>
          {worst > 0 && (
            <>
              {" "}
              · weakest step: <span className="text-ink">{steps[worst].event}</span> ({formatPercent(steps[worst].rateFromPrevious)} continued)
            </>
          )}
        </p>
      )}
      <ol className="flex items-stretch gap-1">
        {steps.map((step, index) => (
          <li key={`${step.event}-${index}`} className="flex min-w-0 flex-1 items-stretch gap-1" data-step={step.event}>
            {index > 0 && (
              <div
                className={cn("flex w-14 shrink-0 flex-col items-center justify-center text-center text-2xs tabular-nums text-ink-muted", index === worst && "text-warning-ink")}
                data-testid="obs-step-funnel-rate"
              >
                <ChevronRight className="h-4 w-4" aria-hidden="true" />
                <span className="font-medium">{formatPercent(step.rateFromPrevious)}</span>
                <span className="sr-only">of {steps[index - 1].event} continued to {step.event}</span>
              </div>
            )}
            <div className="flex min-w-0 flex-1 flex-col">
              <div className={cn("flex items-end rounded-sm bg-ink/5", compact ? "h-10" : "h-24")} aria-hidden="true">
                <span className="block w-full rounded-sm" style={{ height: `${height(step.count)}%`, backgroundColor: tokenColor(CHART_PALETTE[0], 1 - Math.min(index, 4) * 0.15) }} />
              </div>
              <p className="mt-1.5 truncate text-xs text-ink" title={step.event}>
                {step.event}
              </p>
              <p className="text-sm font-semibold tabular-nums text-ink">
                {formatCount(step.count)}
                {index > 0 && <span className="ml-1.5 text-2xs font-normal text-ink-muted">{formatPercent(step.rateFromFirst)}</span>}
              </p>
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}
