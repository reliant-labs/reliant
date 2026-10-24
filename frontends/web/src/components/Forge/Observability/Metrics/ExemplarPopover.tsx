// Copyright (c) 2025 Reliant Labs

/**
 * Exemplars: sampled observations of a histogram, each carrying the trace that
 * produced it (ListMetricExemplars). They answer "show me a slow one".
 *
 *   rail     under the chart, one dot per exemplar at its time, raised by its
 *            value — a stored trace solid, a sampled-out one hollow. Each dot is
 *            a button, so the keyboard reaches them too.
 *   popover  a dot's bucket: up to 20 exemplars in it, stored traces first.
 *            "Open trace" goes to the console's trace view; a trace the tail
 *            sampler dropped says so instead of linking to an empty page.
 *
 * The rail is its own strip rather than dots on the canvas: uPlot's scales are
 * internal to the chart, and a dot drawn at a guessed y would lie about the
 * value. Time is shared, so a dot sits under the bucket it belongs to.
 */

import { useEffect, useRef } from "react";
import { ExternalLink, X } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import type { MetricExemplar } from "@/services/controlPlane/observability/metricsTypes";
import { cn } from "@/lib/utils";

import { formatTimestamp, formatValue, type ValueUnit } from "../format";
import { ObsError, ObsLoadingRows } from "../ObsStates";

/** Stored traces first (a link that opens something), then the newest. */
export function orderExemplars(exemplars: readonly MetricExemplar[]): MetricExemplar[] {
  return [...exemplars].sort((a, b) => Number(b.traceStored) - Number(a.traceStored) || b.timeMs - a.timeMs);
}

export interface ExemplarRailProps {
  exemplars: readonly MetricExemplar[];
  /** The chart's x axis (epoch seconds) and its step: the rail spans the same time. */
  timestamps: readonly number[];
  stepSeconds: number;
  unit: ValueUnit;
  onOpen: (exemplar: MetricExemplar, at: { x: number; y: number }) => void;
}

/** The chart's plot starts after its value axis (UPlotChart's `size: 64`). */
const AXIS_GUTTER = "pl-16";

export function ExemplarRail({ exemplars, timestamps, stepSeconds, unit, onOpen }: ExemplarRailProps) {
  if (timestamps.length === 0 || exemplars.length === 0) return null;
  const start = timestamps[0]! * 1000;
  const end = (timestamps[timestamps.length - 1]! + Math.max(stepSeconds, 1)) * 1000;
  const values = exemplars.map((e) => e.value);
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const inWindow = exemplars.filter((e) => e.timeMs >= start && e.timeMs <= end);
  if (inWindow.length === 0) return null;
  return (
    <div className={cn("relative", AXIS_GUTTER)} data-testid="metrics-exemplar-rail">
      <div className="relative h-7 rounded border border-border/60 bg-background" role="group" aria-label={`${inWindow.length} exemplars: a sampled observation and its trace`}>
        {inWindow.map((e, i) => {
          const x = ((e.timeMs - start) / (end - start)) * 100;
          const y = hi > lo ? (e.value - lo) / (hi - lo) : 0.5;
          return (
            <button
              key={`${e.traceId}-${e.spanId}-${i}`}
              type="button"
              onClick={(event) => {
                const rect = event.currentTarget.getBoundingClientRect();
                onOpen(e, { x: rect.left, y: rect.bottom });
              }}
              style={{ left: `${x}%`, bottom: `${4 + y * 14}px` }}
              className={cn(
                "absolute h-2.5 w-2.5 -translate-x-1/2 rounded-full border border-primary transition-transform hover:scale-125 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
                e.traceStored ? "bg-primary" : "bg-background"
              )}
              aria-label={`Exemplar ${formatValue(e.value, unit)} at ${formatTimestamp(e.timeMs)}${e.traceStored ? "" : ", trace sampled out"}`}
              data-testid="metrics-exemplar-dot"
              data-stored={e.traceStored || undefined}
            />
          );
        })}
      </div>
      <p className="mt-1 text-2xs text-ink-muted">Exemplars: a dot is one sampled observation; open it for its trace.</p>
    </div>
  );
}

export interface ExemplarPopoverProps {
  at: { x: number; y: number };
  /** The bucket the dot belongs to, for the heading. */
  bucketStartMs: number;
  exemplars: readonly MetricExemplar[] | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  unit: ValueUnit;
  traceHref: (traceId: string) => string;
  onClose: () => void;
  subject: string;
}

export function ExemplarPopover({ at, bucketStartMs, exemplars, isLoading, error, onRetry, unit, traceHref, onClose, subject }: ExemplarPopoverProps) {
  const ref = useRef<HTMLDivElement | null>(null);
  const escapeLayer = useEscapeLayer(true, onClose);
  useEffect(() => {
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) onClose();
    };
    document.addEventListener("mousedown", onPointerDown);
    ref.current?.focus();
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [onClose]);
  const left = Math.max(8, Math.min(at.x - 16, (typeof window === "undefined" ? 1200 : window.innerWidth) - 360));
  const ordered = orderExemplars(exemplars ?? []);
  return (
    <div
      ref={ref}
      role="dialog"
      aria-label="Exemplars in this bucket"
      tabIndex={-1}
      {...escapeLayer}
      style={{ left, top: at.y + 6 }}
      className="fixed z-[60] w-[22rem] rounded-lg border border-border bg-surface p-3 text-left shadow-md outline-none"
      data-testid="metrics-exemplar-popover"
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <p className="text-xs font-medium text-ink">Exemplars from {formatTimestamp(bucketStartMs)}</p>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="inline-flex h-6 w-6 items-center justify-center rounded text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      </div>
      {isLoading ? (
        <ObsLoadingRows rows={3} testId="metrics-exemplars-loading" />
      ) : error ? (
        <ObsError error={error} subject={subject} onRetry={onRetry} />
      ) : ordered.length === 0 ? (
        <p className="text-xs text-ink-muted">No exemplars were recorded in this bucket.</p>
      ) : (
        <ul className="max-h-80 space-y-1.5 overflow-y-auto" data-testid="metrics-exemplar-list">
          {ordered.map((e, i) => (
            <li key={`${e.traceId}-${e.spanId}-${i}`} className="rounded-md border border-border/60 bg-background px-2.5 py-2 text-xs" data-stored={e.traceStored || undefined}>
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium tabular-nums text-ink">{formatValue(e.value, unit)}</span>
                <span className="text-ink-muted">{formatTimestamp(e.timeMs)}</span>
              </div>
              {Object.keys(e.attributes).length > 0 && (
                <p className="mt-0.5 truncate font-mono text-2xs text-ink-muted" title={Object.entries(e.attributes).map(([k, v]) => `${k}=${v}`).join(" ")}>
                  {Object.entries(e.attributes)
                    .map(([k, v]) => `${k}=${v}`)
                    .join(" ")}
                </p>
              )}
              <div className="mt-1 flex items-center justify-between gap-2">
                <Mono value={e.traceId} length={12} copy={false} />
                {e.traceStored ? (
                  <Link href={traceHref(e.traceId)} className="no-color inline-flex items-center gap-1 text-ink underline decoration-ink-muted/50 underline-offset-2 hover:decoration-ink" data-testid="metrics-exemplar-open-trace">
                    Open trace
                    <ExternalLink className="h-3 w-3" aria-hidden="true" />
                  </Link>
                ) : (
                  <span className="text-ink-muted" data-testid="metrics-exemplar-sampled-out">
                    this trace was sampled out
                  </span>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
