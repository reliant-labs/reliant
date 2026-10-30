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
 *
 * ── MARKERS ─────────────────────────────────────────────────────────────────
 *
 * A marker is a moment on the time axis — a version first seen, or a release
 * promoted, from control-plane DeliveryInsightsService.ListReleases.
 * Each is drawn as a dashed vertical rule, summarized below the plot, and named in the
 * caption. A chart takes them from its `markers` prop or, absent that, from
 * the nearest ObsChartMarkersProvider — the seam that lets one provider put
 * release markers on every time-series chart of the area at once.
 *
 * ── AREA (opt-in) ───────────────────────────────────────────────────────────
 *
 * `area="fill"` tints under every solid series (overlapping, translucent);
 * `area="stacked"` stacks the solid series, so the top edge is their sum and
 * each band is one series' share. Stacking only means something for additive
 * values (counts, rates, sums) — the caller decides, the chart does not guess.
 * A dashed (comparison) series is never stacked or filled, and the caption and
 * the cursor read each series' own value, not the running total.
 *
 * ── EXEMPLARS (opt-in) ──────────────────────────────────────────────────────
 *
 * An exemplar is one sampled observation: a time and the value it had. With
 * `exemplars`, the chart places each at its true (time, value) on the plot,
 * using the chart's own scales (`valToPos`), as a button — a canvas dot could
 * not be focused or clicked, and uPlot's scales are internal. The buttons live
 * in a layer over the plot, outside the aria-hidden canvas. One whose time or
 * value lies outside the axis is not drawn (the axis follows the series, never
 * the exemplars); `onExemplarsPlaced` reports which were, so the caller can show
 * the rest elsewhere.
 */

import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";


import { cn } from "@/lib/utils";

import { formatValue, type ValueUnit } from "../format";

export type ChartTone = "primary" | "destructive" | "warning" | "success" | "secondary" | "muted";

export interface ChartSeries {
  label: string;
  values: (number | null)[];
  tone?: ChartTone;
  /**
   * Drawn dashed and unfilled: a comparison (the previous period, or the
   * window before the current release), laid over the series it compares.
   */
  dashed?: boolean;
}

/** One sampled observation, placed on the plot at its time and value. */
export interface ChartExemplar {
  /** Epoch seconds. */
  timeSec: number;
  /** In the axis' unit. */
  value: number;
  /** A solid dot (it has something to open) or a hollow one. */
  solid?: boolean;
  /** The dot's accessible name. */
  label: string;
}

/** Where a placed exemplar sits, in CSS pixels from the plot's top-left. */
export interface PlacedExemplar {
  index: number;
  x: number;
  y: number;
}

interface ExemplarLayout {
  /** The plot area within the chart, CSS pixels. */
  left: number;
  top: number;
  width: number;
  height: number;
  dots: PlacedExemplar[];
}

const NO_LAYOUT: ExemplarLayout = { left: 0, top: 0, width: 0, height: 0, dots: [] };

/**
 * A stretch of the time axis to shade: an alert firing. Drawn as a tinted
 * band behind the series, summarized below the plot and named in the caption.
 */
export interface ChartBand {
  /** Epoch seconds. */
  startSec: number;
  endSec: number;
  label: string;
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

/** A moment on the time axis: a deploy, a version first seen. */
export interface ChartMarker {
  /** Epoch seconds. */
  timeSec: number;
  /** Short, included in the annotation summary ("v1.8.2"). */
  label: string;
  /** Longer, for the caption ("prod promoted v1.8.2"). Defaults to `label`. */
  description?: string;
  tone?: ChartTone;
}

const ObsChartMarkersContext = createContext<readonly ChartMarker[]>([]);

/** Puts `markers` on every chart below that does not pass its own. */
export function ObsChartMarkersProvider({ markers, children }: { markers: readonly ChartMarker[]; children: ReactNode }) {
  return <ObsChartMarkersContext.Provider value={markers}>{children}</ObsChartMarkersContext.Provider>;
}

/** The markers the nearest provider puts on charts — for a chart that adds its own to them. */
export function useInheritedChartMarkers(): readonly ChartMarker[] {
  return useContext(ObsChartMarkersContext);
}

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
  /**
   * Moments to mark. Absent: the area's (ObsChartMarkersProvider). A sparkline
   * draws only markers handed to it here, without labels.
   */
  markers?: readonly ChartMarker[];
  /** Stretches to shade (alerts firing). Sparklines draw them too, without labels. */
  bands?: readonly ChartBand[];
  /** uPlot's own legend (series and the value under the cursor). Default: every chart but a sparkline. */
  legend?: boolean;
  /** Fill under the series, or stack them. Absent: today's look (a fill only under a lone series). */
  area?: "fill" | "stacked";
  /** Observations to place on the plot at their value. */
  exemplars?: readonly ChartExemplar[];
  /** A dot was activated; `at` is its bottom-left in viewport pixels, for anchoring a popover. */
  onExemplarOpen?: (index: number, at: { x: number; y: number }) => void;
  /** After each layout: the indexes of the exemplars drawn on the plot. Every other one lies outside the axes. */
  onExemplarsPlaced?: (placed: readonly number[]) => void;
  /**
   * The window that was asked for, in epoch seconds. The x axis spans it, so
   * one bucket sits where it falls in the window instead of filling a
   * viewport sized around itself. Absent: the axis follows the data.
   */
  window?: { startSec: number; endSec: number };
  /**
   * The timestamps are calendar dates (a SQL Date column), not instants: each
   * is that date's midnight in `timezone`. The axis and cursor label them as
   * dates in that zone, whatever zone the browser is in, and a note under the
   * chart says which. `bucketSeconds` names the bucket in that note.
   */
  calendar?: { timezone: "UTC"; bucketSeconds?: number };
}

const NO_BANDS: readonly ChartBand[] = [];

export function UPlotChart({
  timestamps,
  series,
  unit,
  height,
  label,
  compact = false,
  className,
  markers: own,
  bands = NO_BANDS,
  legend,
  area,
  exemplars,
  onExemplarOpen,
  onExemplarsPlaced,
  window: requested,
  calendar,
}: UPlotChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const [layout, setLayout] = useState<ExemplarLayout | null>(null);
  const layoutKey = useRef("");
  const placedCallback = useRef(onExemplarsPlaced);
  placedCallback.current = onExemplarsPlaced;
  const themeVersion = useThemeVersion();
  const inherited = useContext(ObsChartMarkersContext);
  const markers = useMemo(
    () => (compact ? (own ? visibleMarkers(own, timestamps) : []) : visibleMarkers(own ?? inherited, timestamps)),
    [compact, own, inherited, timestamps]
  );
  const shown = useMemo(() => visibleBands(bands, timestamps), [bands, timestamps]);
  const requestedStart = requested?.startSec;
  const requestedEnd = requested?.endSec;
  const extent = useMemo(
    () => timeExtent(timestamps, markers, requestedStart === undefined || requestedEnd === undefined ? undefined : { startSec: requestedStart, endSec: requestedEnd }),
    [timestamps, markers, requestedStart, requestedEnd]
  );
  const annotationCounts = [
    markers.length ? `${markers.length} release${markers.length === 1 ? "" : "s"}` : "",
    shown.length ? `${shown.length} alert${shown.length === 1 ? "" : "s"}` : "",
  ].filter(Boolean).join(" · ");
  const annotationSummary = annotationCounts
    ? `${annotationCounts}: ${[...markers.map((m) => m.label), ...shown.map((b) => b.label)].join(", ")}`
    : "";

  // uPlot wants columns: x, then one array per series.
  const stacked = area === "stacked" && !compact;
  const data = useMemo<uPlot.AlignedData>(
    () => [timestamps, ...(stacked ? stackValues(series) : series.map((s) => s.values))] as uPlot.AlignedData,
    [timestamps, series, stacked]
  );

  useEffect(() => {
    const host = hostRef.current;
    if (!host || timestamps.length === 0) return;
    const palette = readPalette();
    const width = Math.max(host.clientWidth, 40);
    const tones = series.map((s, index) => s.tone ?? CATEGORICAL[index % CATEGORICAL.length]);
    const layoutExemplars = (u: uPlot) => {
      if (!exemplars) return;
      const next = layoutOf(u, exemplars);
      const key = JSON.stringify(next);
      if (key === layoutKey.current) return;
      layoutKey.current = key;
      setLayout(next);
    };

    const opts: uPlot.Options = {
      width,
      height,
      pxAlign: false,
      // A calendar date is its UTC midnight: read in the browser's zone it is
      // the previous evening for anyone west of Greenwich.
      ...(calendar ? { tzDate: (ts: number) => uPlot.tzDate(new Date(ts * 1000), calendar.timezone) } : {}),
      cursor: compact ? { show: false } : { drag: { x: false, y: false } },
      legend: { show: legend ?? !compact },
      // The x axis reaches a marker in the last (or first) bucket's span: a
      // promotion 10 minutes ago is in the window even when the newest
      // bucket starts 20 minutes ago.
      scales: {
        x: {
          time: true,
          // uPlot 1.6.32 expands a singleton by 1000 days BEFORE this
          // callback. Never trust its min/max; use the actual observations.
          range: () => extent,
        },
        // An area is read against its baseline: start at zero, with headroom above.
        ...(area && !compact
          ? { y: { range: (_u: uPlot, min: number, max: number): [number, number] => [Math.min(0, min), max + Math.abs(max) * 0.1 || 1] } }
          : {}),
      },
      bands: stacked ? stackBands(series, tones, palette) : undefined,
      hooks:
        markers.length || shown.length || exemplars
          ? {
              // Bands go under the series (drawAxes runs before the series
              // paint); markers over them.
              drawAxes: shown.length ? [(u: uPlot) => drawBands(u, shown, palette)] : [],
              draw: [
                ...(markers.length ? [(u: uPlot) => drawMarkers(u, markers, palette)] : []),
                ...(exemplars ? [layoutExemplars] : []),
              ],
              ready: exemplars ? [layoutExemplars] : [],
            }
          : undefined,
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
        calendar ? { value: "{YYYY}-{MM}-{DD}" } : {},
        ...series.map((s, index) => {
          const stroke = palette.tones[tones[index]];
          const solid = series.filter((candidate) => !candidate.dashed).length;
          return {
            label: s.label,
            stroke: s.dashed ? withAlpha(stroke, 0.7) : stroke,
            width: compact ? 1.25 : 1.5,
            dash: s.dashed ? [4, 3] : undefined,
            fill: seriesFill(s, index, series, stroke, { area, compact, stacked, solid }),
            // A line cannot show a singleton or an observation surrounded
            // by nulls. Draw just those glyphs, never connect across a gap.
            // A series with under two observations has no line at all: its
            // marker is the whole series, so it is larger and solid.
            points: {
              show: false,
              size: presentCount(s.values) < 2 ? (compact ? 5 : 9) : compact ? 4 : 6,
              fill: stroke,
              filter: (u: uPlot, seriesIdx: number) => isolatedIndexes(u.data[seriesIdx]),
            },
            spanGaps: false,
            // A stacked column holds the running total; the cursor reads the series' own value.
            value: stacked
              ? (_u: uPlot, _value: number | null, _seriesIdx: number, idx: number | null) => formatValue(idx === null ? null : (s.values[idx] ?? null), unit)
              : (_u: uPlot, value: number | null) => formatValue(value, unit),
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
      if (exemplars) setLayout(NO_LAYOUT);
      return;
    }
    // uPlot fires `ready` once drawn; lay out here too in case a build does not.
    if (exemplars) layoutExemplars(chart);

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
  }, [data, series, timestamps.length, height, unit, compact, themeVersion, markers, shown, legend, area, stacked, exemplars, extent, calendar]);

  useEffect(() => {
    if (exemplars && layout) placedCallback.current?.(layout.dots.map((dot) => dot.index));
  }, [layout, exemplars]);

  const bucket = calendar ? bucketName(calendar.bucketSeconds) : "";
  const calendarNote = calendar ? `${bucket ? `${bucket} buckets` : "Buckets"}: ${calendar.timezone} calendar dates` : "";
  const caption = [describeSeries(series, unit), describeMarkers(markers), describeBands(shown), calendarNote && `${calendarNote}.`].filter(Boolean).join(" ");

  return (
    <figure className={exemplars ? cn("relative", className) : className} aria-label={label} role="group">
      <div ref={hostRef} aria-hidden="true" style={{ minHeight: height }} />
      {exemplars && layout && layout.dots.length > 0 && (
        <div
          className="pointer-events-none absolute"
          style={{ left: layout.left, top: layout.top, width: layout.width, height: layout.height }}
          data-testid="chart-exemplar-layer"
        >
          {layout.dots.map((dot) => {
            const exemplar = exemplars[dot.index];
            if (!exemplar) return null;
            return (
              <button
                key={dot.index}
                type="button"
                onClick={(event) => {
                  const rect = event.currentTarget.getBoundingClientRect();
                  onExemplarOpen?.(dot.index, { x: rect.left, y: rect.bottom });
                }}
                style={{ left: dot.x, top: dot.y }}
                className={cn(
                  "pointer-events-auto absolute h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full border border-primary transition-transform hover:scale-125 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
                  exemplar.solid ? "bg-primary" : "bg-surface-sunken"
                )}
                aria-label={exemplar.label}
                data-testid="chart-exemplar-dot"
                data-solid={exemplar.solid || undefined}
              />
            );
          })}
        </div>
      )}
      {!compact && calendarNote && (
        <p className="min-w-0 truncate text-xs text-ink-muted" data-testid="chart-bucket-note" aria-hidden="true">
          {calendarNote}
        </p>
      )}
      {!compact && annotationSummary && (
        <p className="min-w-0 truncate text-xs text-ink-muted" title={`${describeMarkers(markers)} ${describeBands(shown)}`} data-testid="chart-annotations" aria-hidden="true">
          {annotationSummary}
        </p>
      )}
      <figcaption className="sr-only">{caption}</figcaption>
    </figure>
  );
}

/**
 * The data columns of a stacked chart: each solid series' running total, in
 * order, so a column's top edge is the sum of it and every solid series before
 * it. A null point is a gap in that series (the total carries on without it);
 * a dashed series is left as it is.
 */
export function stackValues(series: readonly ChartSeries[]): (number | null)[][] {
  const length = Math.max(0, ...series.map((s) => s.values.length));
  const running = new Array<number>(length).fill(0);
  return series.map((s) => {
    if (s.dashed) return s.values;
    return s.values.map((value, i) => {
      if (value === null || !Number.isFinite(value)) return null;
      running[i] += value;
      return running[i];
    });
  });
}

/** One band per solid series after the first: between its running total and the one below. */
function stackBands(series: readonly ChartSeries[], tones: readonly ChartTone[], palette: Palette): uPlot.Band[] {
  const solid = series.map((s, index) => ({ s, index })).filter(({ s }) => !s.dashed);
  return solid.slice(1).map(({ index }, k) => ({
    // uPlot series are 1-based (0 is x).
    series: [index + 1, solid[k].index + 1] as [number, number],
    fill: withAlpha(palette.tones[tones[index]], STACK_ALPHA),
  }));
}

const STACK_ALPHA = 0.35;
const OVERLAP_ALPHA = 0.18;

function seriesFill(
  s: ChartSeries,
  index: number,
  all: readonly ChartSeries[],
  stroke: string,
  mode: { area: UPlotChartProps["area"]; compact: boolean; stacked: boolean; solid: number }
): string | undefined {
  if (s.dashed) return undefined;
  if (mode.stacked) {
    // The first solid series fills to the baseline; the rest are bands (stackBands).
    return all.findIndex((candidate) => !candidate.dashed) === index ? withAlpha(stroke, STACK_ALPHA) : undefined;
  }
  if (mode.area === "fill" && !mode.compact) return withAlpha(stroke, mode.solid === 1 ? 0.12 : OVERLAP_ALPHA);
  return mode.compact || mode.solid === 1 ? withAlpha(stroke, 0.12) : undefined;
}

/**
 * Where each exemplar sits on the plot, from the chart's own scales. An
 * exemplar outside the x or y axis is left out: the axis follows the series, so
 * a value above it has no honest position here.
 */
export function placeExemplars(u: Pick<uPlot, "valToPos" | "scales">, exemplars: readonly ChartExemplar[]): PlacedExemplar[] {
  const x = u.scales?.x;
  const y = u.scales?.y;
  if (x?.min == null || x.max == null || y?.min == null || y.max == null) return [];
  const placed: PlacedExemplar[] = [];
  exemplars.forEach((e, index) => {
    if (!Number.isFinite(e.value) || e.timeSec < x.min! || e.timeSec > x.max! || e.value < y.min! || e.value > y.max!) return;
    placed.push({ index, x: u.valToPos(e.timeSec, "x", false), y: u.valToPos(e.value, "y", false) });
  });
  return placed;
}

function layoutOf(u: uPlot, exemplars: readonly ChartExemplar[]): ExemplarLayout {
  if (!u.bbox) return NO_LAYOUT;
  const ratio = uPlot.pxRatio || 1;
  return {
    left: u.bbox.left / ratio,
    top: u.bbox.top / ratio,
    width: u.bbox.width / ratio,
    height: u.bbox.height / ratio,
    dots: placeExemplars(u, exemplars),
  };
}

/**
 * The x axis. With the window the caller asked for, that window: a bucket sits
 * where it falls in it, so one bucket of a day panel is one dot in a week, not
 * a chart sized around itself. A bucket is labelled by its start, which can
 * precede the window's (a calendar day that began before it did), so the axis
 * reaches back to the first one, and out to a marker or bucket past the end.
 *
 * Without a window, bounds come from observations, never uPlot's pre-expanded
 * singleton range. With no second timestamp there is no known bucket cadence:
 * use a one-minute viewport centered on the observation, not a fabricated
 * daily history. Markers have already been filtered to the observed bucket span.
 */
export function timeExtent(
  timestamps: readonly number[],
  markers: readonly ChartMarker[] = [],
  window?: { startSec: number; endSec: number }
): [number, number] {
  const first = timestamps[0];
  const last = timestamps[timestamps.length - 1];
  if (!Number.isFinite(first) || !Number.isFinite(last)) return window ? [window.startSec, window.endSec] : [0, 60];
  const min = Math.min(first, ...markers.map((m) => m.timeSec), ...(window ? [window.startSec] : []));
  const max = Math.max(last, ...markers.map((m) => m.timeSec), ...(window ? [window.endSec] : []));
  return min === max ? [min - 30, max + 30] : [min, max];
}

function presentCount(values: ArrayLike<number | null | undefined>): number {
  let n = 0;
  for (let i = 0; i < values.length; i++) if (values[i] != null && Number.isFinite(values[i])) n++;
  return n;
}

/** "1-day", "6-hour": how a bucket is named; empty when unknown. */
export function bucketName(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return "";
  if (seconds % 86_400 === 0) return `${seconds / 86_400}-day`;
  if (seconds % 3_600 === 0) return `${seconds / 3_600}-hour`;
  if (seconds % 60 === 0) return `${seconds / 60}-minute`;
  return `${seconds}-second`;
}

/** Only observations with no adjacent measured neighbour need a glyph. */
export function isolatedIndexes(values: ArrayLike<number | null | undefined>): number[] {
  const present = (i: number) => values[i] != null && Number.isFinite(values[i]);
  const indexes: number[] = [];
  for (let i = 0; i < values.length; i++) {
    if (present(i) && !present(i - 1) && !present(i + 1)) indexes.push(i);
  }
  return indexes;
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

/**
 * The markers inside the chart's time span, oldest first. A bucket's timestamp
 * is its start, so the span runs one bucket past the last one: a marker in the
 * newest bucket belongs on the chart.
 *
 * ONE MARKER PER LABEL: a version promoted twice in the window (re-promoted,
 * or rolled back to) is drawn at its latest landing, not listed twice.
 */
export function visibleMarkers(markers: readonly ChartMarker[], timestamps: number[]): ChartMarker[] {
  if (timestamps.length === 0) return [];
  const first = timestamps[0];
  const last = timestamps[timestamps.length - 1];
  const step = timestamps.length > 1 ? timestamps[1] - timestamps[0] : 0;
  const latest = new Map<string, ChartMarker>();
  for (const m of markers) {
    if (m.timeSec < first || m.timeSec > last + step) continue;
    const seen = latest.get(m.label);
    if (!seen || m.timeSec > seen.timeSec) latest.set(m.label, m);
  }
  return [...latest.values()].sort((a, b) => a.timeSec - b.timeSec);
}

/** "Marked: v1.8.2 at 14:05." */
export function describeMarkers(markers: readonly ChartMarker[]): string {
  if (markers.length === 0) return "";
  const at = (sec: number) => new Date(sec * 1000).toLocaleString(undefined, { hour: "2-digit", minute: "2-digit", month: "short", day: "numeric" });
  return `Marked: ${markers.map((m) => `${m.description ?? m.label} at ${at(m.timeSec)}`).join("; ")}.`;
}

/** The bands overlapping the chart's span, clipped to it, oldest first. */
export function visibleBands(bands: readonly ChartBand[], timestamps: number[]): ChartBand[] {
  if (timestamps.length === 0 || bands.length === 0) return [];
  const first = timestamps[0];
  const step = timestamps.length > 1 ? timestamps[1] - timestamps[0] : 0;
  const last = timestamps[timestamps.length - 1] + step;
  return bands
    .filter((band) => band.endSec >= first && band.startSec <= last)
    .map((band) => ({ ...band, startSec: Math.max(band.startSec, first), endSec: Math.min(band.endSec, last) }))
    .sort((a, b) => a.startSec - b.startSec);
}

/** "Shaded: HighErrorRate firing from 14:05 to 14:16." */
export function describeBands(bands: readonly ChartBand[]): string {
  if (bands.length === 0) return "";
  const at = (sec: number) => new Date(sec * 1000).toLocaleString(undefined, { hour: "2-digit", minute: "2-digit", month: "short", day: "numeric" });
  return `Shaded: ${bands.map((b) => `${b.label} from ${at(b.startSec)} to ${at(b.endSec)}`).join("; ")}.`;
}

function drawBands(u: uPlot, bands: readonly ChartBand[], palette: Palette) {
  const { ctx } = u;
  const { top, height } = u.bbox;
  const ratio = typeof window === "undefined" ? 1 : window.devicePixelRatio || 1;
  ctx.save();
  for (const band of bands) {
    const x0 = u.valToPos(band.startSec, "x", true);
    const x1 = Math.max(u.valToPos(band.endSec, "x", true), x0 + 2 * ratio);
    const color = palette.tones[band.tone ?? "destructive"];
    ctx.fillStyle = withAlpha(color, 0.1);
    ctx.fillRect(x0, top, x1 - x0, height);
    ctx.fillStyle = withAlpha(color, 0.55);
    ctx.fillRect(x0, top, x1 - x0, 2 * ratio);
  }
  ctx.restore();
}

function drawMarkers(u: uPlot, markers: readonly ChartMarker[], palette: Palette) {
  const { ctx } = u;
  const { top, height } = u.bbox;
  const ratio = typeof window === "undefined" ? 1 : window.devicePixelRatio || 1;
  ctx.save();
  for (const marker of markers) {
    const x = Math.round(u.valToPos(marker.timeSec, "x", true));
    const color = marker.tone ? palette.tones[marker.tone] : palette.axis;
    ctx.strokeStyle = color;
    ctx.fillStyle = color;
    ctx.lineWidth = ratio;
    ctx.setLineDash([4 * ratio, 3 * ratio]);
    ctx.beginPath();
    ctx.moveTo(x, top);
    ctx.lineTo(x, top + height);
    ctx.stroke();
  }
  ctx.restore();
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
