// Copyright (c) 2025 Reliant Labs

/**
 * What a dashboard panel DRAWS, for every kind forge's spec has, from one
 * QueryPanel answer — the same component the dashboard grid, the dashboards
 * list's sparklines and the panel drawer's live preview render, so a panel
 * looks the same wherever it is.
 *
 *   line      uPlot, one line per series × group; release markers from the
 *             nearest ObsChartMarkersProvider; alert-firing bands; with a
 *             comparison, each series again, dashed, from the earlier window.
 *   number    the headline figure (one per series × group), with the change
 *             against the comparison when one is on.
 *   table     one row per group, one column per series.
 *   sql       response-shaped: time series, rows, or ungrouped aggregate figures.
 *   search    a list of rows, which is what Logs and Traces do: a way in.
 *   funnel    product events in order: how many journeys reached each step,
 *             of the first, and of the previous (C7, windowFunnel).
 *   issues    errors grouped by distinct stack, most events first.
 *   sessions  browser sessions, newest first, with what each hit.
 *
 * Every state is drawn here too — loading, empty, a panel the server cannot
 * compile (InvalidArgument names why), a failure — so no surface invents its
 * own.
 */

import type React from "react";
import type { ReactNode } from "react";
import { ArrowRight, Info } from "lucide-react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import { ObsWillLoad } from "../ObsStates";
import type { Panel, PanelResult, Series, Unit } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { attributeLabel } from "../attributes";
import { UPlotChart, type ChartBand } from "../charts/UPlotChart";
import { formatAgo, formatCount, formatDuration, formatPercent, formatValue } from "../format";
import { groupLabel } from "../obsFilters";
import { sessionTitle } from "../Sessions/sessionModel";

/** The way out of a panel into the explorers: what a series or row is, as a filter. */
export interface DrillTarget {
  /** The panel's title, for the menu's heading. */
  panel: string;
  /** The series' label, or the row's first value. */
  label: string;
  /** group-by field → value of the series or row. */
  group: Record<string, string>;
  signal: string;
}

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
}

export interface PanelBodyProps {
  panel: Panel;
  query: PanelQuery;
  /** The same panel over the comparison window, when a comparison is on. */
  compare?: { query: PanelQuery; label: string; offsetSec: number };
  /** The in-place group-by, which replaces the panel's own (the drawer and variables). */
  groupBy?: string[];
  bands?: readonly ChartBand[];
  links?: PanelLinks;
  /** Chart height in px; the panel's own rows decide it otherwise. */
  height?: number;
  /** A sparkline: no axes, legend, labels or table chrome. */
  compact?: boolean;
}

export function PanelBody({ panel, query, compare, groupBy, bands, links, height, compact = false }: PanelBodyProps) {
  if (panel.kind === "search") return <SearchLink panel={panel} href={links?.searchHref?.(panel)} />;
  if (!KNOWN_KINDS.has(panel.kind)) {
    return (
      <PanelNote testId="obs-panel-unknown-kind">
        This console doesn&apos;t draw <span className="font-mono">{panel.kind || "(no kind)"}</span> panels yet.
      </PanelNote>
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
      <p className="text-xs text-danger-ink" role="alert" data-testid="obs-panel-error">
        This panel couldn&apos;t be loaded. {obsErrorMessage(query.error)}
      </p>
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
  // SQL has no declared series or groupBy: Columns names the numeric values,
  // while every non-numeric column lives in the returned groups. Include keys
  // from ALL rows (not just the first row or the compact preview's three).
  const keys = panel.kind === "sql"
    ? [...new Set([...data.rows, ...data.series].flatMap((row) => Object.keys(row.group)))]
    : groupBy?.length ? groupBy : (panel.groupBy ?? []);
  const kind = panel.kind === "sql"
    ? data.series.length > 0 ? "line" : sqlAggregate(data) ? "number" : "table"
    : panel.kind;
  const truncated = data.truncated && !compact ? (
    <p className="mt-2 text-xs text-ink-muted">
      {panel.kind === "sql"
        ? `Only the first returned ${kind === "table" ? "rows" : "series or points"} are shown.`
        : `Only the largest ${TRUNCATED_WHAT[panel.kind] ?? "series"} are shown.`}
    </p>
  ) : null;

  switch (kind) {
    case "number":
      return (
        <NumberFigures
          panel={panel}
          data={panel.kind === "sql" ? sqlFigures(data) : data}
          unit={unit}
          keys={keys}
          compare={panel.kind === "sql" && compare?.query.data ? sqlFigures(compare.query.data) : compare?.query.data}
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
      return <FunnelSteps data={data} compact={compact} />;
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
          <LineChart
            panel={panel}
            data={data}
            unit={unit}
            keys={keys}
            compare={compare}
            bands={bands}
            onDrill={links?.onDrill}
            height={height ?? (compact ? 28 : Math.max((panel.height || 7) * 28 - 92, 120))}
            compact={compact}
          />
          {truncated}
        </>
      );
  }
}

const KNOWN_KINDS = new Set(["line", "number", "table", "sql", "search", "funnel", "issues", "sessions"]);

/** A single numeric-only row is an aggregate; never collapse multiple rows or groups. */
function sqlAggregate(data: PanelResult): boolean {
  return !data.truncated && data.rows.length === 1 && data.columns.length > 0 && Object.keys(data.rows[0].group).length === 0;
}

/** NumberFigures consumes Series.value, whereas SQL aggregates arrive as table values. */
function sqlFigures(data: PanelResult): PanelResult {
  if (!sqlAggregate(data)) return { ...data, series: [] };
  return {
    ...data,
    series: data.columns.map((label, index) => ({ label, group: {}, timestamps: [], values: [], value: data.rows[0].values[index] ?? null })),
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

/** "api — requests", "api" for a ratio's per-group series, or the series' own label ungrouped. */
export function seriesLabel(series: Series, panel: Panel, keys: string[] = []): string {
  const group = Object.keys(series.group).length ? groupLabel(series.group, keys.filter((k) => k in series.group)) : "";
  if (!group) return series.label;
  // SQL may return several numeric columns for each group without declaring
  // panel.series. Keep their response labels, even when one is named "ratio".
  if (panel.kind !== "sql" && (series.label === "ratio" || panel.series.length <= 1)) return group;
  return `${group} — ${series.label}`;
}

/** Every bucket any series has — a grouped panel's series can each skip buckets they had no rows in. */
export function unionTimestamps(series: { timestamps: number[] }[]): number[] {
  const all = new Set<number>();
  series.forEach((s) => s.timestamps.forEach((t) => all.add(t)));
  return [...all].sort((a, b) => a - b);
}

/** A series on the shared x axis, a missing bucket as a gap (null). */
export function alignTo(timestamps: number[], series: { timestamps: number[]; values: (number | null)[] }): (number | null)[] {
  const byTime = new Map(series.timestamps.map((t, i) => [t, series.values[i] ?? null]));
  return timestamps.map((t) => byTime.get(t) ?? null);
}

/**
 * A comparison series moved forward by `offsetSec` onto the current axis:
 * each current bucket takes the shifted bucket nearest it (within half a
 * step), so two windows whose buckets do not start on the same second still
 * line up.
 */
export function alignShifted(timestamps: number[], series: { timestamps: number[]; values: (number | null)[] }, offsetSec: number): (number | null)[] {
  if (timestamps.length === 0) return [];
  const step = timestamps.length > 1 ? timestamps[1] - timestamps[0] : 60;
  const shifted = series.timestamps.map((t) => t + offsetSec);
  return timestamps.map((t) => {
    let best = -1;
    let distance = Infinity;
    for (let i = 0; i < shifted.length; i++) {
      const d = Math.abs(shifted[i] - t);
      if (d < distance) {
        distance = d;
        best = i;
      }
    }
    return best >= 0 && distance <= step / 2 ? (series.values[best] ?? null) : null;
  });
}

/** Where a click happened, or — from the keyboard, which has no pointer — the element's bottom-left corner. */
function pointOf(event: React.MouseEvent | React.KeyboardEvent): { x: number; y: number } {
  if ("clientX" in event && (event.clientX || event.clientY)) return { x: event.clientX, y: event.clientY };
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  return { x: rect.left, y: rect.bottom };
}

const groupKey = (group: Record<string, string>) =>
  Object.keys(group)
    .sort()
    .map((k) => `${k}=${group[k]}`)
    .join("\u0000");

function LineChart({
  panel,
  data,
  unit,
  keys,
  compare,
  bands,
  onDrill,
  height,
  compact,
}: {
  panel: Panel;
  data: PanelResult;
  unit: Unit;
  keys: string[];
  compare?: PanelBodyProps["compare"];
  bands?: readonly ChartBand[];
  onDrill?: PanelLinks["onDrill"];
  height: number;
  compact: boolean;
}) {
  const timestamps = unionTimestamps(data.series);
  const current = data.series.map((s) => ({ label: seriesLabel(s, panel, keys), values: alignTo(timestamps, s), source: s }));
  const previous = compare?.query.data?.series ?? [];
  const compared = current.flatMap((c, index) => {
    const match = previous.find((p) => p.label === c.source.label && groupKey(p.group) === groupKey(c.source.group));
    if (!match) return [];
    return [{ label: `${c.label} (${compare!.label})`, values: alignShifted(timestamps, match, compare!.offsetSec), dashed: true, toneIndex: index }];
  });
  const tones = ["primary", "secondary", "success", "warning", "destructive", "muted"] as const;
  return (
    <div>
      <UPlotChart
        height={height}
        timestamps={timestamps}
        series={[
          ...current.map((c, index) => ({ label: c.label, values: c.values, tone: tones[index % tones.length] })),
          ...compared.map((c) => ({ label: c.label, values: c.values, dashed: true, tone: tones[c.toneIndex % tones.length] })),
        ]}
        unit={unit}
        label={panel.title}
        compact={compact}
        bands={bands}
        // The drill-down legend below names the series; uPlot's would repeat them.
        legend={!compact && !onDrill}
      />
      {!compact && onDrill && current.length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1" data-testid="obs-panel-legend">
          {current.map((c) => (
            <button
              key={c.label}
              type="button"
              onClick={(event) => onDrill({ panel: panel.title, label: c.label, group: c.source.group, signal: panel.signal }, pointOf(event))}
              className="inline-flex max-w-full items-center gap-1.5 truncate rounded text-xs text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              title={`Drill into ${c.label}`}
            >
              {c.label}
              <ArrowRight className="h-3 w-3 shrink-0 opacity-60" aria-hidden="true" />
            </button>
          ))}
          {compare && <span className="text-xs text-ink-muted">dashed: {compare.label}</span>}
        </div>
      )}
    </div>
  );
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
  return (
    <dl className={cn("grid gap-3", many ? "grid-cols-[repeat(auto-fill,minmax(7rem,1fr))]" : "grid-cols-1")} data-testid="obs-panel-number">
      {data.series.map((s, index) => {
        const before = compare?.series.find((p) => p.label === s.label && groupKey(p.group) === groupKey(s.group))?.value ?? null;
        const change = compareLabel ? describeChange(s.value, before, unit, compareLabel) : null;
        return (
          <div key={`${s.label}-${index}`} className="min-w-0">
            <dt className={many ? "truncate text-xs text-ink-muted" : "sr-only"}>{seriesLabel(s, panel, keys)}</dt>
            <dd className={cn("font-semibold tabular-nums text-ink", compact ? "text-base" : many ? "text-lg" : "text-2xl")}>{formatValue(s.value, unit)}</dd>
            {change && (
              <dd className="text-xs text-ink-muted" data-testid="obs-panel-change">
                {change}
              </dd>
            )}
          </div>
        );
      })}
    </dl>
  );
}

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
    if (panel.kind === "sql") return unitOf(panel, result);
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
          className="no-color inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2.5 text-xs font-medium text-ink hover:bg-ink/5"
        >
          Open in {toLogs ? "Logs" : "Traces"}
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      )}
    </div>
  );
}

// ── The browser kinds (C7) ──────────────────────────────────────────────────

/**
 * Funnel: one row per step — the event, how many journeys reached it, a bar
 * against the first step, and the drop from the step before. The biggest
 * drop is called out, since that is the question a funnel answers.
 */
function FunnelSteps({ data, compact }: { data: PanelResult; compact: boolean }) {
  const steps = data.funnelSteps;
  let worst = -1;
  steps.forEach((step, index) => {
    if (index === 0) return;
    if (worst < 0 || step.rateFromPrevious < steps[worst].rateFromPrevious) worst = index;
  });
  const overall = steps.length > 1 ? steps[steps.length - 1].rateFromFirst : 1;
  return (
    <div data-testid="obs-panel-funnel">
      {!compact && (
        <p className="mb-3 text-xs text-ink-muted">
          <span className="font-medium tabular-nums text-ink">{formatPercent(overall)}</span> of {formatCount(steps[0]?.count ?? 0)} journeys
          finished
          {worst > 0 && (
            <>
              {" "}
              · the biggest drop is into <span className="font-mono text-ink">{steps[worst].event}</span> ({formatPercent(1 - steps[worst].rateFromPrevious)} drop off)
            </>
          )}
        </p>
      )}
      <ol className="space-y-2">
        {(compact ? steps.slice(0, 3) : steps).map((step, index) => (
          <li key={`${step.event}-${index}`} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1" data-step={step.event}>
            <span className="truncate text-xs text-ink">
              <span className="mr-1.5 tabular-nums text-ink-muted">{index + 1}</span>
              <span className="font-mono">{step.event}</span>
            </span>
            <span className="text-right text-xs tabular-nums text-ink">
              {formatCount(step.count)}
              {index > 0 && !compact && <span className={cn("ml-2 text-ink-muted", index === worst && "text-warning-ink")}>{formatPercent(step.rateFromPrevious)} of previous</span>}
            </span>
            <span className="col-span-2 block h-1.5 overflow-hidden rounded-full bg-ink/5" aria-hidden="true">
              <span className="block h-full rounded-full bg-ink/40" style={{ width: `${Math.max(step.rateFromFirst * 100, step.count > 0 ? 1 : 0)}%` }} />
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}

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
