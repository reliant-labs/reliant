// Copyright (c) 2025 Reliant Labs

/**
 * A time-series chart on uPlot — the one chart primitive of the observability
 * area (sparklines, dashboard lines, latency bands).
 *
 * ── COLOURS COME FROM THE THEME ─────────────────────────────────────────────
 *
 * uPlot draws on a canvas, which cannot read a Tailwind class, so the series
 * and axis colours are resolved from the same CSS variables the classes use
 * (`--primary`, `--destructive`, `--border`…) when the chart is built, and the
 * chart is rebuilt when the colour scheme or light/dark mode changes. A
 * hard-coded hex would be right on one of the fourteen schemes.
 *
 * ── THE CANVAS IS DECORATION; THE CAPTION IS THE CONTENT ────────────────────
 *
 * A canvas says nothing to a screen reader, so every chart is a <figure> whose
 * caption states each series' latest value and range in words. The canvas is
 * aria-hidden. The caption is visually hidden: sighted readers have the
 * canvas, the legend and (for a sparkline) the number beside it.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";


import { formatValue, type ValueUnit } from "../format";

export type ChartTone = "primary" | "destructive" | "warning" | "success" | "secondary" | "muted";

export interface ChartSeries {
  label: string;
  values: (number | null)[];
  tone?: ChartTone;
}

/** The categorical order a multi-series chart cycles through. */
const CATEGORICAL: ChartTone[] = ["primary", "secondary", "success", "warning", "destructive", "muted"];

const TONE_VAR: Record<ChartTone, string> = {
  primary: "--primary",
  destructive: "--destructive",
  warning: "--warning",
  success: "--success",
  secondary: "--secondary",
  muted: "--muted-foreground",
};

export interface UPlotChartProps {
  /** Epoch seconds, shared by every series. */
  timestamps: number[];
  series: ChartSeries[];
  unit?: ValueUnit;
  height: number;
  /** The figure's accessible name ("Request rate for api"). */
  label: string;
  /** A sparkline: no axes, legend or cursor. */
  compact?: boolean;
  className?: string;
}

export function UPlotChart({ timestamps, series, unit, height, label, compact = false, className }: UPlotChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const themeVersion = useThemeVersion();

  // uPlot wants columns: x, then one array per series.
  const data = useMemo<uPlot.AlignedData>(
    () => [timestamps, ...series.map((s) => s.values)] as uPlot.AlignedData,
    [timestamps, series]
  );

  useEffect(() => {
    const host = hostRef.current;
    if (!host || timestamps.length === 0) return;
    const palette = readPalette();
    const width = Math.max(host.clientWidth, 40);
    const tones = series.map((s, index) => s.tone ?? CATEGORICAL[index % CATEGORICAL.length]);

    const opts: uPlot.Options = {
      width,
      height,
      pxAlign: false,
      cursor: compact ? { show: false } : { drag: { x: false, y: false } },
      legend: { show: !compact },
      scales: { x: { time: true } },
      axes: compact
        ? [{ show: false }, { show: false }]
        : [
            { stroke: palette.axis, grid: { stroke: palette.grid, width: 1 }, ticks: { stroke: palette.grid } },
            {
              stroke: palette.axis,
              grid: { stroke: palette.grid, width: 1 },
              ticks: { stroke: palette.grid },
              size: 64,
              values: (_u, splits) => splits.map((value) => formatValue(value, unit)),
            },
          ],
      series: [
        {},
        ...series.map((s, index) => {
          const stroke = palette.tones[tones[index]];
          return {
            label: s.label,
            stroke,
            width: compact ? 1.25 : 1.5,
            fill: compact || series.length === 1 ? withAlpha(stroke, 0.12) : undefined,
            points: { show: false },
            spanGaps: false,
            value: (_u: uPlot, value: number | null) => formatValue(value, unit),
          };
        }),
      ],
    };

    let chart: uPlot | null = null;
    try {
      chart = new uPlot(opts, data, host);
    } catch {
      // No 2D context (a headless renderer, a GPU process that died): the
      // caption still carries the numbers, so draw nothing rather than throw.
      return;
    }

    const observer = typeof ResizeObserver === "undefined"
      ? null
      : new ResizeObserver(() => {
          const next = Math.max(host.clientWidth, 40);
          if (chart && next !== chart.width) chart.setSize({ width: next, height });
        });
    observer?.observe(host);
    return () => {
      observer?.disconnect();
      chart?.destroy();
    };
  }, [data, series, timestamps.length, height, unit, compact, themeVersion]);

  const caption = describeSeries(series, unit);

  return (
    <figure className={className} aria-label={label} role="group">
      <div ref={hostRef} aria-hidden="true" style={{ minHeight: height }} />
      <figcaption className="sr-only">{caption}</figcaption>
    </figure>
  );
}

/** "Requests: latest 12/s, between 3/s and 40/s. Errors: no data." */
export function describeSeries(series: ChartSeries[], unit: ValueUnit | undefined): string {
  return series
    .map((s) => {
      const present = s.values.filter((value): value is number => value !== null && Number.isFinite(value));
      if (present.length === 0) return `${s.label}: no data.`;
      const latest = present[present.length - 1];
      const min = Math.min(...present);
      const max = Math.max(...present);
      return `${s.label}: latest ${formatValue(latest, unit)}, between ${formatValue(min, unit)} and ${formatValue(max, unit)}.`;
    })
    .join(" ");
}

interface Palette {
  tones: Record<ChartTone, string>;
  axis: string;
  grid: string;
}

function readPalette(): Palette {
  const style = getComputedStyle(document.documentElement);
  const token = (name: string, fallback: string) => {
    const raw = style.getPropertyValue(name).trim();
    return raw ? `hsl(${raw})` : fallback;
  };
  const tones = Object.fromEntries(
    (Object.keys(TONE_VAR) as ChartTone[]).map((tone) => [tone, token(TONE_VAR[tone], "rgb(100, 116, 139)")])
  ) as Record<ChartTone, string>;
  return {
    tones,
    axis: token("--muted-foreground", "rgb(100, 116, 139)"),
    grid: withAlpha(token("--border", "rgb(203, 213, 225)"), 0.6),
  };
}

/** `hsl(217 91% 60%)` → `hsl(217 91% 60% / 0.12)`. Anything else passes through. */
function withAlpha(color: string, alpha: number): string {
  return color.startsWith("hsl(") && !color.includes("/") ? color.replace(/\)$/, ` / ${alpha})`) : color;
}

/** Bumps when the colour scheme or light/dark mode changes, so canvases redraw in the new palette. */
function useThemeVersion(): number {
  const [version, setVersion] = useState(0);
  useEffect(() => {
    if (typeof MutationObserver === "undefined") return;
    const observer = new MutationObserver(() => setVersion((v) => v + 1));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "data-color-scheme"] });
    return () => observer.disconnect();
  }, []);
  return version;
}
