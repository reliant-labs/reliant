// Copyright (c) 2025 Reliant Labs

/**
 * Logs: search, filter, tail.
 *
 * ── THE QUERY BOX IS THE SERVER'S ───────────────────────────────────────────
 *
 * What is typed is sent verbatim as the query string, and the control plane
 * parses and compiles it — the browser never builds a filter out of it, and
 * never sends SQL. A parse error comes back as InvalidArgument and is shown
 * under the box that caused it.
 *
 * ── ROWS ARE DISCLOSURES ────────────────────────────────────────────────────
 *
 * A log line is read in one glance and expanded for its attributes, so each
 * row is a button with aria-expanded over a details region, in a virtualized
 * list (thousands of lines stay one screen of DOM). The list is ONE tab stop;
 * the arrow keys, Home and End move between rows, and Enter or Space opens
 * one — the roving-tabindex pattern.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { createColumnHelper, tableFeatures, useTable } from "@tanstack/react-table";
import { ChevronRight, GitBranch, Radio, Search } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useSearchLogs, useServiceOverview, type LogFilters } from "@/hooks/observability-queries";
import { obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { SEVERITIES, type ObsScope, type LogRecord, type Severity } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { AttributeTable } from "./AttributeTable";
import { formatTimestamp } from "./format";
import { severityParam, type ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows, ObsNoData, OperatorBadge } from "./ObsStates";
import { useRovingFocus } from "./useRovingFocus";

const features = tableFeatures({});
const column = createColumnHelper<typeof features, LogRecord>();
const columns = column.columns([
  column.accessor("timeMs", { header: "Time", cell: (info) => formatTimestamp(info.getValue()) }),
  column.accessor("severity", {
    header: "Level",
    cell: (info) => <SeverityLabel severity={info.getValue()} text={info.row.original.severityText} />,
  }),
  column.accessor("service", {
    header: "Service",
    cell: (info) => (
      <span className="flex min-w-0 items-center gap-1">
        <span className="truncate">{info.getValue()}</span>
        <OperatorBadge provenance={info.row.original} />
      </span>
    ),
  }),
  column.accessor("body", { header: "Message" }),
]);

/**
 * One CSS grid shared by the header and every row, so the columns line up
 * without a <table>. On a phone the four fixed columns alone are wider than
 * the screen, so a record is two lines there: its time, level and service,
 * then the message across the full width (`LOG_BODY` moves it).
 */
const ROW_GRID =
  "grid grid-cols-[1rem_auto_auto_minmax(0,1fr)] items-baseline gap-x-3 md:grid-cols-[1rem_8.5rem_4rem_9rem_minmax(0,1fr)]";
/** The message cell: its own full-width line on a phone, under the chevron's column. */
const LOG_BODY = "max-md:col-start-2 max-md:col-end-[-1]";

const EMPTY: LogRecord[] = [];

/** The server's grammar (proto: SearchLogsRequest.query), as the reader needs it. */
const QUERY_SYNTAX: [string, string][] = [
  ["timeout", "message contains the word"],
  ['"connection refused"', "message contains the phrase"],
  ["severity:>=warn", "level at least warn"],
  ["service:api*", "service name starts with api"],
  ["attr.http.status:500", "log attribute equals"],
  ["res.k8s.pod.name:web-1", "resource attribute equals"],
  ["attr.user.id:*", "attribute is present"],
  ["-severity:debug", "exclude (also NOT)"],
  ["(a OR b) c", "alternation and grouping; terms are ANDed"],
];

export function LogsView({
  scope,
  subject,
  range,
  query,
  service,
  severities,
  traceId,
  spanId,
  live,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  query: string;
  service: string | undefined;
  severities: Severity[];
  traceId?: string;
  spanId?: string;
  live: boolean;
  navigate: ObsNavigate;
}) {
  const filters: LogFilters = useMemo(
    () => ({ range, query, services: service ? [service] : [], severities, traceId, spanId }),
    [range, query, service, severities, traceId, spanId]
  );
  const logs = useSearchLogs(scope, filters, live);
  const records = useMemo(() => logs.data?.pages.flatMap((page) => page.records) ?? EMPTY, [logs.data]);
  const filtered = !!query || !!service || severities.length > 0 || !!traceId;

  const queryError = logs.error && obsErrorKind(logs.error) === "invalid" ? obsErrorMessage(logs.error) : null;

  return (
    <div className="space-y-3" data-testid="obs-logs">
      <LogsToolbar
        scope={scope}
        range={range}
        query={query}
        queryError={queryError}
        service={service}
        severities={severities}
        live={live}
        navigate={navigate}
      />
      {traceId && (
        <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground" data-testid="obs-logs-trace-scope">
          Showing logs from trace <span className="font-mono text-foreground">{shortId(traceId)}</span>
          {spanId && (
            <>
              , span <span className="font-mono text-foreground">{shortId(spanId)}</span>
            </>
          )}
          <button
            type="button"
            className="rounded-sm text-foreground underline underline-offset-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={() => navigate({ trace: undefined, span: undefined })}
          >
            Show all logs
          </button>
        </p>
      )}

      {logs.isLoading ? (
        <Card padding="none">
          <ObsLoadingRows testId="obs-logs-loading" />
        </Card>
      ) : logs.error && !logs.data && !queryError ? (
        <ObsError error={logs.error} subject={subject} onRetry={() => void logs.refetch()} retrying={logs.isFetching} />
      ) : records.length === 0 && !queryError ? (
        <ObsNoData
          what="logs"
          range={range}
          filtered={filtered}
          onWiden={() => navigate({ range: "7d" })}
          onClearFilters={() =>
            navigate({ q: undefined, service: undefined, severity: undefined, trace: undefined, span: undefined })
          }
        />
      ) : records.length > 0 ? (
        <LogList
          records={records}
          subject={subject}
          hasMore={!!logs.hasNextPage && !live}
          loadingMore={logs.isFetchingNextPage}
          onLoadMore={() => void logs.fetchNextPage()}
          navigate={navigate}
        />
      ) : null}
    </div>
  );
}

function LogsToolbar({
  scope,
  range,
  query,
  queryError,
  service,
  severities,
  live,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  query: string;
  queryError: string | null;
  service: string | undefined;
  severities: Severity[];
  live: boolean;
  navigate: ObsNavigate;
}) {
  const [draft, setDraft] = useState(query);
  // A link or Back changes the query under the box; follow it.
  useEffect(() => setDraft(query), [query]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    navigate({ q: draft.trim() || undefined });
  };

  const toggleSeverity = (severity: Severity) => {
    const next = severities.includes(severity)
      ? severities.filter((s) => s !== severity)
      : SEVERITIES.filter((s) => s === severity || severities.includes(s));
    navigate({ severity: severityParam(next) }, { replace: true });
  };

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-start gap-2">
        <form role="search" onSubmit={submit} className="min-w-[16rem] flex-1">
          <label htmlFor="obs-log-query" className="sr-only">
            Search logs
          </label>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-muted-foreground" aria-hidden="true" />
            <input
              id="obs-log-query"
              type="search"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              placeholder='Search, e.g. timeout  "connection refused"  attr.http.status:500  -severity:debug'
              aria-invalid={queryError ? true : undefined}
              aria-describedby={queryError ? "obs-log-query-error" : undefined}
              className={cn(
                "h-8 w-full rounded-md border bg-background pl-8 pr-3 font-mono text-sm text-foreground placeholder:font-sans placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                queryError ? "border-destructive" : "border-border"
              )}
              data-testid="obs-log-query"
            />
          </div>
          {queryError && (
            <p id="obs-log-query-error" role="alert" className="mt-1 text-xs text-destructive" data-testid="obs-log-query-error">
              {queryError}
            </p>
          )}
          <details className="mt-1 text-xs text-muted-foreground">
            <summary className="cursor-pointer rounded-sm hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              Query syntax
            </summary>
            <dl className="mt-1 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 rounded-md border border-border/60 bg-background px-3 py-2">
              {QUERY_SYNTAX.map(([example, meaning]) => (
                <div key={example} className="contents">
                  <dt className="font-mono text-foreground">{example}</dt>
                  <dd>{meaning}</dd>
                </div>
              ))}
            </dl>
          </details>
        </form>
        <ServiceSelect
          scope={scope}
          range={range}
          value={service}
          onChange={(next) => navigate({ service: next }, { replace: true })}
        />
        <Button
          variant={live ? "primary" : "outline"}
          size="sm"
          aria-pressed={live}
          onClick={() => navigate({ live: live ? undefined : true }, { replace: true })}
          leftIcon={<Radio className={cn("h-3.5 w-3.5", live && "motion-safe:animate-pulse")} />}
          data-testid="obs-live-tail"
        >
          {live ? "Tailing" : "Live tail"}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Filter by level">
        {SEVERITIES.map((severity) => {
          const on = severities.includes(severity);
          return (
            <button
              key={severity}
              type="button"
              aria-pressed={on}
              onClick={() => toggleSeverity(severity)}
              data-testid={`obs-severity-${severity}`}
              className={cn(
                "rounded-full border px-2.5 py-0.5 text-xs capitalize transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                on
                  ? "border-primary bg-primary/10 text-foreground"
                  : "border-border text-muted-foreground hover:text-foreground"
              )}
            >
              {severity}
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** The services the overview knows, as filter options. Shared cache entry with the Overview view. */
export function ServiceSelect({
  scope,
  range,
  value,
  onChange,
  includeAll = true,
  id = "obs-service-filter",
}: {
  scope: ObsScope;
  range: RangePreset;
  value: string | undefined;
  onChange: (service: string | undefined) => void;
  includeAll?: boolean;
  id?: string;
}) {
  const overview = useServiceOverview(scope, range);
  const names = useMemo(() => {
    const known = (overview.data?.services ?? []).map((s) => s.name);
    // A service from a link that the window no longer contains is still selectable.
    if (value && !known.includes(value)) known.unshift(value);
    return known;
  }, [overview.data, value]);

  return (
    <>
      <label htmlFor={id} className="sr-only">
        Service
      </label>
      <select
        id={id}
        value={value ?? ""}
        onChange={(event) => onChange(event.target.value || undefined)}
        className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-[44px]"
        data-testid={id}
      >
        {includeAll && <option value="">All services</option>}
        {!includeAll && !value && <option value="">Choose a service…</option>}
        {names.map((name) => (
          <option key={name} value={name}>
            {name}
          </option>
        ))}
      </select>
    </>
  );
}

function LogList({
  records,
  subject,
  hasMore,
  loadingMore,
  onLoadMore,
  navigate,
}: {
  records: LogRecord[];
  subject: string;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  navigate: ObsNavigate;
}) {
  const table = useTable({ features, columns, data: records, getRowId: (row) => row.id });
  const rows = table.getRowModel().rows;
  const scrollRef = useRef<HTMLDivElement>(null);
  const requestFocus = useRovingFocus(scrollRef, "data-row-index");
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [active, setActive] = useState(0);
  const activeIndex = Math.min(active, rows.length - 1);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 30,
    getItemKey: (index) => rows[index]?.id ?? index,
    overscan: 12,
  });

  const toggle = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const focusRow = (index: number) => {
    const clamped = Math.max(0, Math.min(rows.length - 1, index));
    setActive(clamped);
    virtualizer.scrollToIndex(clamped, { align: "auto" });
    // The row may only exist after the scroll renders it.
    requestFocus(clamped);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
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
      <div
        aria-hidden="true"
        className={cn(ROW_GRID, "border-b border-border px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground max-md:hidden")}
      >
        <span />
        {table.getHeaderGroups()[0]?.headers.map((header) => (
          <span key={header.id}>
            <table.FlexRender header={header} />
          </span>
        ))}
      </div>
      <div
        ref={scrollRef}
        className="max-h-[60vh] min-h-[12rem] overflow-auto"
        onKeyDown={onKeyDown}
        data-testid="obs-log-list"
      >
        <div role="list" aria-label={`Log records from ${subject}, newest first`} className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const row = rows[item.index];
            if (!row) return null;
            const record = row.original;
            const open = expanded.has(record.id);
            const detailsId = `obs-log-details-${item.index}`;
            return (
              <div
                key={item.key}
                role="listitem"
                data-index={item.index}
                ref={virtualizer.measureElement}
                className="absolute left-0 top-0 w-full border-b border-border/50"
                style={{ transform: `translateY(${item.start}px)` }}
              >
                <button
                  type="button"
                  data-row-index={item.index}
                  tabIndex={item.index === activeIndex ? 0 : -1}
                  aria-expanded={open}
                  aria-controls={detailsId}
                  onClick={() => {
                    setActive(item.index);
                    toggle(record.id);
                  }}
                  onFocus={() => setActive(item.index)}
                  className={cn(
                    ROW_GRID,
                    "w-full px-3 py-1 text-left font-mono text-xs hover:bg-muted/50 focus:outline-none focus-visible:bg-muted/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                    "touch:min-h-[44px] max-md:gap-y-0.5 max-md:py-2",
                    open && "bg-muted/40"
                  )}
                  data-testid="obs-log-row"
                >
                  <ChevronRight
                    className={cn("h-3 w-3 self-center text-muted-foreground transition-transform", open && "rotate-90")}
                    aria-hidden="true"
                  />
                  {row.getAllCells().map((cell) => (
                    <span
                      key={cell.id}
                      className={cn(
                        "min-w-0",
                        cell.column.id === "body" ? cn("truncate text-foreground", LOG_BODY) : "truncate text-muted-foreground"
                      )}
                    >
                      <table.FlexRender cell={cell} />
                    </span>
                  ))}
                </button>
                {open && (
                  <div id={detailsId} className="space-y-3 border-t border-border/50 bg-background px-9 py-3 max-md:px-3" data-testid="obs-log-details">
                    <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">{record.body}</pre>
                    {record.traceId && (
                      <div className="flex flex-wrap gap-2">
                        <Button
                          variant="outline"
                          size="xs"
                          onClick={() => navigate({ view: "traces", trace: record.traceId, span: record.spanId || undefined })}
                          leftIcon={<GitBranch className="h-3 w-3" />}
                          data-testid="obs-log-open-trace"
                        >
                          Open trace {shortId(record.traceId)}
                        </Button>
                        <Button
                          variant="ghost"
                          size="xs"
                          onClick={() => navigate({ trace: record.traceId, span: undefined, q: undefined })}
                        >
                          All logs from this trace
                        </Button>
                      </div>
                    )}
                    <AttributeTable title="Attributes" attributes={record.attributes} />
                    <AttributeTable title="Resource" attributes={record.resource} />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
      {hasMore && (
        <div className="border-t border-border px-3 py-2">
          <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} disabled={loadingMore} data-testid="obs-logs-more">
            Load older
          </Button>
        </div>
      )}
    </Card>
  );
}

const SEVERITY_CLASS: Record<Severity, string> = {
  fatal: "text-destructive font-semibold",
  error: "text-destructive",
  warn: "text-warning",
  info: "text-foreground",
  debug: "text-muted-foreground",
  trace: "text-muted-foreground",
  unknown: "text-muted-foreground",
};

export function SeverityLabel({ severity, text }: { severity: Severity; text?: string }) {
  return (
    <span className={cn("uppercase", SEVERITY_CLASS[severity])} title={text && text.toLowerCase() !== severity ? text : undefined}>
      {severity === "unknown" ? text || "—" : severity}
    </span>
  );
}

export function shortId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 8)}…` : id;
}
