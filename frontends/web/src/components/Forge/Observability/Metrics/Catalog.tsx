// Copyright (c) 2025 Reliant Labs

/**
 * The metric catalog: every metric the scope's telemetry carried in the last
 * day (ListMetrics), grouped as Declared (the release's own schema), Runtime
 * (forge's and Go's built-ins), Infra (k8s, container) and Other, each row
 * saying what the metric is (kind, unit, ~series), who sends it and when it was
 * last seen. A declared metric nothing sent, and a metric sent but never
 * declared, are badged.
 *
 * It is one listbox (groups of options) with roving focus: Tab lands on the
 * current metric, the arrow keys, Home and End move, Enter or Space picks.
 */

import { useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { ChevronDown, ChevronRight, Search } from "lucide-react";

import Tag from "@/components/forge-ui/tag";
import { cn } from "@/lib/utils";
import type { MetricCatalogEntry } from "@/services/controlPlane/observability/metricsTypes";

import { formatAgo, formatCount } from "../format";
import { useRovingFocus } from "../useRovingFocus";
import { GROUP_LABELS, groupCatalog, kindLabel } from "./seriesView";

export interface CatalogProps {
  metrics: readonly MetricCatalogEntry[];
  /** The metric whose detail is open (highlighted). */
  selected?: string;
  onPick: (entry: MetricCatalogEntry) => void;
  /** The search box's text, and its change (the parent asks the server with it, debounced). */
  query: string;
  onQuery: (query: string) => void;
  /** The server listed only its first `limit`: a search reaches the rest. */
  truncated?: boolean;
  isFetching?: boolean;
  nowMs?: number;
}

const INDEX_ATTR = "data-catalog-index";

export function Catalog({ metrics, selected, onPick, query, onQuery, truncated, isFetching, nowMs }: CatalogProps) {
  const groups = useMemo(() => groupCatalog(metrics, query), [metrics, query]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const listRef = useRef<HTMLDivElement>(null);
  const focusRow = useRovingFocus(listRef, INDEX_ATTR);
  const searchId = useId();

  // The options a keyboard can reach, in order: collapsed groups hide theirs.
  const visible = useMemo(() => groups.flatMap((g) => (collapsed.has(g.group) ? [] : g.metrics)), [groups, collapsed]);
  const selectedIndex = visible.findIndex((m) => m.name === selected);
  // The row Tab lands on: the one the keyboard last moved to, else the open metric, else the first.
  const [active, setActive] = useState<number | null>(null);
  const current = Math.max(0, Math.min(active ?? (selectedIndex >= 0 ? selectedIndex : 0), visible.length - 1));

  const move = (to: number) => {
    const next = Math.max(0, Math.min(visible.length - 1, to));
    setActive(next);
    focusRow(next);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (visible.length === 0) return;
    const keys: Record<string, () => void> = {
      ArrowDown: () => move(current + 1),
      ArrowUp: () => move(current - 1),
      Home: () => move(0),
      End: () => move(visible.length - 1),
      Enter: () => visible[current] && pick(visible[current]!),
      " ": () => visible[current] && pick(visible[current]!),
    };
    const run = keys[event.key];
    if (!run) return;
    event.preventDefault();
    run();
  };
  const pick = (entry: MetricCatalogEntry) => {
    if (!entry.queryable) return;
    onPick(entry);
  };

  let index = -1;
  return (
    <div className="flex min-h-0 flex-col" data-testid="metrics-catalog">
      <label htmlFor={searchId} className="sr-only">
        Search metrics
      </label>
      <div className="relative">
        <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden="true" />
        <input
          id={searchId}
          type="search"
          value={query}
          onChange={(event) => {
            onQuery(event.target.value);
            setActive(null);
          }}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown" && visible.length) {
              event.preventDefault();
              move(0);
            }
          }}
          placeholder="Search metrics"
          className="h-8 w-full rounded-md border border-border bg-background pl-7 pr-2 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          data-testid="metrics-catalog-search"
          aria-busy={isFetching || undefined}
        />
      </div>
      {groups.length === 0 ? (
        <p className="px-1 py-4 text-xs text-ink-muted" data-testid="metrics-catalog-no-match">
          No metric matches <span className="font-mono text-ink">{query}</span>.
        </p>
      ) : (
        <div
          ref={listRef}
          role="listbox"
          aria-label="Metrics"
          onKeyDown={onKeyDown}
          className="mt-2 max-h-[28rem] min-h-0 overflow-y-auto pr-0.5"
          data-testid="metrics-catalog-list"
        >
          {groups.map((g) => {
            const open = !collapsed.has(g.group);
            const headingId = `${searchId}-${g.group}`;
            return (
              <div key={g.group} role="group" aria-labelledby={headingId} data-group={g.group} className="mb-1">
                <button
                  type="button"
                  id={headingId}
                  aria-expanded={open}
                  onClick={() =>
                    setCollapsed((prev) => {
                      const next = new Set(prev);
                      if (next.has(g.group)) next.delete(g.group);
                      else next.add(g.group);
                      return next;
                    })
                  }
                  className="flex w-full items-center gap-1 rounded px-1 py-1 text-left text-xs font-medium text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                  data-testid={`metrics-catalog-group-${g.group}`}
                >
                  {open ? <ChevronDown className="h-3 w-3" aria-hidden="true" /> : <ChevronRight className="h-3 w-3" aria-hidden="true" />}
                  {GROUP_LABELS[g.group]}
                  <span className="tabular-nums">({g.metrics.length})</span>
                </button>
                {open &&
                  g.metrics.map((entry) => {
                    index += 1;
                    const rowIndex = index;
                    return (
                      <CatalogRow
                        key={`${entry.name}/${entry.kind}`}
                        entry={entry}
                        index={rowIndex}
                        selected={entry.name === selected}
                        focusable={rowIndex === current}
                        onPick={() => {
                          setActive(rowIndex);
                          pick(entry);
                        }}
                        nowMs={nowMs}
                      />
                    );
                  })}
              </div>
            );
          })}
        </div>
      )}
      {truncated && (
        <p className="mt-2 px-1 text-xs text-ink-muted" data-testid="metrics-catalog-truncated">
          Only the first {formatCount(metrics.length)} metrics are listed. Search to reach the rest.
        </p>
      )}
    </div>
  );
}

function CatalogRow({
  entry,
  index,
  selected,
  focusable,
  onPick,
  nowMs,
}: {
  entry: MetricCatalogEntry;
  index: number;
  selected: boolean;
  focusable: boolean;
  onPick: () => void;
  nowMs?: number;
}) {
  const services = entry.services.slice(0, 2).join(", ");
  const more = entry.serviceCount - Math.min(entry.services.length, 2);
  return (
    <div
      role="option"
      aria-selected={selected}
      aria-disabled={!entry.queryable || undefined}
      tabIndex={focusable ? 0 : -1}
      {...{ [INDEX_ATTR]: index }}
      onClick={onPick}
      data-metric={entry.name}
      className={cn(
        "cursor-pointer rounded-md px-2 py-1.5 text-xs outline-none transition-colors focus-visible:ring-2 focus-visible:ring-accent",
        selected ? "bg-ink/8" : "hover:bg-ink/5",
        !entry.queryable && "cursor-not-allowed opacity-60"
      )}
    >
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-ink" title={entry.name}>
          {entry.name}
        </span>
        <span className="shrink-0 text-ink-muted">{kindLabel(entry)}</span>
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-ink-muted">
        {entry.unit && <span className="font-mono">{entry.unit}</span>}
        {entry.seen && <span title="Estimated series in the last day">~{formatCount(entry.seriesEstimate)} series</span>}
        {services && (
          <span className="truncate" title={entry.services.join(", ")}>
            {services}
            {more > 0 ? ` +${more}` : ""}
          </span>
        )}
        {entry.lastSeenMs !== null && <span>{formatAgo(entry.lastSeenMs, nowMs)}</span>}
        {entry.declared && !entry.seen && <Tag title="Declared in the release's metrics schema, but nothing sent it in the last day">no data</Tag>}
        {!entry.declared && entry.seen && entry.group !== "infra" && <Tag title="Sent, but not declared in the release's metrics schema">undeclared</Tag>}
        {entry.stability === "deprecated" && <Tag tone="warning">deprecated</Tag>}
        {!entry.queryable && <Tag title="The explorer can't chart this kind yet">not queryable</Tag>}
      </div>
    </div>
  );
}
