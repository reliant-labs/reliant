// Copyright (c) 2025 Reliant Labs

/**
 * The two signal tiles on a project's home (the Overview, and the
 * Observability overview the demo opens on): what the requests took, and who
 * was in the app.
 *
 *   Traces     the latency histogram of the window's traces, OK and failed
 *              stacked per bucket, p50 / p95 / p99 above it. A bucket opens
 *              Traces narrowed to that latency band (the `lat` search key).
 *   Sessions   the five newest browser sessions: who, how long, how many pages
 *              and errors. A row opens that session's replay.
 *
 * ── EACH TILE STANDS ALONE ──────────────────────────────────────────────────
 *
 * Each tile makes its own read (the traces page's first page, the sessions
 * list's) and owns its loading, error and retry, so a slow or failed read
 * takes only its own tile away and never its neighbours. They ask with the
 * pages' own default filters, so a tile and the page it opens share one cache
 * entry.
 *
 * The histogram is the traces page's model (traceScatter/traceScatterModel):
 * the same buckets and percentiles that page draws beside its scatter, so the
 * tile and the page cannot disagree about a trace's latency.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { useListTraces, type TraceFilters } from "@/hooks/observability-queries";
import { useSessions, type SessionFilters } from "@/hooks/observability-session-queries";
import { rumErrorKind } from "@/services/controlPlane/observability/errors";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { formatAgo, formatCount, formatDuration, formatPercent } from "../format";
import { histogram, latencyDomain, percentiles, pointsOf, type HistogramBucket } from "../traceScatter/traceScatterModel";
import { rumScopeOf } from "../Sessions/sessionModel";

/** How many latency buckets the tile draws: wide enough to see a second mode, few enough to click. */
export const TILE_BUCKETS = 16;
/** How many sessions the tile lists. */
export const TILE_SESSIONS = 5;

/** A latency band, in milliseconds, both ends inclusive. */
export interface LatencyBand {
  loMs: number;
  hiMs: number;
}

/** The `lat` search key: "120-480" (ms). Rounded outward so the band holds every trace the bucket counted. */
export function latParam(band: LatencyBand): string {
  return `${Math.max(0, Math.floor(band.loMs))}-${Math.ceil(band.hiMs)}`;
}

/** The band a `lat` search key names, or null when it names none. */
export function parseLatParam(param: string | undefined): LatencyBand | null {
  const match = /^(\d+(?:\.\d+)?)-(\d+(?:\.\d+)?)$/.exec(param ?? "");
  if (!match) return null;
  const loMs = Number(match[1]);
  const hiMs = Number(match[2]);
  return Number.isFinite(loMs) && Number.isFinite(hiMs) && hiMs > loMs ? { loMs, hiMs } : null;
}

/** Where a tile's link or row goes: a real link when the host has hrefs, else a callback. */
type Open<T> = { href: (value: T) => string } | { onOpen: (value: T) => void };

function Opener<T>({ open, value, className, label, children, testId }: { open: Open<T>; value: T; className: string; label?: string; children: ReactNode; testId?: string }) {
  if ("href" in open) {
    return (
      <Link href={open.href(value)} className={cn("no-color", className)} aria-label={label} data-testid={testId}>
        {children}
      </Link>
    );
  }
  return (
    <button type="button" onClick={() => open.onOpen(value)} className={cn("text-left", className)} aria-label={label} data-testid={testId}>
      {children}
    </button>
  );
}

function Tile({ title, meta, all, children, testId, state }: { title: string; meta?: ReactNode; all: ReactNode; children: ReactNode; testId: string; state: string }) {
  return (
    <Card padding="none" className="flex min-w-0 flex-col" data-testid={testId} data-state={state}>
      <div className="flex items-center justify-between gap-3 px-4 pt-3">
        <h2 className="min-w-0 text-sm font-medium text-ink">
          {title}
          {meta && <span className="ml-2 text-xs font-normal text-ink-muted">{meta}</span>}
        </h2>
        {all}
      </div>
      <div className="flex min-h-[9rem] flex-1 flex-col px-4 pb-3 pt-2">{children}</div>
    </Card>
  );
}

const ALL_LINK = "inline-flex h-7 shrink-0 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink";

function AllLink<T>({ open, value, children }: { open: Open<T>; value: T; children: ReactNode }) {
  return (
    <Opener open={open} value={value} className={ALL_LINK}>
      {children}
      <ArrowRight className="h-3 w-3" aria-hidden="true" />
    </Opener>
  );
}

function TileMessage({ children, action, testId }: { children: ReactNode; action?: ReactNode; testId?: string }) {
  return (
    <div className="flex flex-1 flex-col items-start justify-center gap-2 text-sm text-ink-muted" data-testid={testId}>
      <p>{children}</p>
      {action}
    </div>
  );
}

function Retry({ onRetry, retrying }: { onRetry: () => void; retrying: boolean }) {
  return (
    <button
      type="button"
      onClick={onRetry}
      disabled={retrying}
      className="inline-flex h-7 items-center rounded-md border border-border-strong bg-surface px-2.5 text-xs font-medium text-ink hover:bg-ink/5 disabled:opacity-60"
    >
      {retrying ? "Retrying…" : "Try again"}
    </button>
  );
}

function TileSkeleton({ bars }: { bars?: boolean }) {
  return bars ? (
    <div className="flex h-24 items-end gap-1" aria-label="Loading" aria-busy="true">
      {Array.from({ length: TILE_BUCKETS }).map((_, i) => (
        <span key={i} className="flex-1 animate-pulse rounded-sm bg-ink/5 motion-reduce:animate-none" style={{ height: `${20 + ((i * 37) % 70)}%` }} />
      ))}
    </div>
  ) : (
    <div className="space-y-2" aria-label="Loading" aria-busy="true">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="h-5 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
      ))}
    </div>
  );
}

// ── Traces ──────────────────────────────────────────────────────────────────

const NO_KEYS: string[] = [];

/** The traces page's first page with its default filters: one cache entry for the tile and the page. */
export function tileTraceFilters(range: RangePreset): TraceFilters {
  return { range, rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0, groupBy: NO_KEYS };
}

export interface TracesTileProps {
  scope: ObsScope | null;
  range: RangePreset;
  /** Opens Traces narrowed to a latency band. */
  band: Open<LatencyBand>;
  /** Opens Traces unfiltered. */
  all: Open<null>;
}

export function TracesTile({ scope, range, band, all }: TracesTileProps) {
  const filters = useMemo(() => tileTraceFilters(range), [range]);
  const traces = useListTraces(scope, filters);
  const rows = useMemo(() => traces.data?.pages[0]?.traces ?? [], [traces.data]);
  const points = useMemo(() => pointsOf(rows), [rows]);
  const model = useMemo(() => {
    if (points.length === 0) return null;
    const domain = latencyDomain(
      points.map((p) => p.durationMs),
      "log"
    );
    return { buckets: histogram(points, domain, "log", TILE_BUCKETS), pct: percentiles(points), failed: points.filter((p) => p.error).length };
  }, [points]);
  const windowWords = RANGE_LABELS[range].toLowerCase();
  const failed = !!traces.error && !traces.data;
  const state = traces.isLoading ? "loading" : failed ? "error" : model ? "ready" : "empty";

  return (
    <Tile
      title="Traces"
      meta={model ? `${formatCount(points.length)} in the ${windowWords}` : undefined}
      all={<AllLink open={all} value={null}>Open traces</AllLink>}
      testId="overview-traces-tile"
      state={state}
    >
      {traces.isLoading ? (
        <TileSkeleton bars />
      ) : failed ? (
        <TileMessage action={<Retry onRetry={() => void traces.refetch()} retrying={traces.isFetching} />} testId="overview-traces-error">
          The traces could not be read right now.
        </TileMessage>
      ) : !model ? (
        <TileMessage testId="overview-traces-empty">
          No traces in the {windowWords}. Traces appear once a service handles a request; forge&apos;s generated handlers trace every one.
        </TileMessage>
      ) : (
        <LatencyHistogram buckets={model.buckets} pct={model.pct} failed={model.failed} total={points.length} band={band} />
      )}
    </Tile>
  );
}

function LatencyHistogram({
  buckets,
  pct,
  failed,
  total,
  band,
}: {
  buckets: HistogramBucket[];
  pct: ReturnType<typeof percentiles>;
  failed: number;
  total: number;
  band: Open<LatencyBand>;
}) {
  const max = Math.max(1, ...buckets.map((b) => b.ok + b.error));
  // Only the buckets between the first and the last that hold a trace: a log
  // domain is padded to decades, and empty decades are not information.
  const first = buckets.findIndex((b) => b.ok + b.error > 0);
  const last = buckets.length - 1 - [...buckets].reverse().findIndex((b) => b.ok + b.error > 0);
  const shown = buckets.slice(Math.max(0, first), last + 1);
  return (
    <div className="flex flex-1 flex-col gap-2">
      <dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-muted" data-testid="overview-traces-percentiles">
        {pct &&
          (
            [
              ["p50", pct.p50],
              ["p95", pct.p95],
              ["p99", pct.p99],
            ] as const
          ).map(([name, value]) => (
            <div key={name} className="flex items-baseline gap-1">
              <dt>{name}</dt>
              <dd className="font-medium tabular-nums text-ink">{formatDuration(value)}</dd>
            </div>
          ))}
        <div className="flex items-baseline gap-1">
          <dt>failed</dt>
          <dd className={cn("font-medium tabular-nums", failed > 0 ? "text-danger-ink" : "text-ink")}>{formatPercent(failed / total)}</dd>
        </div>
      </dl>
      <ul className="flex h-24 items-stretch gap-px" aria-label="Traces by latency. Choose a bar to see its traces.">
        {shown.map((bucket) => {
          const count = bucket.ok + bucket.error;
          const label = `${formatDuration(bucket.lo)} to ${formatDuration(bucket.hi)}: ${count} ${count === 1 ? "trace" : "traces"}${bucket.error ? `, ${bucket.error} failed` : ""}`;
          return (
            <li key={bucket.lo} className="flex min-w-0 flex-1">
              {count === 0 ? (
                <span className="flex-1" title={label} />
              ) : (
                <Opener
                  open={band}
                  value={{ loMs: bucket.lo, hiMs: bucket.hi }}
                  label={label}
                  className="group flex flex-1 flex-col justify-end rounded-sm hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  testId="overview-traces-bucket"
                >
                  <span className="flex w-full flex-col justify-end" style={{ height: `${(count / max) * 100}%` }} title={label}>
                    {bucket.error > 0 && <span className="block w-full rounded-t-sm bg-danger" style={{ height: `${(bucket.error / count) * 100}%` }} />}
                    {bucket.ok > 0 && <span className={cn("block w-full bg-info/70 group-hover:bg-info", bucket.error === 0 && "rounded-t-sm")} style={{ height: `${(bucket.ok / count) * 100}%` }} />}
                  </span>
                </Opener>
              )}
            </li>
          );
        })}
      </ul>
      <div className="flex justify-between text-2xs tabular-nums text-ink-muted" aria-hidden="true">
        <span>{formatDuration(shown[0]?.lo ?? 0)}</span>
        <span className="inline-flex items-center gap-3">
          <span className="inline-flex items-center gap-1">
            <span className="h-2 w-2 rounded-sm bg-info/70" /> OK
          </span>
          <span className="inline-flex items-center gap-1">
            <span className="h-2 w-2 rounded-sm bg-danger" /> Failed
          </span>
        </span>
        <span>{formatDuration(shown[shown.length - 1]?.hi ?? 0)}</span>
      </div>
    </div>
  );
}

// ── Sessions ────────────────────────────────────────────────────────────────

/** The sessions list's first page with its default filters: one cache entry for the tile and the page. */
export function tileSessionFilters(range: RangePreset): SessionFilters {
  return { range, hasError: false, userId: "", pathContains: "" };
}

export interface SessionsTileProps {
  scope: ObsScope | null;
  range: RangePreset;
  /** Opens one session's replay. */
  session: Open<string>;
  /** Opens Sessions. */
  all: Open<null>;
}

export function SessionsTile({ scope, range, session, all }: SessionsTileProps) {
  const rumScope = useMemo(() => (scope ? rumScopeOf(scope) : null), [scope]);
  const filters = useMemo(() => tileSessionFilters(range), [range]);
  const sessions = useSessions(rumScope, filters);
  const rows = (sessions.data?.sessions ?? []).slice(0, TILE_SESSIONS);
  const failed = !!sessions.error && !sessions.data;
  const notConfigured = failed && rumErrorKind(sessions.error) === "not-configured";
  const windowWords = RANGE_LABELS[range].toLowerCase();
  const state = sessions.isLoading ? "loading" : notConfigured ? "not-configured" : failed ? "error" : rows.length ? "ready" : "empty";

  return (
    <Tile title="Recent sessions" meta="Browser sessions with a replay" all={<AllLink open={all} value={null}>All sessions</AllLink>} testId="overview-sessions-tile" state={state}>
      {sessions.isLoading ? (
        <TileSkeleton />
      ) : notConfigured ? (
        <TileMessage testId="overview-sessions-not-configured">
          This control plane does not serve browser sessions yet. Browser spans are still in Traces, and browser errors in Errors.
        </TileMessage>
      ) : failed ? (
        <TileMessage action={<Retry onRetry={() => void sessions.refetch()} retrying={sessions.isFetching} />} testId="overview-sessions-error">
          The sessions could not be read right now.
        </TileMessage>
      ) : rows.length === 0 ? (
        <TileMessage testId="overview-sessions-empty">
          No browser sessions in the {windowWords}. Sessions appear once your frontend is deployed and people visit it: forge&apos;s web runtime records
          them.
        </TileMessage>
      ) : (
        <ul className="-mx-2 divide-y divide-border/70" aria-label="Recent browser sessions">
          {rows.map((row) => (
            <li key={row.sessionId}>
              <SessionLine row={row} open={session} />
            </li>
          ))}
        </ul>
      )}
    </Tile>
  );
}

function SessionLine({ row, open }: { row: SessionSummary; open: Open<string> }) {
  const duration = Math.max(0, row.endedMs - row.startedMs);
  const who = row.userId || "Anonymous";
  return (
    <Opener
      open={open}
      value={row.sessionId}
      className="grid w-full grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-3 rounded-md px-2 py-1.5 text-sm hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      label={`Replay ${who}'s session on ${row.entryPath || "/"}, ${formatDuration(duration)}, ${row.errors} ${row.errors === 1 ? "error" : "errors"}`}
      testId={`overview-session-${row.sessionId}`}
    >
      <span className="min-w-0 truncate">
        <span className={cn("font-medium", row.userId ? "font-mono text-ink" : "text-ink-muted")}>{who}</span>
        <span className="ml-2 text-xs text-ink-muted">{row.entryPath || "/"}</span>
      </span>
      <span className="text-xs tabular-nums text-ink-muted">
        {formatDuration(duration)} · {row.pageViews} {row.pageViews === 1 ? "page" : "pages"}
      </span>
      <span className={cn("w-16 text-right text-xs tabular-nums", row.errors > 0 ? "text-danger-ink" : "text-ink-muted")}>
        {row.errors > 0 ? `${row.errors} ${row.errors === 1 ? "error" : "errors"}` : formatAgo(row.startedMs)}
      </span>
    </Opener>
  );
}
