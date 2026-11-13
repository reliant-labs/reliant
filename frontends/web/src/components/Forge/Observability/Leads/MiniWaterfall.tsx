// Copyright (c) 2026 Reliant Labs

import type { Span } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatDuration } from "../format";
import { plural, slowStepOf, stepsOf } from "./leadStory";

/**
 * The first `max` steps of a trace on one time axis, the slowest step and the
 * failed ones marked: a trace's timeline at a glance, before the full
 * waterfall.
 */
export function MiniWaterfall({ spans, max = 8, className }: { spans: readonly Span[]; max?: number; className?: string }) {
  const steps = stepsOf(spans);
  const root = steps[0]?.span;
  if (!root) return null;
  const total = Math.max(root.durationMs, 1);
  const slowId = slowStepOf(steps)?.span.spanId;
  const shown = steps.slice(0, max);
  return (
    <div className={cn("min-w-0 space-y-1", className)} role="img" aria-label={`Timeline of ${root.name}: ${shown.length} of ${steps.length} steps`} data-testid="lead-mini-waterfall">
      {shown.map(({ span, depth }) => {
        const left = Math.min(Math.max(0, ((span.startMs - root.startMs) / total) * 100), 99);
        const width = Math.min(Math.max(0.8, (span.durationMs / total) * 100), 100 - left);
        const slow = span.spanId === slowId;
        return (
          <div key={span.spanId} className="grid grid-cols-[minmax(6rem,13rem)_1fr_3.5rem] items-center gap-2 text-xs leading-4 max-sm:grid-cols-[minmax(5rem,8rem)_1fr_3rem]">
            <span className={cn("truncate", slow ? "font-semibold text-ink" : "text-ink-muted")} style={{ paddingLeft: `${Math.min(depth, 4) * 0.6}rem` }} title={`${span.service}: ${span.name}`}>
              {span.name}
            </span>
            <span className="relative h-2 rounded-sm bg-surface-sunken">
              <span
                className={cn("absolute inset-y-0 rounded-sm", span.status === "error" ? "bg-danger" : slow ? "bg-warning" : "bg-primary/50")}
                style={{ left: `${left}%`, width: `${width}%` }}
              />
            </span>
            <span className={cn("text-right tabular-nums", slow ? "font-semibold text-warning-ink" : "text-ink-muted")}>{formatDuration(span.durationMs)}</span>
          </div>
        );
      })}
      {steps.length > shown.length && <p className="text-xs text-ink-muted">and {plural(steps.length - shown.length, "more step")} in the full timeline</p>}
    </div>
  );
}
