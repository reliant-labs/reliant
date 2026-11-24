// Copyright (c) 2026 Reliant Labs

/**
 * The bubble a chart shows for its highlighted sample: the sample's time,
 * then each series' value there, with its colour (chartHover.ts builds the
 * rows). It portals to <body> with fixed positioning, so a card's overflow
 * never clips it, and sits beside the crosshair, flipping left at the
 * viewport's right edge (placeTooltip).
 *
 * Its look is forge-ui's Tooltip's footprint (rounded-md, z-[60], shadow)
 * on the raised panel instead of the inverted bubble: the series swatches are
 * tuned for a surface, not for ink.
 *
 * It is a read-out, not a control: pointer-events pass through it to the plot
 * under it, and it is aria-hidden (the chart announces a keyboard highlight in
 * its own live region, which reads the same text).
 */

import { useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { cn } from "@/lib/utils";

import { MAX_TOOLTIP_ROWS, placeTooltip, type SampleAnchor, type SampleRow } from "./chartHover";

export interface ChartTooltipProps {
  anchor: SampleAnchor | null;
  time: string;
  rows: readonly SampleRow[];
}

export function ChartTooltip({ anchor, time, rows }: ChartTooltipProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState<{ left: number; top: number } | null>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !anchor) return;
    const next = placeTooltip(anchor, { width: el.offsetWidth, height: el.offsetHeight }, { width: window.innerWidth, height: window.innerHeight });
    setPlace((prev) => (prev && prev.left === next.left && prev.top === next.top ? prev : { left: next.left, top: next.top }));
  }, [anchor, time, rows]);

  if (typeof document === "undefined") return null;
  const listed = rows.slice(0, MAX_TOOLTIP_ROWS);
  const more = rows.length - listed.length;
  const single = rows.length === 1;
  return createPortal(
    <div
      ref={ref}
      role="presentation"
      aria-hidden="true"
      data-testid="chart-tooltip"
      // Until it is measured it waits off-screen, so it never flashes at the corner.
      style={place && anchor ? { left: place.left, top: place.top } : { left: -9999, top: -9999 }}
      // bg-popover is the raised panel: it reads above the card it floats over in light and dark.
      className="pointer-events-none fixed z-[60] w-max max-w-72 rounded-md border border-border bg-popover px-2.5 py-1.5 text-meta text-ink shadow-lg"
    >
      <p className="whitespace-nowrap text-ink-muted" data-testid="chart-tooltip-time">
        {time}
      </p>
      <ul className={cn(single ? "mt-0.5" : "mt-1 space-y-0.5")}>
        {listed.map((row, i) => (
          <li key={`${row.label}-${i}`} className="flex min-w-0 items-center gap-2" data-testid="chart-tooltip-row">
            <span
              aria-hidden="true"
              className={cn("size-2 shrink-0 rounded-full", row.dashed && "border bg-transparent")}
              style={row.dashed ? { borderColor: row.color } : { backgroundColor: row.color }}
            />
            {!single && <span className="min-w-0 flex-1 truncate">{row.label}</span>}
            <span className={cn("shrink-0 font-medium tabular-nums", single ? "text-ui" : "ml-auto")}>{row.value}</span>
          </li>
        ))}
        {more > 0 && <li className="text-ink-muted">{`+${more} more`}</li>}
      </ul>
    </div>,
    document.body
  );
}
