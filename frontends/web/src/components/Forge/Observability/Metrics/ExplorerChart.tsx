// Copyright (c) 2025 Reliant Labs

/**
 * What the explorer draws from one QueryPanel answer.
 *
 *   line    uPlot (charts/UPlotChart) with the area's release markers; each
 *           series' still-open buckets dashed after its last complete point;
 *           with a comparison, each series again, dashed, from the earlier
 *           window; hidden series left out (their colour kept, so the rest do
 *           not change colour when one is hidden).
 *   bar     one bar per group, the heaviest first.
 *   number  the headline figures (Dashboards/PanelBody), with the change.
 *   table   one row per group (PanelBody).
 *
 * Above it: the server's notes — "showing 10 of 1,204 series" when the series
 * cap cut the result, and the warnings it raised — as one line of chips.
 */

import type { ReactNode } from "react";
import { AlertTriangle, Info } from "lucide-react";

import type { MetricAttribute } from "@/services/controlPlane/observability/metricsTypes";
import type { Panel, PanelResult, Series } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { UPlotChart, type ChartSeries, type ChartTone } from "../charts/UPlotChart";
import { alignShifted, alignTo, PanelBody, seriesLabel, unionTimestamps, type PanelQuery } from "../Dashboards/PanelBody";
import { formatCount, formatValue, type ValueUnit } from "../format";
import { capNote, fieldLabel, panelUnitOf, splitPartial, warningText } from "./seriesView";

/** The categorical order UPlotChart cycles through; the legend's swatches use the same. */
export const TONES: ChartTone[] = ["primary", "secondary", "success", "warning", "destructive", "muted"];

export const TONE_SWATCH: Record<ChartTone, string> = {
  primary: "bg-primary",
  secondary: "bg-secondary",
  success: "bg-success",
  warning: "bg-warning",
  destructive: "bg-destructive",
  muted: "bg-muted-foreground",
};

export interface DisplaySeries {
  /** What the legend and the hide list call it ("status=error — A"). */
  label: string;
  source: Series;
  tone: ChartTone;
  /** On the shared axis. */
  values: (number | null)[];
  partial: boolean[];
}

const groupKey = (group: Record<string, string>) =>
  Object.keys(group)
    .sort()
    .map((k) => `${k}=${group[k]}`)
    .join("\u0000");

/** Every series of a line answer on one time axis, each with its label and colour. */
export function displaySeries(panel: Panel, result: PanelResult): { timestamps: number[]; series: DisplaySeries[] } {
  const timestamps = unionTimestamps(result.series);
  const keys = panel.groupBy ?? [];
  const series = result.series.map((s, index) => {
    const byTime = new Map(s.timestamps.map((t, i) => [t, !!s.partial?.[i]]));
    return {
      label: seriesLabel(s, panel, keys),
      source: s,
      tone: TONES[index % TONES.length]!,
      values: alignTo(timestamps, s),
      partial: timestamps.map((t) => byTime.get(t) ?? false),
    };
  });
  return { timestamps, series };
}

export interface ExplorerChartProps {
  panel: Panel;
  query: PanelQuery;
  compare?: { query: PanelQuery; label: string; offsetSec: number };
  unit: ValueUnit;
  hidden: readonly string[];
  /** The open metric's attributes: a group-by on a high-cardinality one is called out. */
  attributes: readonly MetricAttribute[];
  height?: number;
  /** Drawn under a line chart: the exemplar rail. */
  footer?: ReactNode;
}

export function ExplorerChart({ panel, query, compare, unit, hidden, attributes, height = 260, footer }: ExplorerChartProps) {
  const data = query.data;
  return (
    <div className="space-y-2" data-testid="metrics-chart" data-kind={panel.kind}>
      {data && <ResultNotes panel={panel} result={data} attributes={attributes} />}
      {panel.kind === "line" && data && !query.isLoading && !query.error && data.series.some((s) => s.values.some((v) => v !== null)) ? (
        <>
          <LineChart panel={panel} result={data} compare={compare} unit={unit} hidden={hidden} height={height} />
          {footer}
        </>
      ) : panel.kind === "bar" && data && !query.isLoading && !query.error && (data.rows.length > 0 || data.series.length > 0) ? (
        <Bars panel={panel} result={data} unit={unit} />
      ) : (
        // Every state (loading, empty, refused, failed) and the number and table kinds are PanelBody's.
        <PanelBody panel={{ ...panel, kind: panel.kind === "bar" ? "table" : panel.kind, unit: panelUnitOf(unit) || panel.unit }} query={query} compare={compare} height={height} />
      )}
    </div>
  );
}

function LineChart({
  panel,
  result,
  compare,
  unit,
  hidden,
  height,
}: {
  panel: Panel;
  result: PanelResult;
  compare?: ExplorerChartProps["compare"];
  unit: ValueUnit;
  hidden: readonly string[];
  height: number;
}) {
  const { timestamps, series } = displaySeries(panel, result);
  const shown = series.filter((s) => !hidden.includes(s.label));
  const previous = compare?.query.data?.series ?? [];
  const lines: ChartSeries[] = [];
  for (const s of shown) {
    const { complete, open } = splitPartial(s.values, s.partial);
    lines.push({ label: s.label, values: complete, tone: s.tone });
    if (open.some((v) => v !== null)) lines.push({ label: `${s.label} (still filling)`, values: open, tone: s.tone, dashed: true });
  }
  if (compare) {
    for (const s of shown) {
      const match = previous.find((p) => p.label === s.source.label && groupKey(p.group) === groupKey(s.source.group));
      if (match) lines.push({ label: `${s.label} (${compare.label})`, values: alignShifted(timestamps, match, compare.offsetSec), tone: s.tone, dashed: true });
    }
  }
  const caption = `${panel.title || "Metrics"}${compare ? `, compared with the ${compare.label}` : ""}`;
  return (
    <div>
      {shown.length === 0 ? (
        <p className="py-10 text-center text-xs text-ink-muted" data-testid="metrics-chart-all-hidden">
          Every series is hidden. Show one from the table below.
        </p>
      ) : (
        <UPlotChart height={height} timestamps={timestamps} series={lines} unit={unit} label={caption} legend={false} />
      )}
      <p className="mt-1 text-xs text-ink-muted">
        {result.stepSeconds > 0 && <>One point per {stepText(result.stepSeconds)}. </>}
        {lines.some((l) => l.dashed && l.label.endsWith("(still filling)")) && <>Dashed at the end: buckets still filling. </>}
        {compare && <>Dashed: the {compare.label}.</>}
      </p>
    </div>
  );
}

function stepText(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

/** One bar per group, longest first: the reduced value over the whole window. */
function Bars({ panel, result, unit }: { panel: Panel; result: PanelResult; unit: ValueUnit }) {
  const keys = panel.groupBy ?? [];
  const label = (group: Record<string, string>, fallback: string) =>
    keys.length ? keys.map((k) => group[k] ?? "—").join(" · ") : fallback;
  const bars = (
    result.rows.length
      ? result.rows.map((r, i) => ({ key: `r${i}`, label: label(r.group, panel.series[0]?.label ?? ""), value: r.values[0] ?? null }))
      : result.series.map((s, i) => ({ key: `s${i}`, label: label(s.group, s.label), value: s.value ?? lastOf(s.values) }))
  ).sort((a, b) => (b.value ?? -Infinity) - (a.value ?? -Infinity));
  const max = Math.max(0, ...bars.map((b) => Math.abs(b.value ?? 0)));
  return (
    <figure aria-label={panel.title || "Metrics"} className="space-y-1.5" data-testid="metrics-bars">
      <figcaption className="sr-only">
        {bars.map((b) => `${b.label}: ${formatValue(b.value, unit)}`).join(". ")}
      </figcaption>
      {bars.map((b) => (
        <div key={b.key} className="grid grid-cols-[minmax(6rem,14rem)_minmax(0,1fr)_auto] items-center gap-3 text-xs" aria-hidden="true">
          <span className="truncate font-mono text-ink" title={b.label}>
            {b.label || "—"}
          </span>
          <span className="block h-2 overflow-hidden rounded-full bg-ink/5">
            <span className="block h-full rounded-full bg-primary" style={{ width: `${max ? (Math.abs(b.value ?? 0) / max) * 100 : 0}%` }} />
          </span>
          <span className="tabular-nums text-ink">{formatValue(b.value, unit)}</span>
        </div>
      ))}
    </figure>
  );
}

function lastOf(values: readonly (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) if (values[i] !== null) return values[i]!;
  return null;
}

/** The server's notes on the answer: the series cap, a high-cardinality group-by, and its warnings. */
function ResultNotes({ panel, result, attributes }: { panel: Panel; result: PanelResult; attributes: readonly MetricAttribute[] }) {
  const cap = capNote(result);
  const wide = (panel.groupBy ?? [])
    .map((field) => attributes.find((a) => fieldLabel(field) === a.key && a.highCardinality))
    .filter((a): a is MetricAttribute => !!a);
  if (!cap && wide.length === 0 && result.warnings.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5" data-testid="metrics-notes">
      {cap && (
        <Chip testId="metrics-cap-note" icon={<Info className="h-3 w-3" aria-hidden="true" />}>
          {cap.total
            ? `Showing ${cap.shown.toLocaleString()} of ${cap.total.toLocaleString()} series`
            : `Showing the ${formatCount(cap.shown)} heaviest series`}{" "}
          — add a filter{panel.kind === "table" || panel.kind === "bar" ? " or lower the top" : " or group by fewer keys"}
        </Chip>
      )}
      {wide.map((a) => (
        <Chip key={a.key} testId="metrics-high-cardinality-note" icon={<Info className="h-3 w-3" aria-hidden="true" />}>
          {a.key} has ~{formatCount(a.distinctEstimate)} values: only the heaviest series are kept
        </Chip>
      ))}
      {result.warnings.map((code) => (
        <Chip key={code} testId="metrics-warning" tone="warning" icon={<AlertTriangle className="h-3 w-3" aria-hidden="true" />} title={code}>
          {warningText(code)}
        </Chip>
      ))}
    </div>
  );
}

function Chip({ children, icon, tone = "neutral", testId, title }: { children: ReactNode; icon: ReactNode; tone?: "neutral" | "warning"; testId: string; title?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs",
        tone === "warning" ? "border-warning-ink/40 text-warning-ink" : "border-border text-ink-muted"
      )}
      data-testid={testId}
      title={title}
    >
      {icon}
      {children}
    </span>
  );
}
