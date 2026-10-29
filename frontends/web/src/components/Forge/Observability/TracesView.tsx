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
import { AlertCircle, ArrowDown, ArrowUp, Search } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import { useListTraces, type TraceFilters } from "@/hooks/observability-queries";
import { obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { ObsScope, TraceRed, TraceRedGroup, TraceSummary } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { attributeLabel } from "./attributes";
import { formatCount, formatDuration, formatPercent, formatRate, formatTimestamp } from "./format";
import { ServiceSelect } from "./LogsView";
import { filterParam, formatFilterToken, groupLabel, type FilterToken } from "./obsFilters";
import type { ObsNavigate } from "./obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows, ObsNoData, OperatorBadge } from "./ObsStates";
import { TraceDetail } from "./TraceDetail";
import { useRovingFocus } from "./useRovingFocus";
import DataTable, { STACK, type DataTableColumn } from "@/components/forge-ui/data_table";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";

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
  navigate: ObsNavigate;
}) {
  const filters: TraceFilters = useMemo(
    () => ({
      range,
      rootSpanName,
      services: service ? [service] : [],
      errorsOnly,
      minDurationMs: MIN_DURATION_MS[minDuration ?? ""] ?? 0,
      groupBy,
    }),
    [range, rootSpanName, service, errorsOnly, minDuration, groupBy]
  );
  const traces = useListTraces(scope, filters);
  const rows = useMemo(() => traces.data?.pages.flatMap((page) => page.traces) ?? EMPTY, [traces.data]);
  const red = traces.data?.pages[0]?.red ?? null;
  const redGroups = traces.data?.pages[0]?.redGroups ?? [];
  const filterError = traces.error && obsErrorKind(traces.error) === "invalid" ? obsErrorMessage(traces.error) : null;
  const filtered = !!rootSpanName || !!service || errorsOnly || !!minDuration;

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
        navigate={navigate}
      />
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
      {traces.data && <ObsAsOf updatedAt={traces.dataUpdatedAt} />}
      {traces.isLoading ? (
        <Card padding="none">
          <ObsLoadingRows testId="obs-traces-loading" />
        </Card>
      ) : traces.error && !traces.data && !filterError ? (
        <ObsError error={traces.error} subject={subject} onRetry={() => void traces.refetch()} retrying={traces.isFetching} />
      ) : rows.length === 0 && !filterError ? (
        <ObsNoData
          what="traces"
          range={range}
          filtered={filtered}
          onWiden={() => navigate({ range: "7d" })}
          onClearFilters={() => navigate({ q: undefined, service: undefined, errors: undefined, minDuration: undefined })}
        />
      ) : rows.length > 0 ? (
        <TraceTable
          traces={rows}
          subject={subject}
          hasMore={!!traces.hasNextPage}
          loadingMore={traces.isFetchingNextPage}
          onLoadMore={() => void traces.fetchNextPage()}
          onOpen={(traceId) => navigate({ trace: traceId, span: undefined })}
        />
      ) : null}
    </div>
  );
}

const NO_KEYS: string[] = [];
const NO_TOKENS: FilterToken[] = [];

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
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  rootSpanName: string;
  filterError: string | null;
  service: string | undefined;
  errorsOnly: boolean;
  minDuration: string | undefined;
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
}: {
  traces: TraceSummary[];
  subject: string;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  onOpen: (traceId: string) => void;
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
                <span className="truncate font-mono text-ink">{trace.rootOperation || trace.traceId}</span>
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
    [maxDuration]
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
  // On a phone the five columns do not fit, so each trace is a stacked card
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
            <col className="w-64" />
            <col className="w-20" />
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
                <td colSpan={5} style={{ height: padTop }} />
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
                          aria-label={`Open trace ${trace.rootService} ${trace.rootOperation}, ${formatDuration(trace.rootDurationMs)}${trace.errorCount ? `, ${trace.errorCount} errors` : ""}`}
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
                <td colSpan={5} style={{ height: padBottom }} />
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
