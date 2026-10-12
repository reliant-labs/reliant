// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for the observability area.
 *
 * Keys carry the time PRESET, never resolved timestamps: the window is resolved
 * inside the query function, at fetch time. So a refetch (Refresh, live tail,
 * the overview's minute poll) re-reads "the last hour" as of now, and two
 * components asking for the same preset share one request.
 *
 * Paged queries pin the window their FIRST page resolved and pass it to every
 * later page with the cursor — page two of "the last hour" must be the same
 * hour as page one (the server's page token is only valid with the same
 * request), or rows slip between pages as time moves.
 */

import { useInfiniteQuery, useQueries, useQuery } from "@tanstack/react-query";

import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { obsRetry } from "@/services/controlPlane/observability/errors";
import type {
  AttributeFilter,
  EnvScope,
  ListTracesResult,
  SearchLogsResult,
  Severity,
  TimeWindow,
} from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";
import type { MetricPlanQuery, TraceScope } from "@/components/Forge/Observability/panelPlan";

export const obsKeys = {
  org: () => ["observability", "org"] as const,
  all: (scope: EnvScope | null) => ["observability", scope?.orgId ?? "", scope?.environmentId ?? ""] as const,
  overview: (scope: EnvScope | null, range: RangePreset, services: string[] = []) =>
    [...obsKeys.all(scope), "overview", range, services] as const,
  logs: (scope: EnvScope | null, filters: LogFilters) => [...obsKeys.all(scope), "logs", filters] as const,
  traces: (scope: EnvScope | null, filters: TraceFilters) => [...obsKeys.all(scope), "traces", filters] as const,
  traceRed: (scope: EnvScope | null, range: RangePreset, traceScope: TraceScope, limit: number) =>
    [...obsKeys.all(scope), "trace-red", range, traceScope, limit] as const,
  trace: (scope: EnvScope | null, traceId: string) => [...obsKeys.all(scope), "trace", traceId] as const,
  metric: (scope: EnvScope | null, range: RangePreset, query: MetricPlanQuery, step: number) =>
    [...obsKeys.all(scope), "metric", range, query, step] as const,
  dashboards: (scope: EnvScope | null) => [...obsKeys.all(scope), "dashboards"] as const,
  flamegraph: (scope: EnvScope | null, filters: FlamegraphFilters) => [...obsKeys.all(scope), "flamegraph", filters] as const,
};

interface PageParam {
  cursor: string;
  window: TimeWindow;
}

// ── The caller's org ─────────────────────────────────────────────────────────

/**
 * The org every observability request is scoped by: the caller's current
 * organization, as the billing surfaces resolve it. The server verifies
 * membership; this never guesses one.
 */
export function useObservabilityOrg() {
  const api = useObservabilityApi();
  const org = useQuery({
    queryKey: obsKeys.org(),
    queryFn: () => api.currentOrgId(),
    staleTime: 5 * 60_000,
    retry: obsRetry,
  });
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error, refetch: org.refetch };
}

// ── Overview ─────────────────────────────────────────────────────────────────

export function useServiceOverview(scope: EnvScope | null, range: RangePreset, services: string[] = []) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: obsKeys.overview(scope, range, services),
    queryFn: () => api.getServiceOverview({ ...(scope as EnvScope), window: resolveWindow(range), services }),
    enabled: !!scope,
    staleTime: 30_000,
    refetchInterval: 60_000,
    retry: obsRetry,
  });
}

// ── Logs ─────────────────────────────────────────────────────────────────────

export interface LogFilters {
  range: RangePreset;
  query: string;
  services: string[];
  severities: Severity[];
  traceId?: string;
  spanId?: string;
}

export const LOG_PAGE_SIZE = 200;
/** Live tail re-reads the newest page this often. */
export const LIVE_TAIL_MS = 5_000;

export function useSearchLogs(scope: EnvScope | null, filters: LogFilters, live: boolean) {
  const api = useObservabilityApi();
  return useInfiniteQuery({
    queryKey: obsKeys.logs(scope, filters),
    queryFn: async ({ pageParam }): Promise<SearchLogsResult & { window: TimeWindow }> => {
      const window = pageParam?.window ?? resolveWindow(filters.range);
      const result = await api.searchLogs({
        ...(scope as EnvScope),
        window,
        query: filters.query,
        services: filters.services,
        severities: filters.severities,
        traceId: filters.traceId,
        spanId: filters.spanId,
        limit: LOG_PAGE_SIZE,
        cursor: pageParam?.cursor,
      });
      return { ...result, window };
    },
    initialPageParam: null as PageParam | null,
    getNextPageParam: (last) => (last.nextCursor ? { cursor: last.nextCursor, window: last.window } : undefined),
    enabled: !!scope,
    // While tailing, older pages would be re-fetched on every tick; keep just the newest.
    maxPages: live ? 1 : undefined,
    refetchInterval: live ? LIVE_TAIL_MS : false,
    staleTime: 10_000,
    retry: obsRetry,
  });
}

// ── Traces ───────────────────────────────────────────────────────────────────

export interface TraceFilters {
  range: RangePreset;
  rootSpanName: string;
  services: string[];
  errorsOnly: boolean;
  minDurationMs: number;
}

export const TRACE_PAGE_SIZE = 100;

export function useListTraces(scope: EnvScope | null, filters: TraceFilters) {
  const api = useObservabilityApi();
  return useInfiniteQuery({
    queryKey: obsKeys.traces(scope, filters),
    queryFn: async ({ pageParam }): Promise<ListTracesResult & { window: TimeWindow }> => {
      const window = pageParam?.window ?? resolveWindow(filters.range);
      const result = await api.listTraces({
        ...(scope as EnvScope),
        window,
        rootSpanName: filters.rootSpanName,
        services: filters.services,
        errorsOnly: filters.errorsOnly,
        minDurationMs: filters.minDurationMs,
        limit: TRACE_PAGE_SIZE,
        cursor: pageParam?.cursor,
      });
      return { ...result, window };
    },
    initialPageParam: null as PageParam | null,
    getNextPageParam: (last) => (last.nextCursor ? { cursor: last.nextCursor, window: last.window } : undefined),
    enabled: !!scope,
    staleTime: 15_000,
    retry: obsRetry,
  });
}

/**
 * ListTraces for a dashboard panel: the server's RED summary over every
 * matching trace (`limit` 1 — only `red` is read), or the newest `limit`
 * traces for a per-rpc table grouped in the browser.
 */
export function useTraceRed(scope: EnvScope | null, range: RangePreset, traceScope: TraceScope, limit: number) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: obsKeys.traceRed(scope, range, traceScope, limit),
    queryFn: () =>
      api.listTraces({
        ...(scope as EnvScope),
        window: resolveWindow(range),
        rootSpanName: traceScope.rootSpanName,
        services: traceScope.services,
        errorsOnly: traceScope.errorsOnly,
        minDurationMs: 0,
        spanAttributes: traceScope.spanAttributes as AttributeFilter[],
        limit,
      }),
    enabled: !!scope,
    staleTime: 30_000,
    retry: obsRetry,
  });
}

/**
 * One trace. Spans are immutable once written, so it never goes stale. The
 * lookup searches the whole 7 days traces are kept, not the list's window: a
 * trace linked from a log may be older than the window the reader is in.
 */
export function useTrace(scope: EnvScope | null, traceId: string | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: obsKeys.trace(scope, traceId ?? ""),
    queryFn: () => api.getTrace({ ...(scope as EnvScope), traceId: traceId as string, window: resolveWindow("7d") }),
    enabled: !!scope && !!traceId,
    staleTime: Infinity,
    retry: obsRetry,
  });
}

// ── Metrics and dashboards ───────────────────────────────────────────────────

/** Every metric query of one panel, together: a panel draws its series on one chart. */
export function useMetricQueries(scope: EnvScope | null, range: RangePreset, queries: MetricPlanQuery[], step: number | "window" = 0) {
  const api = useObservabilityApi();
  const stepKey = step === "window" ? -1 : step;
  return useQueries({
    queries: queries.map((query) => ({
      queryKey: obsKeys.metric(scope, range, query, stepKey),
      queryFn: () => {
        const window = resolveWindow(range);
        const stepSeconds = step === "window" ? Math.ceil((window.endMs - window.startMs) / 1000) : step;
        return api.queryMetric({ ...(scope as EnvScope), window, ...query, stepSeconds });
      },
      enabled: !!scope,
      staleTime: 30_000,
      retry: obsRetry,
    })),
  });
}

/** The dashboards for this environment. Synthesized from the last week's services until a release ships its own spec. */
export function useDashboards(scope: EnvScope | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: obsKeys.dashboards(scope),
    queryFn: () => api.getDashboards({ ...(scope as EnvScope), window: resolveWindow("7d") }),
    enabled: !!scope,
    staleTime: 5 * 60_000,
    retry: obsRetry,
  });
}

// ── Profiles ─────────────────────────────────────────────────────────────────

export interface FlamegraphFilters {
  range: RangePreset;
  service: string;
  profileType: string;
  traceId?: string;
}

export function useFlamegraph(scope: EnvScope | null, filters: FlamegraphFilters) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: obsKeys.flamegraph(scope, filters),
    queryFn: () =>
      api.getFlamegraph({
        ...(scope as EnvScope),
        window: resolveWindow(filters.range),
        service: filters.service,
        profileType: filters.profileType,
        traceId: filters.traceId,
      }),
    enabled: !!scope && !!filters.service && !!filters.profileType,
    staleTime: 60_000,
    retry: obsRetry,
  });
}
