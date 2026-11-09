// Copyright (c) 2025 Reliant Labs

/**
 * What a dashboard panel DRAWS, for every kind forge's spec has, from one
 * QueryPanel answer — the same component the dashboard grid, the dashboards
 * list's sparklines and the panel drawer's live preview render, so a panel
 * looks the same wherever it is.
 *
 *   line      uPlot (SeriesChart): one line, area or bar series per series ×
 *             group (display.style, display.stack), coloured from the chart
 *             palette with errors in danger, a legend of swatches and figures
 *             that highlights and isolates, the top N with the rest as
 *             "Other"; release markers from the nearest
 *             ObsChartMarkersProvider; alert-firing bands; with a comparison,
 *             each series again, dashed, from the earlier window.
 *   number    the headline figure (one per series × group), its sparkline
 *             when the server sent one (or, on a sql panel, when its
 *             trendSql answered), the change against the comparison when
 *             one is on; or a gauge (display.style: gauge).
 *   bar       bars per group (upright, horizontal, stacked) or a pie or donut
 *             (CategoryChart).
 *   heatmap   time across, value buckets up, events per cell (HeatmapChart).
 *   table     one row per group, one column per series.
 *   sql       response-shaped: time series, rows, or ungrouped aggregate
 *             figures; its rows as a funnel with display.style: funnel, and
 *             as bars with display.style: bars (a histogram, a top N: rows
 *             with no time column), laid along the y axis when the labels
 *             are long or many; as a grid of shaded cells with
 *             display.style: heatmap (SqlHeatmap).
 *   search    a list of rows, which is what Logs and Traces do: a way in.
 *   text      the author's Markdown, with no query (TextPanel).
 *   funnel    product events in order: how many journeys reached each step,
 *             of the first, and the drop-off between (FunnelChart).
 *   issues    errors grouped by distinct stack, most events first.
 *   sessions  browser sessions, newest first, with what each hit.
 *
 * Colours: a series the dashboard names (display.colors) or a panel's one
 * colour (display.color) is drawn in it; the rest are dealt by rank
 * (seriesModel).
 *
 * A kind or a display style a newer forge added is never an error here: an
 * unknown kind says it is not drawn yet, an unknown style draws the kind's
 * default (the query is the kind's either way).
 *
 * Every state is drawn here too — loading, empty, a panel the server cannot
 * compile (InvalidArgument names why), a failure — so no surface invents its
 * own.
 */

import type { ReactNode } from "react";
import { ArrowRight, Info, RefreshCw } from "lucide-react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { isInFlightLimit, obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import { ObsWillLoad } from "../ObsStates";
import { DRAWN_KINDS } from "../obsdashSpec";
import type { Panel, PanelResult, Unit } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { attributeLabel } from "../attributes";
import { UPlotChart, type ChartBand } from "../charts/UPlotChart";
import { formatAgo, formatCount, formatDuration, formatPercent, formatValue } from "../format";
import { groupLabel } from "../obsFilters";
import { sessionTitle } from "../Sessions/sessionModel";
import { CategoryChart } from "./CategoryChart";
import { FunnelChart, funnelFromRows } from "./FunnelChart";
import { HeatmapChart } from "./HeatmapChart";
import { pointOf, TimeSeriesChart, type DrillTarget } from "./SeriesChart";
import { CHART_PALETTE, DANGER_PALETTE, intentOf, namedToken, seriesKey, seriesLabel, tokenColor } from "./seriesModel";
import { SqlHeatmap } from "./SqlHeatmap";
import { sqlAggregate } from "./viewerModel";
import { TextPanel } from "./TextPanel";

export type { DrillTarget } from "./SeriesChart";
export { alignShifted, alignTo, seriesLabel, unionTimestamps } from "./seriesModel";

export interface PanelLinks {
  /** Opens one trace (an issue's sample). */
  traceHref?: (traceId: string) => string;
  /** Opens one browser session (a sessions panel's row): its timeline and replay. */
  sessionHref?: (sessionId: string) => string;
  /** Logs / Traces for a search panel. */
  searchHref?: (panel: Panel) => string;
  /** A series or row clicked: the drill-down menu, opened at `at` (viewport px). */
  onDrill?: (target: DrillTarget, at: { x: number; y: number }) => void;
}

export interface PanelQuery {
  data: PanelResult | undefined;
  isLoading: boolean;
  error: unknown;
  /** The failures of the current fetch so far: above zero while it waits to retry. */
  failureCount?: number;
  /** The failure being retried. */
  failureReason?: unknown;
  isFetching?: boolean;
  /** Reads this panel again, and only this panel. Given, every failure offers it. */
  refetch?: () => unknown;
}

/** The server refused the read for its concurrent-read cap and the panel is waiting its turn to ask again. */
function waitingForCapacity(query: PanelQuery): boolean {
  return query.isLoading && !query.error && (query.failureCount ?? 0) > 0 && isInFlightLimit(query.failureReason);
}

function RetryButton({ query }: { query: PanelQuery }) {
  if (!query.refetch) return null;
  return (
    <button
      type="button"
      className="mt-2 inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-md border border-border-strong bg-surface px-2.5 text-xs font-medium text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
      onClick={() => void query.refetch?.()}
      disabled={query.isFetching}
      data-testid="obs-panel-retry"
    >
      <RefreshCw className="h-3 w-3" aria-hidden="true" />
      Retry
    </button>
  );
}

export interface PanelBodyProps {
  panel: Panel;
  query: PanelQuery;
  /** The same panel over the comparison window, when a comparison is on. */
  compare?: { query: PanelQuery; label: string; offsetSec: number };
  /** A sql panel's trendSql over the same window: the sparkline under its figure. */
  trend?: PanelQuery;
  /** The in-place group-by, which replaces the panel's own (the drawer and variables). */
  groupBy?: string[];
  bands?: readonly ChartBand[];
  links?: PanelLinks;
  /** Chart height in px; the panel's own rows decide it otherwise. */
  height?: number;
  /** A sparkline: no axes, legend, labels or table chrome. */
  compact?: boolean;
}

export function PanelBody({ panel, query, compare, trend, groupBy, bands, links, height, compact = false }: PanelBodyProps) {
  if (panel.kind === "search") return <SearchLink panel={panel} href={links?.searchHref?.(panel)} />;
  if (panel.kind === "text") return <TextPanel markdown={panel.markdown ?? ""} compact={compact} />;
  if (!DRAWN_KINDS.has(panel.kind)) {
    return (
      <PanelNote testId="obs-panel-unknown-kind">
        This console doesn&apos;t draw <span className="font-mono">{panel.kind || "(no kind)"}</span> panels yet.
      </PanelNote>
    );
  }
  if (waitingForCapacity(query)) {
    if (compact) return <p className="text-xs text-ink-muted" title="Queued…">…</p>;
    return (
      <p role="status" className="flex items-center gap-2 text-xs text-ink-muted" data-testid="obs-panel-waiting">
        <RefreshCw className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        Queued…
      </p>
    );
  }
  if (query.isLoading) {
    return <div className={cn("animate-pulse rounded bg-ink/5 motion-reduce:animate-none", compact ? "h-7" : "h-16")} aria-busy="true" aria-label="Loading" />;
  }
  if (query.error && !keepsLastResult(query.error, query.data)) {
    if (obsErrorKind(query.error) === "over-quota") return <ObsWillLoad error={query.error} />;
    if (compact) return <p className="text-xs text-ink-muted">—</p>;
    if (obsErrorKind(query.error) === "invalid") {
      return (
        <PanelNote testId="obs-panel-unsupported">
          This panel can&apos;t be computed: <span className="font-mono">{obsErrorMessage(query.error)}</span>
        </PanelNote>
      );
    }
    return (
      <div role="alert" data-testid="obs-panel-error">
        <p className="text-xs text-danger-ink">This panel couldn&apos;t be loaded. {obsErrorMessage(query.error)}</p>
        <RetryButton query={query} />
      </div>
    );
  }
  const data = query.data;
  if (!data || !hasData(panel, data)) {
    if (compact) return <p className="text-xs text-ink-muted">No data</p>;
    const absent = absentMetrics(data);
    return (
      <div className="py-4 text-center text-xs text-ink-muted" data-testid="obs-panel-empty">
        <p>{EMPTY_COPY[panel.kind] ?? "No data in this window"}</p>
        {absent.length > 0 && (
          <p className="mt-1" data-testid="obs-panel-absent">
            {absent.map((detail) => (
              <span key={detail} className="block font-mono">
                {detail}
              </span>
            ))}
          </p>
        )}
      </div>
    );
  }
  const unit = unitOf(panel, data);
  // A sql or postgres panel is drawn from what its statement returned.
  const statement = panel.kind === "sql" || panel.kind === "postgres";
  // SQL has no declared series or groupBy: Columns names the numeric values,
  // while every non-numeric column lives in the returned groups. Include keys
  // from ALL rows (not just the first row or the compact preview's three).
  const keys = statement
    ? [...new Set([...data.rows, ...data.series].flatMap((row) => Object.keys(row.group)))]
    : groupBy?.length ? groupBy : (panel.groupBy ?? []);
  const sqlFunnel = panel.kind === "sql" && panel.display?.style === "funnel" ? funnelFromRows(data) : null;
  const sqlHeatmap = panel.kind === "sql" && panel.display?.style === "heatmap";
  // "bars" on a sql panel draws its time series as bars; on rows with no time
  // column (a histogram's buckets, a top N) it draws the rows as bars.
  const sqlBars = panel.kind === "sql" && panel.display?.style === "bars" && data.series.length === 0 && data.rows.length > 0 && !sqlAggregate(data);
  const draw = panel.postgres?.draw;
  const kind = statement
    ? sqlFunnel
      ? "sql-funnel"
      : sqlHeatmap
        ? "sql-heatmap"
        : data.series.length > 0
        ? "line"
        : draw === "bar" || sqlBars
          ? "bar"
          : draw === "number" || (draw !== "table" && sqlAggregate(data))
            ? "number"
            : "table"
    : panel.kind;
  const chartHeight = height ?? (compact ? 28 : Math.max((panel.height || 7) * 28 - 92, 120));
  const truncated = data.truncated && !compact ? (
    <p className="mt-2 text-xs text-ink-muted">
      {statement
        ? `Only the first returned ${kind === "table" ? "rows" : "series or points"} are shown.`
        : `Only the largest ${TRUNCATED_WHAT[panel.kind] ?? "series"} are shown.`}
    </p>
  ) : null;

  switch (kind) {
    case "number":
      return (
        <NumberFigures
          panel={panel}
          data={statement ? sqlFigures(data, trend?.data) : data}
          unit={unit}
          keys={keys}
          compare={statement && compare?.query.data ? sqlFigures(compare.query.data) : compare?.query.data}
          compareLabel={compare?.label}
          compact={compact}
        />
      );
    case "table":
      return (
        <>
          <PanelTable panel={panel} result={data} groupFields={keys} onDrill={links?.onDrill} compact={compact} />
          {truncated}
        </>
      );
    case "funnel":
      return <FunnelChart steps={data.funnelSteps} compact={compact} />;
    case "sql-funnel":
      return <FunnelChart steps={sqlFunnel ?? []} compact={compact} label={data.columns[0] ?? "journeys"} />;
    case "sql-heatmap":
      return (
        <>
          <SqlHeatmap panel={panel} data={data} unit={unit} keys={keys} height={chartHeight} compact={compact} />
          {truncated}
        </>
      );
    case "bar":
      return (
        <>
          <CategoryChart
            panel={sqlBars ? sqlBarPanel(panel, data, keys) : panel}
            data={data}
            unit={unit}
            keys={keys}
            onDrill={links?.onDrill}
            height={chartHeight}
            compact={compact}
          />
          {truncated}
        </>
      );
    case "heatmap":
      return (
        <>
          <HeatmapChart cells={data.heatmap ?? []} unit={unit} height={chartHeight} compact={compact} label={panel.title} stepSeconds={data.stepSeconds} window={data.window} />
          {truncated}
        </>
      );
    case "issues":
      return (
        <>
          <IssuesList data={data} traceHref={links?.traceHref} compact={compact} />
          {truncated}
        </>
      );
    case "sessions":
      return (
        <>
          <SessionsList data={data} sessionHref={links?.sessionHref} compact={compact} />
          {truncated}
        </>
      );
    default:
      return (
        <>
          <TimeSeriesChart
            panel={panel}
            data={data}
            unit={unit}
            keys={keys}
            compare={compare}
            bands={bands}
            onDrill={links?.onDrill}
            height={chartHeight}
            compact={compact}
          />
          {truncated}
        </>
      );
  }
}



/** Labels longer than this, or more rows than SQL_BARS_UPRIGHT, lay a sql panel's bars along the y axis. */
const SQL_BAR_LABEL = 14;
const SQL_BARS_UPRIGHT = 12;

/**
 * A sql panel's rows drawn as bars. dashspec gives a sql panel no
 * "horizontal" (only a bar panel takes it), so the reader picks the
 * orientation: upright for a few short buckets ("0", "2-5", "51+"), along the
 * y axis for names that would not fit under a bar (pages, sources).
 */
export function sqlBarPanel(panel: Panel, data: PanelResult, keys: string[]): Panel {
  const long = data.rows.length > SQL_BARS_UPRIGHT || data.rows.some((row) => groupLabel(row.group, keys).length > SQL_BAR_LABEL);
  return { ...panel, display: { ...panel.display, horizontal: long } };
}


/**
 * NumberFigures consumes Series.value, whereas SQL aggregates arrive as table
 * values. A trend (the panel's trendSql, a time series) gives the figures
 * their sparkline: the series named like the figure, or, for a single figure,
 * the trend's only series.
 */
export function sqlFigures(data: PanelResult, trend?: PanelResult): PanelResult {
  if (!sqlAggregate(data)) return { ...data, series: [] };
  const lines = (trend?.series ?? []).filter((s) => Object.keys(s.group).length === 0);
  const lineOf = (label: string) => lines.find((s) => s.label === label) ?? (data.columns.length === 1 && lines.length === 1 ? lines[0] : undefined);
  return {
    ...data,
    series: data.columns.map((label, index) => {
      const line = lineOf(label);
      return { label, group: {}, timestamps: line?.timestamps ?? [], values: line?.values ?? [], value: data.rows[0].values[index] ?? null };
    }),
  };
}

const EMPTY_COPY: Record<string, string> = {
  funnel: "No journeys entered this funnel in the window",
  issues: "No errors in this window",
  sessions: "No browser sessions started in this window",
};

const TRUNCATED_WHAT: Record<string, string> = { table: "rows", issues: "50 issues", sessions: "50 sessions" };

function PanelNote({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <p className="flex items-start gap-2 text-xs text-ink-muted" data-testid={testId}>
      <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{children}</span>
    </p>
  );
}

/** The details of a result's metric_absent warnings: which metric has no data, and where. */
export function absentMetrics(result: PanelResult | undefined): string[] {
  const out: string[] = [];
  for (const warning of result?.warnings ?? []) {
    const [code, ...detail] = warning.split(": ");
    if (code === "metric_absent" && detail.length > 0) out.push(detail.join(": "));
  }
  return out;
}

export function hasData(panel: Pick<Panel, "kind">, result: PanelResult): boolean {
  switch (panel.kind) {
    case "funnel":
      return result.funnelSteps.some((step) => step.count > 0);
    case "issues":
      return result.issues.length > 0;
    case "heatmap":
      return (result.heatmap ?? []).some((cell) => cell.count > 0);
    case "sessions":
      return result.sessions.length > 0;
    default:
      return result.rows.length > 0 || result.series.some((s) => s.value !== null || s.values.some((v) => v !== null));
  }
}

const UNITS: readonly string[] = ["", "count", "ms", "bytes", "percent", "s"];

/** The panel's declared unit; the server's when the panel declares none. */
export function unitOf(panel: Panel, result: PanelResult): Unit {
  if (panel.unit) return panel.unit;
  return (UNITS.includes(result.unit) ? result.unit : "") as Unit;
}

/** "▲ 18ms vs previous period" — neutral: whether up is good depends on the figure, which the spec does not say. */
export function describeChange(now: number | null, before: number | null, unit: Unit, label: string): string | null {
  if (now === null || before === null) return null;
  const delta = now - before;
  if (delta === 0) return `no change vs ${label}`;
  const arrow = delta > 0 ? "▲" : "▼";
  if (unit === "percent") return `${arrow} ${formatCount(Math.abs(delta) * 100)}pt vs ${label}`;
  if (unit === "ms" || unit === "s" || unit === "bytes") return `${arrow} ${formatValue(Math.abs(delta), unit)} vs ${label}`;
  if (before === 0) return `${arrow} ${formatValue(Math.abs(delta), unit)} vs ${label}`;
  return `${arrow} ${formatPercent(Math.abs(delta) / Math.abs(before))} vs ${label}`;
}

function NumberFigures({
  panel,
  data,
  unit,
  keys,
  compare,
  compareLabel,
  compact,
}: {
  panel: Panel;
  data: PanelResult;
  unit: Unit;
  keys: string[];
  compare?: PanelResult;
  compareLabel?: string;
  compact: boolean;
}) {
  const many = data.series.length > 1;
  const gauge = panel.display?.style === "gauge" && !compact;
  return (
    <dl
      className={cn("grid gap-3", many ? (gauge ? "grid-cols-[repeat(auto-fill,minmax(9rem,1fr))]" : "grid-cols-[repeat(auto-fill,minmax(7rem,1fr))]") : "grid-cols-1")}
      data-testid="obs-panel-number"
      data-style={gauge ? "gauge" : "stat"}
    >
      {data.series.map((s, index) => {
        const before = compare?.series.find((p) => seriesKey(p) === seriesKey(s))?.value ?? null;
        const change = compareLabel ? describeChange(s.value, before, unit, compareLabel) : null;
        const label = seriesLabel(s, panel, keys);
        const thresholds = intentOf(s, panel)?.thresholds;
        // The server sends an ungrouped number's trend with it (a sql panel's
        // comes from its trendSql); two points make a line.
        const trend = !compact && !gauge && s.values.filter((v) => v !== null).length > 1;
        const token = namedToken(panel.display, [s.label, ...Object.values(s.group)], true) ?? CHART_PALETTE[0];
        return (
          <div key={`${s.label}-${index}`} className="min-w-0">
            <dt className={many ? "truncate text-xs text-ink-muted" : "sr-only"}>{label}</dt>
            {gauge ? (
              <dd>
                <Gauge value={s.value} unit={unit} min={panel.display?.min} max={panel.display?.max} thresholds={thresholds} label={label} token={token} />
              </dd>
            ) : (
              <dd className={cn("font-semibold tabular-nums", gradeInk(s.value, thresholds), compact ? "text-base" : many ? "text-lg" : "text-2xl")}>{formatValue(s.value, unit)}</dd>
            )}
            {change && (
              <dd className={cn("text-xs text-ink-muted", gauge && "text-center")} data-testid="obs-panel-change">
                {change}
              </dd>
            )}
            {trend && (
              <dd className="mt-1" data-testid="obs-panel-sparkline">
                <UPlotChart
                  height={many ? 24 : 36}
                  timestamps={s.timestamps}
                  series={[{ label, values: s.values, color: token }]}
                  unit={unit}
                  label={`${label}, over the window`}
                  compact
                />
              </dd>
            )}
          </div>
        );
      })}
    </dl>
  );
}

type Grade = "good" | "fair" | "poor";

/** Core Web Vitals' grading: at or below good is good, above poor is poor, between needs improvement. */
function gradeOf(value: number | null, thresholds?: { good: number; poor: number }): Grade | null {
  if (value === null || !thresholds) return null;
  if (value <= thresholds.good) return "good";
  if (value > thresholds.poor) return "poor";
  return "fair";
}

function gradeInk(value: number | null, thresholds?: { good: number; poor: number }): string {
  switch (gradeOf(value, thresholds)) {
    case "good":
      return "text-success-ink";
    case "fair":
      return "text-warning-ink";
    case "poor":
      return "text-danger-ink";
    default:
      return "text-ink";
  }
}

const GRADE_TOKEN: Record<Grade, string> = { good: "--success", fair: "--warning", poor: DANGER_PALETTE[0] };

/**
 * A half-dial from min to max: the arc filled to the value, in the series'
 * threshold grade when it has thresholds, else its colour (named, or the palette's first). A
 * conic gradient masked to a ring — no hand-drawn arc. Without a max, a
 * percent panel's scale is 0 to 1 (forge requires a max otherwise).
 */
function Gauge({ value, unit, min = 0, max, thresholds, label, token = CHART_PALETTE[0] }: { value: number | null; unit: Unit; min?: number; max?: number; thresholds?: { good: number; poor: number }; label: string; token?: string }) {
  const top = max ?? (unit === "percent" ? 1 : Math.max(value ?? 0, 1));
  const span = top - min;
  const fraction = value === null || !(span > 0) ? 0 : Math.min(Math.max((value - min) / span, 0), 1);
  const grade = gradeOf(value, thresholds);
  const fill = tokenColor(grade ? GRADE_TOKEN[grade] : token);
  const track = "hsl(var(--foreground) / 0.08)";
  const degrees = fraction * 180;
  return (
    <div className="mx-auto w-full max-w-[11rem]" data-testid="obs-panel-gauge" role="meter" aria-label={label} aria-valuemin={min} aria-valuemax={top} aria-valuenow={value ?? undefined} aria-valuetext={formatValue(value, unit)}>
      <div className="relative aspect-[2/1] w-full overflow-hidden">
        <div
          className="absolute inset-x-0 top-0 aspect-square w-full rounded-full"
          style={{
            background: `conic-gradient(from 270deg, ${fill} 0deg ${degrees}deg, ${track} ${degrees}deg 180deg, transparent 180deg)`,
            mask: GAUGE_MASK,
            WebkitMask: GAUGE_MASK,
          }}
          aria-hidden="true"
        />
        <span className={cn("absolute inset-x-0 bottom-0 text-center text-xl font-semibold tabular-nums", gradeInk(value, thresholds))}>{formatValue(value, unit)}</span>
      </div>
      <div className="mt-0.5 flex justify-between text-2xs tabular-nums text-ink-muted" aria-hidden="true">
        <span>{formatValue(min, unit)}</span>
        <span>{formatValue(top, unit)}</span>
      </div>
    </div>
  );
}

const GAUGE_MASK = "radial-gradient(circle closest-side, transparent 68%, black 69%)";

/** One row per group, one column per series. A latency column is milliseconds whatever the panel's unit. */
type PanelTableRow = { row: PanelResult["rows"][number]; index: number };

function PanelTable({
  panel,
  result,
  groupFields,
  onDrill,
  compact,
}: {
  panel: Panel;
  result: PanelResult;
  groupFields: string[];
  onDrill?: PanelLinks["onDrill"];
  compact: boolean;
}) {
  const columnUnit = (index: number): Unit => {
    if (panel.kind === "sql" || panel.kind === "postgres") return unitOf(panel, result);
    const intent = panel.series[index];
    if (intent?.agg === "quantile" && intent.field === "span.duration") return "ms";
    if (intent?.agg === "count" || intent?.agg === "count_distinct") return "count";
    return panel.unit ?? "";
  };
  const rows = compact ? result.rows.slice(0, 3) : result.rows;
  const columns: DataTableColumn<PanelTableRow>[] = [
    ...groupFields.map(
      (field, fieldIndex): DataTableColumn<PanelTableRow> => ({
        key: `group:${field}`,
        header: attributeLabel(field),
        className: "max-w-[24rem] truncate",
        render: (_, { row }) => <span className={cn("text-xs", fieldIndex > 0 && "text-ink-muted")}>{row.group[field] || "—"}</span>,
      })
    ),
    ...result.columns.map(
      (label, index): DataTableColumn<PanelTableRow> => ({
        key: `value:${index}`,
        header: label,
        numeric: true,
        render: (_, { row }) => <span className="text-xs">{formatValue(row.values[index], columnUnit(index))}</span>,
      })
    ),
  ];
  return (
    <div className={cn(!compact && "max-h-80 overflow-auto")} data-testid="obs-panel-table">
      <DataTable<PanelTableRow>
        flush
        stacked
        compact
        ariaLabel={panel.title}
        columns={columns}
        data={rows.map((row, index) => ({ row, index }))}
        getRowKey={({ index }) => index}
        getRowAction={
          onDrill
            ? ({ row }) =>
                (event) =>
                  onDrill({ panel: panel.title, label: groupLabel(row.group, groupFields), group: row.group, signal: panel.signal }, pointOf(event))
            : undefined
        }
      />
    </div>
  );
}

/** A search panel lists rows; the Logs and Traces explorers do that. */
function SearchLink({ panel, href }: { panel: Panel; href?: string }) {
  const toLogs = panel.signal === "logs";
  return (
    <div className="flex items-center justify-between gap-3" data-testid="obs-panel-link">
      <p className="text-xs text-ink-muted">{toLogs ? "Log records matching this panel." : "Spans matching this panel."}</p>
      {href && (
        <Link
          href={href}
          className="no-color inline-flex h-7 items-center gap-1 rounded-md border border-border-strong bg-surface px-2.5 text-xs font-medium text-ink hover:bg-ink/5"
        >
          Open in {toLogs ? "Logs" : "Traces"}
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      )}
    </div>
  );
}

// ── The browser kinds (C7) ──────────────────────────────────────────────────

/** Issues: one row per distinct stack — what, where, how often, since when, and a sample trace. */
type PanelIssueRow = { issue: PanelResult["issues"][number] };

function IssuesList({ data, traceHref, compact }: { data: PanelResult; traceHref?: (traceId: string) => string; compact: boolean }) {
  const issues = compact ? data.issues.slice(0, 3) : data.issues;
  const columns: DataTableColumn<PanelIssueRow>[] = [
    {
      key: "issue",
      header: "Issue",
      className: "w-full max-w-0",
      render: (_, { issue }) => (
        <span className="block text-xs">
          <span className="block truncate">
            <span className="font-medium">{issue.exceptionType || "Error"}</span>
            {issue.message && <span className="text-ink-muted">: {issue.message}</span>}
          </span>
          {!compact && (issue.urlPath || issue.serviceVersion) && (
            <span className="block truncate text-ink-muted">
              {issue.urlPath && <span className="font-mono">{issue.urlPath}</span>}
              {issue.urlPath && issue.serviceVersion && " · "}
              {issue.serviceVersion && <span>build {issue.serviceVersion}</span>}
            </span>
          )}
        </span>
      ),
    },
    ...(compact
      ? []
      : [{ key: "service", header: "Service", render: (_: unknown, { issue }: PanelIssueRow) => <span className="text-xs text-ink-muted">{issue.serviceName}</span> }]),
    { key: "events", header: "Events", numeric: true, render: (_, { issue }) => <span className="text-xs">{formatCount(issue.events)}</span> },
    ...(compact
      ? []
      : ([
          { key: "sessions", header: "Sessions", numeric: true, render: (_, { issue }) => <span className="text-xs">{formatCount(issue.sessions)}</span> },
          {
            key: "lastSeen",
            header: "Last seen",
            render: (_, { issue }) => (
              <time className="text-xs text-ink-muted" dateTime={new Date(issue.lastSeenMs).toISOString()} title={new Date(issue.lastSeenMs).toLocaleString()}>
                {formatAgo(issue.lastSeenMs)}
              </time>
            ),
          },
          {
            key: "sample",
            header: "Sample",
            srHeader: true,
            interactive: true,
            className: "text-right",
            render: (_, { issue }) =>
              issue.sampleTraceId && traceHref ? (
                <Link href={traceHref(issue.sampleTraceId)} className="no-color text-xs text-ink underline decoration-ink-muted/50 underline-offset-2 hover:decoration-ink">
                  Open trace
                </Link>
              ) : null,
          },
        ] satisfies DataTableColumn<PanelIssueRow>[])),
  ];
  return (
    <div className={cn(!compact && "max-h-96 overflow-auto")} data-testid="obs-panel-issues">
      <DataTable<PanelIssueRow>
        flush
        stacked
        compact
        ariaLabel="Issues"
        columns={columns}
        data={issues.map((issue) => ({ issue }))}
        getRowKey={({ issue }) => `${issue.serviceName}-${issue.stackHash}`}
        getRowProps={({ issue }) => ({ className: "align-top", "data-stack": issue.stackHash })}
      />
    </div>
  );
}

/** Sessions: who, where they came in, how long, what they did, and whether it went wrong. */
type PanelSessionRow = { session: PanelResult["sessions"][number] };

function SessionsList({ data, sessionHref, compact }: { data: PanelResult; sessionHref?: (sessionId: string) => string; compact: boolean }) {
  const sessions = compact ? data.sessions.slice(0, 3) : data.sessions;
  const now = Date.now();
  const columns: DataTableColumn<PanelSessionRow>[] = [
    {
      key: "session",
      header: "Session",
      interactive: true,
      render: (_, { session }) => (
        <span className="inline-flex items-center gap-1.5 text-xs">
          {sessionHref ? (
            <Link
              href={sessionHref(session.sessionId)}
              className="no-color text-ink underline decoration-ink-muted/50 underline-offset-2 hover:decoration-ink"
              title={session.hasReplay ? "Open this session and watch its replay" : "Open this session's timeline"}
              data-testid={`obs-panel-session-${session.sessionId}`}
            >
              {sessionTitle(session.serviceName, session.startMs, { now })}
            </Link>
          ) : (
            <span>{sessionTitle(session.serviceName, session.startMs, { now })}</span>
          )}
          {!compact && <Mono value={session.sessionId} length={8} />}
          {session.hasReplay && !compact && <Tag title="This session's replay was recorded">replay</Tag>}
        </span>
      ),
    },
    ...(compact
      ? []
      : ([
          {
            key: "user",
            header: "User",
            className: "max-w-[10rem] truncate",
            render: (_, { session }) => <span className="text-xs">{session.userId || <span className="text-ink-muted">anonymous</span>}</span>,
          },
          {
            key: "entry",
            header: "Entry",
            className: "max-w-[14rem] truncate",
            render: (_, { session }) => <span className="font-mono text-xs text-ink-muted">{session.entryPath || "/"}</span>,
          },
          {
            key: "duration",
            header: "Duration",
            numeric: true,
            render: (_, { session }) => <span className="text-xs">{formatDuration(Math.max(0, session.endMs - session.startMs))}</span>,
          },
          { key: "pages", header: "Pages", numeric: true, render: (_, { session }) => <span className="text-xs">{formatCount(session.pageViews)}</span> },
        ] satisfies DataTableColumn<PanelSessionRow>[])),
    {
      key: "errors",
      header: "Errors",
      render: (_, { session }) =>
        session.errors > 0 ? (
          <Status tone="danger" label={`${formatCount(session.errors)} error${session.errors === 1 ? "" : "s"}`} />
        ) : (
          <Status tone="neutral" label="none" />
        ),
    },
  ];
  return (
    <div className={cn(!compact && "max-h-96 overflow-auto")} data-testid="obs-panel-sessions">
      <DataTable<PanelSessionRow>
        flush
        stacked
        compact
        ariaLabel="Sessions"
        columns={columns}
        data={sessions.map((session) => ({ session }))}
        getRowKey={({ session }) => session.sessionId}
        getRowProps={({ session }) => ({ "data-session": session.sessionId })}
      />
    </div>
  );
}
