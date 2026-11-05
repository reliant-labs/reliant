// Copyright (c) 2025 Reliant Labs

/**
 * Traces: the list of recent traces, and — when the URL names one — that
 * trace's waterfall.
 *
 * The list is a real <table> (columns a reader sorts and compares), virtualized
 * with spacer rows so a few hundred traces stay a screen of DOM. Each row's
 * operation is the button that opens it; the arrow keys move between those
 * buttons, so the table is one tab stop.
 *
 * With a group-by, the server also answers the RED summary per group, drawn
 * as a comparison table above the list: prod against staging, one version
 * against the next. Choosing a group narrows to it (adds its filters).
 *
 * Above the list, the latency chart plots the same loaded traces, one dot
 * each (traceScatter/TraceLatencyChart). Brushing a region of it narrows the
 * list to the traces inside; the region is page state, not URL state, since
 * it selects among the traces loaded so far.
 */

import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  createColumnHelper,
  createSortedRowModel,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  tableFeatures,
  useTable,
  type SortingState,
} from "@tanstack/react-table";
import { AlertCircle, ArrowDown, ArrowUp, MonitorPlay, Search, X } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import { useListTraces, type TraceFilters } from "@/hooks/observability-queries";
import { FilterUnsupportedError, obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { ObsScope, TraceRed, TraceRedGroup, TraceSummary } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { attributeLabel } from "./attributes";
import { formatCount, formatDuration, formatPercent, formatRate, formatTimestamp } from "./format";
import { ServiceSelect } from "./LogsView";
import { filterParam, formatFilterToken, groupLabel, type FilterToken } from "./obsFilters";
import type { ObsNavigate } from "./obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows, ObsNoData, OperatorBadge } from "./ObsStates";
import { Place } from "./Place";
import { SessionName } from "./Sessions/SessionName";
import { rumScopeOf, sessionLink } from "./Sessions/sessionModel";
import { TraceDetail } from "./TraceDetail";
import { TraceLatencyChart } from "./traceScatter/TraceLatencyChart";
import { inRegion, pointsOf, type TraceRegion } from "./traceScatter/traceScatterModel";
import { operationName } from "./traceTitle";
import { UserIdFilter } from "./UserIdFilter";
import { useRovingFocus } from "./useRovingFocus";
import DataTable, { STACK, type DataTableColumn } from "@/components/forge-ui/data_table";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";
import { hasClient, type ServiceBuild } from "@/services/controlPlane/observability/client";
import { clientLabel, ClientSummary } from "./Client";
import { ClientFilters, hasClientFilters, NO_CLIENT_FILTERS, type ClientFilterValues } from "./ClientFilters";

const features = tableFeatures({
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: { alphanumeric: sortFn_alphanumeric, basic: sortFn_basic },
});
const column = createColumnHelper<typeof features, TraceSummary>();

const EMPTY: TraceSummary[] = [];
const ROW_HEIGHT = 36;
/**
 * A phone's stacked trace card (root operation, then labelled lines for the
 * start, duration, spans and status). Only the virtualizer's first guess:
 * each card is measured once rendered.
 */
const STACKED_ROW_ESTIMATE = 176;

export const MIN_DURATIONS = ["100ms", "500ms", "1s", "5s"] as const;
const MIN_DURATION_MS: Record<string, number> = { "100ms": 100, "500ms": 500, "1s": 1000, "5s": 5000 };

export function TracesView(props: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  /** Substring of the root operation (the URL's `q`). */
  rootSpanName: string;
  service: string | undefined;
  errorsOnly: boolean;
  minDuration: string | undefined;
  traceId: string | undefined;
  spanId: string | undefined;
  groupBy?: string[];
  /** The scope's filter tokens, which a group's "narrow to it" extends. */
  filters?: FilterToken[];
  /** Only traces of this user id (the URL's `user`). */
  user?: string;
  /** Only traces of this browser session (the URL's `rsession`). */
  sessionId?: string;
  /** Only traces of this client and builds (the URL's `browser`, `os`, `fe`, `be`). */
  client?: ClientFilterValues;
  navigate: ObsNavigate;
}) {
  if (props.traceId) {
    return (
      <TraceDetail
        scope={props.scope}
        subject={props.subject}
        traceId={props.traceId}
        spanId={props.spanId}
        range={props.range}
        navigate={props.navigate}
      />
    );
  }
  return <TraceList {...props} />;
}

function TraceList({
  scope,
  subject,
  range,
  rootSpanName,
  service,
  errorsOnly,
  minDuration,
  groupBy = NO_KEYS,
  filters: scopeTokens = NO_TOKENS,
  user = "",
  sessionId,
  client = NO_FILTER_VALUES,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  rootSpanName: string;
  service: string | undefined;
  errorsOnly: boolean;
  minDuration: string | undefined;
  groupBy?: string[];
  filters?: FilterToken[];
  user?: string;
  sessionId?: string;
  client?: ClientFilterValues;
  navigate: ObsNavigate;
}) {
  const { browser, os, fe, be } = client;
  const filters: TraceFilters = useMemo(
    () => ({
      range,
      rootSpanName,
      services: service ? [service] : [],
      errorsOnly,
      minDurationMs: MIN_DURATION_MS[minDuration ?? ""] ?? 0,
      groupBy,
      userId: user || undefined,
      sessionId: sessionId || undefined,
      browser: browser || undefined,
      os: os || undefined,
      frontendVersion: fe || undefined,
      backendVersion: be || undefined,
    }),
    [range, rootSpanName, service, errorsOnly, minDuration, groupBy, user, sessionId, browser, os, fe, be]
  );
  const traces = useListTraces(scope, filters);
  const rows = useMemo(() => traces.data?.pages.flatMap((page) => page.traces) ?? EMPTY, [traces.data]);
  const red = traces.data?.pages[0]?.red ?? null;
  const redGroups = traces.data?.pages[0]?.redGroups ?? [];
  // A filter the server ignored is drawn in place of the list (ObsError), not under the search box.
  const filterError =
    traces.error && obsErrorKind(traces.error) === "invalid" && !(traces.error instanceof FilterUnsupportedError) ? obsErrorMessage(traces.error) : null;
  const filtered = !!rootSpanName || !!service || errorsOnly || !!minDuration || !!user || !!sessionId || hasClientFilters(client);
  const points = useMemo(() => pointsOf(rows), [rows]);
  // The brushed region belongs to the filters it was drawn over: new filters
  // load other traces, and a region kept across them would select nothing.
  const [selection, setSelection] = useState<{ filters: TraceFilters; region: TraceRegion } | null>(null);
  const region = selection?.filters === filters ? selection.region : null;
  const visibleRows = useMemo(() => {
    if (!region) return rows;
    const inside = new Set(points.filter((p) => inRegion(p, region)).map((p) => p.traceId));
    return rows.filter((row) => inside.has(row.traceId));
  }, [rows, points, region]);
  const failed = !!traces.error && !traces.data && !filterError;

  return (
    <div className="space-y-3" data-testid="obs-traces">
      <TraceToolbar
        scope={scope}
        range={range}
        rootSpanName={rootSpanName}
        filterError={filterError}
        service={service}
        errorsOnly={errorsOnly}
        minDuration={minDuration}
        user={user}
        navigate={navigate}
      />
      <ClientFilters
        id="obs-traces-client"
        fields={TRACE_CLIENT_FIELDS}
        values={client}
        onApply={(next) => navigate(next, { replace: true })}
      />
      {sessionId && <SessionFilterBar scope={scope} sessionId={sessionId} navigate={navigate} />}
      {red && red.traceCount > 0 && <RedStrip red={red} />}
      {groupBy.length > 0 && redGroups.length > 0 && (
        <RedGroups
          groups={redGroups}
          groupBy={groupBy}
          onNarrow={(group) => {
            const known = new Set(scopeTokens.map(formatFilterToken));
            const added = groupBy
              .filter((key) => group[key])
              .map((key): FilterToken => ({ key, op: "eq", value: group[key] }))
              .filter((token) => !known.has(formatFilterToken(token)));
            navigate({ f: filterParam([...scopeTokens, ...added]) });
          }}
        />
      )}
      <TraceLatencyChart
        status={traces.isLoading ? "loading" : failed ? "error" : "ready"}
        points={points}
        window={traces.data?.pages[0]?.window ?? null}
        complete={!traces.hasNextPage}
        total={red?.traceCount ?? null}
        filtered={filtered}
        region={region}
        onRegion={(next) => setSelection(next ? { filters, region: next } : null)}
        onOpen={(traceId) => navigate({ trace: traceId, span: undefined })}
      />
      {traces.data && <ObsAsOf updatedAt={traces.dataUpdatedAt} />}
      {region && rows.length > 0 && (
        <RegionBar region={region} shown={visibleRows.length} loaded={rows.length} onClear={() => setSelection(null)} />
      )}
      {traces.isLoading ? (
        <Card padding="none">
          <ObsLoadingRows testId="obs-traces-loading" />
        </Card>
      ) : failed ? (
        <ObsError error={traces.error} subject={subject} onRetry={() => void traces.refetch()} retrying={traces.isFetching} />
      ) : rows.length === 0 && !filterError ? (
        <ObsNoData
          what="traces"
          range={range}
          filtered={filtered}
          onWiden={() => navigate({ range: "7d" })}
          onClearFilters={() =>
            navigate({ q: undefined, service: undefined, errors: undefined, minDuration: undefined, user: undefined, rsession: undefined, ...NO_CLIENT_FILTERS })
          }
        />
      ) : region && visibleRows.length === 0 ? (
        <Card className="text-center" data-testid="obs-trace-region-empty">
          <p className="text-sm font-medium text-ink">No loaded traces fall in the selected region</p>
          <p className="mt-1 text-xs text-ink-muted">Drag a larger region on the chart, or clear the selection to see every loaded trace.</p>
          <Button variant="outline" size="sm" className="mt-3" onClick={() => setSelection(null)}>
            Clear selection
          </Button>
        </Card>
      ) : rows.length > 0 ? (
        <TraceTable
          traces={visibleRows}
          subject={subject}
          hasMore={!!traces.hasNextPage}
          loadingMore={traces.isFetchingNextPage}
          onLoadMore={() => void traces.fetchNextPage()}
          onOpen={(traceId) => navigate({ trace: traceId, span: undefined })}
          onUser={(id) => navigate({ user: id }, { replace: true })}
        />
      ) : null}
    </div>
  );
}

const NO_KEYS: string[] = [];
const NO_TOKENS: FilterToken[] = [];
const NO_FILTER_VALUES: ClientFilterValues = {};

/**
 * Every service of a trace with its own build: the frontend that made the call
 * (the browser root's service and its build id) first, then each backend
 * service that served it.
 */
function serviceBuildsOf(t: TraceSummary): ServiceBuild[] {
  const frontend = t.frontendVersion ? [{ service: t.rootService || "frontend", version: t.frontendVersion }] : [];
  return [...frontend, ...t.backendBuilds];
}
const TRACE_CLIENT_FIELDS = ["browser", "os", "fe", "be"] as const;

/** What the brushed region is, how many loaded traces it keeps, and the way out of it. */
function RegionBar({ region, shown, loaded, onClear }: { region: TraceRegion; shown: number; loaded: number; onClear: () => void }) {
  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-primary/40 bg-primary/5 px-3 py-1.5 text-xs"
      data-testid="obs-trace-region"
    >
      <span className="text-ink">
        <span className="font-medium tabular-nums">
          {formatCount(shown)} of {formatCount(loaded)}
        </span>{" "}
        loaded traces started {formatTimestamp(region.startMs)} to {formatTimestamp(region.endMs)} and took{" "}
        <span className="tabular-nums">
          {formatDuration(region.minMs)} to {formatDuration(region.maxMs)}
        </span>
      </span>
      <Button variant="ghost" size="sm" onClick={onClear} leftIcon={<X className="h-3.5 w-3.5" />} data-testid="obs-trace-region-clear">
        Clear selection
      </Button>
    </div>
  );
}

type RedGroupRow = { label: string; group: Record<string, string>; red: TraceRed };

/** The RED summary per group, worst error rate first: the comparison a group-by asks for. */
function RedGroups({ groups, groupBy, onNarrow }: { groups: TraceRedGroup[]; groupBy: string[]; onNarrow: (group: Record<string, string>) => void }) {
  const rows: RedGroupRow[] = [...groups]
    .sort((a, b) => b.red.errorRate - a.red.errorRate || b.red.traceCount - a.red.traceCount)
    .map(({ group, red }) => ({ label: groupLabel(group, groupBy), group, red }));
  const columns: DataTableColumn<RedGroupRow>[] = [
    { key: "label", header: groupBy.map(attributeLabel).join(" · "), render: (_, { label }) => <span className="font-mono">{label}</span> },
    { key: "traces", header: "Traces", numeric: true, render: (_, { red }) => formatCount(red.traceCount) },
    { key: "rate", header: "Rate", numeric: true, render: (_, { red }) => formatRate(red.ratePerSecond) },
    {
      key: "failed",
      header: "Failed",
      numeric: true,
      render: (_, { red }) => <span className={cn(red.errorRate >= 0.05 && "text-danger-ink")}>{formatPercent(red.errorRate)}</span>,
    },
    { key: "p50", header: "p50", numeric: true, render: (_, { red }) => formatDuration(red.p50Ms) },
    { key: "p95", header: "p95", numeric: true, render: (_, { red }) => formatDuration(red.p95Ms) },
    { key: "p99", header: "p99", numeric: true, render: (_, { red }) => formatDuration(red.p99Ms) },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="obs-trace-red-groups">
      <DataTable<RedGroupRow>
        flush
        stacked
        compact
        ariaLabel={`Rate, errors and latency of the matching traces per ${groupBy.map(attributeLabel).join(" and ")}`}
        columns={columns}
        data={rows}
        getRowKey={(row) => row.label}
        getRowAction={(row) => () => onNarrow(row.group)}
        getRowLabel={(row) => `Narrow to ${row.label}`}
        getRowProps={(row) => ({ "data-testid": `obs-trace-red-group-${row.label}` })}
      />
    </Card>
  );
}

/** Rate, errors and duration over every trace the filters match in the window — the server's figures, not this page's. */
function RedStrip({ red }: { red: TraceRed }) {
  const facts: [string, string, boolean?][] = [
    ["Traces", formatCount(red.traceCount)],
    ["Rate", formatRate(red.ratePerSecond)],
    ["Failed", formatPercent(red.errorRate), red.errorRate >= 0.05],
    ["p50", formatDuration(red.p50Ms)],
    ["p95", formatDuration(red.p95Ms)],
    ["p99", formatDuration(red.p99Ms)],
  ];
  return (
    <dl
      className="grid grid-cols-3 gap-3 rounded-lg border border-border bg-surface px-4 py-3 sm:grid-cols-6"
      aria-label="Summary of the matching traces"
      data-testid="obs-trace-red"
    >
      {facts.map(([label, value, alert]) => (
        <div key={label}>
          <dt className="text-xs font-medium uppercase tracking-wide text-ink-muted">{label}</dt>
          <dd className={cn("text-sm font-semibold tabular-nums", alert ? "text-danger-ink" : "text-ink")}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function TraceToolbar({
  scope,
  range,
  rootSpanName,
  filterError,
  service,
  errorsOnly,
  minDuration,
  user,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  rootSpanName: string;
  filterError: string | null;
  service: string | undefined;
  errorsOnly: boolean;
  minDuration: string | undefined;
  user: string;
  navigate: ObsNavigate;
}) {
  const [draft, setDraft] = useState(rootSpanName);
  useEffect(() => setDraft(rootSpanName), [rootSpanName]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    navigate({ q: draft.trim() || undefined });
  };
  return (
    <div className="flex flex-wrap items-start gap-2">
      <form role="search" onSubmit={submit} className="min-w-[16rem] flex-1">
        <label htmlFor="obs-trace-query" className="sr-only">
          Root operation contains
        </label>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-ink-muted" aria-hidden="true" />
          <input
            id="obs-trace-query"
            type="search"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Root operation contains, e.g. GetUser or orders.v1.OrdersService/"
            aria-invalid={filterError ? true : undefined}
            aria-describedby={filterError ? "obs-trace-query-error" : undefined}
            className={cn(
              "h-8 w-full rounded-md border bg-surface-sunken pl-8 pr-3 font-mono text-sm text-ink placeholder:font-sans placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              filterError ? "border-danger" : "border-border"
            )}
            data-testid="obs-trace-query"
          />
        </div>
        {filterError && (
          <p id="obs-trace-query-error" role="alert" className="mt-1 text-xs text-danger-ink">
            {filterError}
          </p>
        )}
      </form>
      <ServiceSelect
        scope={scope}
        range={range}
        value={service}
        onChange={(next) => navigate({ service: next }, { replace: true })}
        signal="traces"
      />
      <label htmlFor="obs-min-duration" className="sr-only">
        Minimum duration
      </label>
      <select
        id="obs-min-duration"
        value={minDuration ?? ""}
        onChange={(event) => navigate({ minDuration: event.target.value || undefined }, { replace: true })}
        className="h-8 rounded-md border border-border bg-surface-sunken px-2 text-sm text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="obs-min-duration"
      >
        <option value="">Any duration</option>
        {MIN_DURATIONS.map((d) => (
          <option key={d} value={d}>
            Slower than {d}
          </option>
        ))}
      </select>
      <Button
        variant={errorsOnly ? "primary" : "outline"}
        size="sm"
        aria-pressed={errorsOnly}
        onClick={() => navigate({ errors: errorsOnly ? undefined : true }, { replace: true })}
        leftIcon={<AlertCircle className="h-3.5 w-3.5" />}
        data-testid="obs-errors-only"
      >
        Errors only
      </Button>
      <UserIdFilter id="obs-traces-user" value={user} onChange={(next) => navigate({ user: next }, { replace: true })} />
    </div>
  );
}

/** The session the list is narrowed to, by name, with the way back to it and out of the filter. */
function SessionFilterBar({ scope, sessionId, navigate }: { scope: ObsScope; sessionId: string; navigate: ObsNavigate }) {
  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-primary/40 bg-primary/5 px-3 py-1.5 text-xs text-ink"
      data-testid="obs-traces-session"
    >
      <span>
        Traces of browser session <SessionName scope={rumScopeOf(scope)} sessionId={sessionId} />: its own spans and the backend requests it made.
      </span>
      <span className="flex gap-1">
        <Button variant="ghost" size="sm" onClick={() => navigate(sessionLink(sessionId))} leftIcon={<MonitorPlay className="h-3.5 w-3.5" />} data-testid="obs-traces-session-open">
          Open session
        </Button>
        <Button variant="ghost" size="sm" onClick={() => navigate({ rsession: undefined }, { replace: true })} leftIcon={<X className="h-3.5 w-3.5" />} data-testid="obs-traces-session-clear">
          All traces
        </Button>
      </span>
    </div>
  );
}

function TraceTable({
  traces,
  subject,
  hasMore,
  loadingMore,
  onLoadMore,
  onOpen,
  onUser,
}: {
  traces: TraceSummary[];
  subject: string;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  onOpen: (traceId: string) => void;
  /** Narrow the list to one user (a user cell's filter button). */
  onUser: (userId: string) => void;
}) {
  const maxDuration = useMemo(() => Math.max(...traces.map((t) => t.rootDurationMs), 1), [traces]);
  const columns = useMemo(
    () =>
      column.columns([
        column.accessor("startMs", {
          header: "Started",
          sortFn: "basic",
          cell: (info) => <span className="tabular-nums text-ink-muted">{formatTimestamp(info.getValue())}</span>,
        }),
        column.accessor("rootOperation", {
          header: "Root operation",
          sortFn: "alphanumeric",
          cell: (info) => {
            const trace = info.row.original;
            return (
              <span className="flex min-w-0 items-baseline gap-2">
                <span className="shrink-0 text-ink-muted">{trace.rootService || "unknown"}</span>
                <span className="truncate font-mono text-ink" title={trace.rootOperation || undefined}>
                  {operationName(trace.rootOperation) || trace.traceId}
                </span>
                <OperatorBadge provenance={trace} />
              </span>
            );
          },
        }),
        // The ROOT span's duration — the request as its caller saw it. The
        // whole trace can run longer (work a queue picked up afterwards), and
        // when it does, that is said beside it.
        column.accessor("rootDurationMs", {
          header: "Duration",
          sortFn: "basic",
          cell: (info) => (
            <span className="flex items-center gap-2">
              <span className="w-16 shrink-0 tabular-nums text-ink">{formatDuration(info.getValue())}</span>
              {info.row.original.durationMs > info.getValue() * 1.05 && (
                <span className="sr-only">, trace {formatDuration(info.row.original.durationMs)} in all</span>
              )}
              <span className="h-1.5 w-24 overflow-hidden rounded-full bg-border/60" aria-hidden="true">
                <span
                  className={cn("block h-full rounded-full", info.row.original.errorCount > 0 ? "bg-danger" : "bg-primary")}
                  style={{ width: `${Math.max((info.getValue() / maxDuration) * 100, 2)}%` }}
                />
              </span>
              {info.row.original.durationMs > info.getValue() * 1.05 && (
                <span className="shrink-0 text-2xs tabular-nums text-ink-muted" aria-hidden="true" title="Whole trace, including work after the root span returned">
                  {formatDuration(info.row.original.durationMs)} total
                </span>
              )}
            </span>
          ),
        }),
        column.accessor("userId", {
          header: "User",
          sortFn: "alphanumeric",
          cell: (info) => {
            const trace = info.row.original;
            return (
              <span className="flex min-w-0 flex-col leading-tight">
                {trace.userId ? (
                  <button
                    type="button"
                    onClick={() => onUser(trace.userId)}
                    className="truncate rounded-sm text-left font-mono text-xs text-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    title={`Only traces of ${trace.userId}`}
                    data-testid="obs-trace-user"
                  >
                    {trace.userId}
                  </button>
                ) : (
                  <span className="text-xs text-ink-muted">—</span>
                )}
                <Place geo={trace} className="text-2xs text-ink-muted" />
              </span>
            );
          },
        }),
        // Which client made the call, and each service's own build: the
        // services ship separately, so a regression reads as one service's
        // version, not one app-wide value.
        column.accessor((t) => clientLabel(t.client), {
          id: "client",
          header: "Client",
          sortFn: "alphanumeric",
          cell: (info) => {
            const trace = info.row.original;
            if (!hasClient(trace.client)) return <span className="text-xs text-ink-muted">—</span>;
            return <ClientSummary client={trace.client} className="text-xs text-ink" testId="obs-trace-client" />;
          },
        }),
        column.accessor((t) => serviceBuildsOf(t).map((b) => b.version).join(" "), {
          id: "versions",
          header: "Versions",
          sortFn: "alphanumeric",
          cell: (info) => {
            const builds = serviceBuildsOf(info.row.original);
            if (builds.length === 0) return <span className="text-xs text-ink-muted">—</span>;
            return (
              <span className="flex min-w-0 flex-col leading-tight" data-testid="obs-trace-versions">
                {builds.slice(0, 2).map((b) => (
                  <span key={`${b.service}@${b.version}`} className="truncate text-2xs" title={`${b.service} ${b.version}`}>
                    <span className="text-ink-muted">{b.service}</span> <span className="font-mono text-ink">{b.version}</span>
                  </span>
                ))}
                {builds.length > 2 && (
                  <span className="text-2xs text-ink-muted" title={builds.slice(2).map((b) => `${b.service} ${b.version}`).join("\n")}>
                    +{builds.length - 2} more
                  </span>
                )}
              </span>
            );
          },
        }),
        column.accessor("spanCount", {
          header: "Spans",
          sortFn: "basic",
          cell: (info) => <span className="tabular-nums text-ink-muted">{info.getValue()}</span>,
        }),
        column.accessor("errorCount", {
          header: "Status",
          sortFn: "basic",
          cell: (info) =>
            info.getValue() > 0 ? (
              <span className="text-danger-ink">
                {info.getValue()} error{info.getValue() === 1 ? "" : "s"}
              </span>
            ) : (
              <span className="text-ink-muted">OK</span>
            ),
        }),
      ]),
    [maxDuration, onUser]
  );

  const [sorting, setSorting] = useState<SortingState>([]);
  const table = useTable({
    features,
    columns,
    data: traces,
    getRowId: (row) => row.traceId,
    state: { sorting },
    onSortingChange: setSorting,
  });
  const rows = table.getRowModel().rows;

  const scrollRef = useRef<HTMLDivElement>(null);
  const requestFocus = useRovingFocus(scrollRef, "data-trace-index");
  // On a phone the eight columns do not fit, so each trace is a stacked card
  // (DataTable's STACK) whose height depends on its content: measure, don't assume.
  const narrow = useNarrowViewport();
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => (narrow ? STACKED_ROW_ESTIMATE : ROW_HEIGHT),
    getItemKey: (index) => rows[index]?.id ?? index,
    overscan: 10,
  });
  const items = virtualizer.getVirtualItems();
  const padTop = items[0]?.start ?? 0;
  const padBottom = items.length ? virtualizer.getTotalSize() - (items[items.length - 1]?.end ?? 0) : 0;

  const [active, setActive] = useState(0);
  const activeIndex = Math.min(active, rows.length - 1);
  const focusRow = (index: number) => {
    const clamped = Math.max(0, Math.min(rows.length - 1, index));
    setActive(clamped);
    virtualizer.scrollToIndex(clamped, { align: "auto" });
    requestFocus(clamped);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!(event.target instanceof HTMLElement) || !event.target.dataset.traceIndex) return;
    const moves: Record<string, number> = {
      ArrowDown: activeIndex + 1,
      ArrowUp: activeIndex - 1,
      Home: 0,
      End: rows.length - 1,
      PageDown: activeIndex + 10,
      PageUp: activeIndex - 10,
    };
    if (!(event.key in moves)) return;
    event.preventDefault();
    focusRow(moves[event.key]);
  };

  return (
    <Card padding="none" className="overflow-hidden">
      {/* The scroller sizes to its content up to 60vh. On a phone the stacked
          table's header is off screen, so with no rows yet it would measure
          0px, the virtualizer would render no rows into it, and it would stay
          0px: the floor breaks that loop (as LogList's does). */}
      <div ref={scrollRef} className="max-h-[60vh] overflow-auto max-md:min-h-[12rem]" onKeyDown={onKeyDown}>
        {/* Not a DataTable: it is virtualized (spacer rows) with roving focus,
            which DataTable does not do. It stacks on a phone with DataTable's
            own STACK classes, so the two layouts cannot drift apart. */}
        <table className={cn("w-full table-fixed text-sm", STACK.table)} data-testid="obs-trace-table">
          <caption className="sr-only">Traces from {subject}. Activate a root operation to open its waterfall.</caption>
          <colgroup className="max-md:hidden">
            <col className="w-36" />
            <col />
            <col className="w-56" />
            <col className="w-40" />
            <col className="w-48" />
            <col className="w-40" />
            <col className="w-16" />
            <col className="w-24" />
          </colgroup>
          <thead className={cn("sticky top-0 z-10 bg-surface", STACK.head)}>
            {table.getHeaderGroups().map((group) => (
              <tr key={group.id} className="border-b border-border">
                {group.headers.map((header) => {
                  const sorted = header.column.getIsSorted();
                  return (
                    <th
                      key={header.id}
                      scope="col"
                      aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : "none"}
                      className="px-3 py-2 text-left text-xs font-medium uppercase tracking-wide text-ink-muted"
                    >
                      <button
                        type="button"
                        onClick={header.column.getToggleSortingHandler()}
                        className="inline-flex items-center gap-1 rounded-sm uppercase hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <table.FlexRender header={header} />
                        {sorted === "asc" && <ArrowUp className="h-3 w-3" aria-hidden="true" />}
                        {sorted === "desc" && <ArrowDown className="h-3 w-3" aria-hidden="true" />}
                      </button>
                    </th>
                  );
                })}
              </tr>
            ))}
          </thead>
          <tbody className={STACK.body}>
            {padTop > 0 && (
              <tr aria-hidden="true">
                <td colSpan={6} style={{ height: padTop }} />
              </tr>
            )}
            {items.map((item) => {
              const row = rows[item.index];
              if (!row) return null;
              const trace = row.original;
              return (
                <tr
                  key={row.id}
                  className={cn("border-b border-border/50 hover:bg-ink/5", STACK.row)}
                  style={narrow ? undefined : { height: ROW_HEIGHT }}
                  data-index={item.index}
                  ref={narrow ? virtualizer.measureElement : undefined}
                  data-testid="obs-trace-row"
                  data-trace-id={trace.traceId}
                >
                  {row.getAllCells().map((cell) =>
                    cell.column.id === "rootOperation" ? (
                      <th key={cell.id} scope="row" className={cn("px-3 py-1 text-left font-normal", STACK.cell, STACK.title)}>
                        <button
                          type="button"
                          data-trace-index={item.index}
                          tabIndex={item.index === activeIndex ? 0 : -1}
                          onFocus={() => setActive(item.index)}
                          onClick={() => onOpen(trace.traceId)}
                          className="flex w-full min-w-0 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:min-h-[44px] touch:items-center"
                          aria-label={`Open trace ${trace.rootService} ${operationName(trace.rootOperation)}, ${formatDuration(trace.rootDurationMs)}${trace.errorCount ? `, ${trace.errorCount} errors` : ""}`}
                        >
                          <table.FlexRender cell={cell} />
                        </button>
                      </th>
                    ) : (
                      <td
                        key={cell.id}
                        data-label={typeof cell.column.columnDef.header === "string" ? cell.column.columnDef.header : undefined}
                        className={cn("truncate px-3 py-1", STACK.cell, STACK.labelled)}
                      >
                        <table.FlexRender cell={cell} />
                      </td>
                    )
                  )}
                </tr>
              );
            })}
            {padBottom > 0 && (
              <tr aria-hidden="true">
                <td colSpan={6} style={{ height: padBottom }} />
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {hasMore && (
        <div className="border-t border-border px-3 py-2">
          <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} disabled={loadingMore} data-testid="obs-traces-more">
            Load older
          </Button>
        </div>
      )}
    </Card>
  );
}
