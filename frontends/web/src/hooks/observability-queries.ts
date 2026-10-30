import { useEffect, useMemo } from "react";
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

import { Code, ConnectError } from "@connectrpc/connect";
import { hashKey, useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient, type QueryKey } from "@tanstack/react-query";

import { useObservabilityApi, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { useObsQuota } from "@/services/controlPlane/observability/ObsQuotaContext";
import type { ActivityList } from "@/services/controlPlane/observability/activityApi";
import type { DeliveryScope } from "@/services/controlPlane/observability/deliveryTypes";
import { alertingRetry, issuesRetry, obsRetry, obsRetryDelay, releaseHealthRetry } from "@/services/controlPlane/observability/errors";
import type {
  AddIssueCommentRequest,
  AssignIssueRequest,
  DeleteIssueCommentRequest,
  Issue,
  IssueComment,
  IssueEventList,
  IssueList,
  IssuePlatform,
  IssueScope,
  IssueSort,
  IssueStatus,
  IssueTimelineList,
  IssueUser,
  LinkIssueChatRequest,
  LinkIssueChatResult,
  UpdateIssueCommentRequest,
  UpdateIssueStatusRequest,
} from "@/services/controlPlane/observability/issuesTypes";
import type { GetMetricRequest, ListMetricExemplarsRequest, MetricKind } from "@/services/controlPlane/observability/metricsTypes";
import type {
  ObsScope,
  ListTracesResult,
  Panel,
  SearchLogsResult,
  Severity,
  TimeWindow,
} from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

// Presentation never changes QueryPanel's computation: two dashboard tiles
// with different ids/titles/layout but the same query share one in-flight read.
function panelRequestKey(panel: Panel) {
  const { id: _id, title: _title, description: _description, width: _width, height: _height, ...request } = panel;
  return request;
}

/**
 * Every key starts with the whole scope — org, project, environments and
 * attribute filters — so Refresh (invalidate `all(scope)`) re-reads exactly
 * what is on screen, and two scopes never share an answer.
 */
export const obsKeys = {
  org: () => ["observability", "org"] as const,
  all: (scope: ObsScope | null) =>
    ["observability", scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? [], scope?.filters ?? []] as const,
  overview: (scope: ObsScope | null, range: RangePreset, services: string[] = [], groupBy: string[] = []) =>
    [...obsKeys.all(scope), "overview", range, services, groupBy] as const,
  logs: (scope: ObsScope | null, filters: LogFilters) => [...obsKeys.all(scope), "logs", filters] as const,
  traces: (scope: ObsScope | null, filters: TraceFilters) => [...obsKeys.all(scope), "traces", filters] as const,
  trace: (scope: ObsScope | null, traceId: string, range: RangePreset = "7d") => [...obsKeys.all(scope), "trace", traceId, range] as const,
  panel: (scope: ObsScope | null, range: RangePreset, panel: Panel, services: string[], groupBy: string[], offset = "") =>
    [...obsKeys.all(scope), "panel", range, panelRequestKey(panel), services, groupBy, offset] as const,
  metrics: (scope: ObsScope | null) => [...obsKeys.all(scope), "metrics"] as const,
  flamegraph: (scope: ObsScope | null, filters: FlamegraphFilters) => [...obsKeys.all(scope), "flamegraph", filters] as const,
  serviceSignals: (scope: ObsScope | null, range: RangePreset) => [...obsKeys.all(scope), "service-signals", range] as const,
  attributeValues: (scope: ObsScope | null, range: RangePreset, key: string, prefix: string, signal: string) =>
    [...obsKeys.all(scope), "attribute-values", range, key, prefix, signal] as const,
  workloads: (scope: ObsScope | null, range: RangePreset, services: string[]) =>
    [...obsKeys.all(scope), "workloads", range, services] as const,
  workload: (scope: ObsScope | null, range: RangePreset, workload: string, container: string) =>
    [...obsKeys.all(scope), "workload", range, workload, container] as const,
  usage: (orgId: string | null) => ["observability", orgId ?? "", "usage"] as const,
};

// ── Quota-aware reads and polls ──────────────────────────────────────────────
//
// Every read that spends the org's ClickHouse query quota (logs, traces,
// metrics, profiles, workloads, issues, release health) goes through
// `useReadApi`, and every poll of one through `usePolled`. Alerts, notifications,
// usage, delivery and activity read Postgres and are left alone: a quota pause
// must not stop them.

/**
 * The observability API with every call guarded by the org's quota state: while
 * the quota is gone a call answers with the quota error at once, without a
 * request. Hooks that make a ClickHouse read use this instead of
 * useObservabilityApi.
 */
export function useObsReadApi(): ObservabilityApi {
  const api = useObservabilityApi();
  const { store } = useObsQuota();
  return useMemo(
    () =>
      new Proxy(api, {
        get(target, prop, receiver) {
          const member = Reflect.get(target, prop, receiver) as unknown;
          if (typeof member !== "function") return member;
          return (...args: unknown[]) => store.guard(() => (member as (...a: unknown[]) => Promise<unknown>).apply(target, args));
        },
      }),
    [api, store]
  );
}

/**
 * Polls `queryKey` every `intervalMs` through the console's ONE poll scheduler
 * (pollRegistry.ts): a single timer per key however many panels watch it,
 * stopped while the quota is gone, a quarter as fast while it is tight, idle in
 * a hidden tab, and each poll marked `Forge-Poll` for the server. `false` or a
 * disabled query does not poll.
 */
function usePolled(queryKey: QueryKey, intervalMs: number | false, enabled = true) {
  const client = useQueryClient();
  const { polls } = useObsQuota();
  const keyHash = hashKey(queryKey);
  useEffect(() => {
    if (!enabled || intervalMs === false) return;
    return polls.subscribe(client, queryKey, intervalMs);
    // The key is compared by hash: a new array with the same content is the same key.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, polls, keyHash, intervalMs, enabled]);
}

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
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error, refetch: org.refetch };
}

// ── Overview ─────────────────────────────────────────────────────────────────

export function useServiceOverview(scope: ObsScope | null, range: RangePreset, services: string[] = [], groupBy: string[] = []) {
  const api = useObsReadApi();
  const queryKey = obsKeys.overview(scope, range, services, groupBy);
  const query = useQuery({
    queryKey,
    queryFn: () => api.getServiceOverview({ ...(scope as ObsScope), window: resolveWindow(range), services, groupBy }),
    enabled: !!scope,
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
  usePolled(queryKey, 60_000, !!scope);
  return query;
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

export function useSearchLogs(scope: ObsScope | null, filters: LogFilters, live: boolean) {
  const api = useObsReadApi();
  const queryKey = obsKeys.logs(scope, filters);
  const query = useInfiniteQuery({
    queryKey,
    queryFn: async ({ pageParam }): Promise<SearchLogsResult & { window: TimeWindow }> => {
      const window = pageParam?.window ?? resolveWindow(filters.range);
      const result = await api.searchLogs({
        ...(scope as ObsScope),
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
    staleTime: 10_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
  usePolled(queryKey, live ? LIVE_TAIL_MS : false, !!scope);
  return query;
}

// ── Traces ───────────────────────────────────────────────────────────────────

export interface TraceFilters {
  range: RangePreset;
  rootSpanName: string;
  services: string[];
  errorsOnly: boolean;
  minDurationMs: number;
  groupBy?: string[];
}

export const TRACE_PAGE_SIZE = 100;

export function useListTraces(scope: ObsScope | null, filters: TraceFilters) {
  const api = useObsReadApi();
  return useInfiniteQuery({
    queryKey: obsKeys.traces(scope, filters),
    queryFn: async ({ pageParam }): Promise<ListTracesResult & { window: TimeWindow }> => {
      const window = pageParam?.window ?? resolveWindow(filters.range);
      const result = await api.listTraces({
        ...(scope as ObsScope),
        window,
        rootSpanName: filters.rootSpanName,
        services: filters.services,
        errorsOnly: filters.errorsOnly,
        minDurationMs: filters.minDurationMs,
        groupBy: filters.groupBy,
        limit: TRACE_PAGE_SIZE,
        cursor: pageParam?.cursor,
      });
      return { ...result, window };
    },
    initialPageParam: null as PageParam | null,
    getNextPageParam: (last) => (last.nextCursor ? { cursor: last.nextCursor, window: last.window } : undefined),
    enabled: !!scope,
    staleTime: 15_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

/**
 * One trace and its incoming causal references. Trace detail uses the selected
 * range for related-evidence admission; callers without a selected range keep
 * the historical seven-day lookup. Late spans/links can arrive after the first
 * fetch, so revisit/focus/Refresh must be able to fetch again. No polling.
 */
export function useTrace(scope: ObsScope | null, traceId: string | null, range: RangePreset = "7d") {
  const api = useObsReadApi();
  return useQuery({
    queryKey: obsKeys.trace(scope, traceId ?? "", range),
    queryFn: () => api.getTrace({ ...(scope as ObsScope), traceId: traceId as string, window: resolveWindow(range) }),
    enabled: !!scope && !!traceId,
    staleTime: 15_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Metrics and dashboards ───────────────────────────────────────────────────

/**
 * How a panel query is run beyond its scope and window.
 *
 * `offset` shifts the window back in time — the comparison of a dashboard's
 * "previous period" (the preset's own length) or "previous release" (the time
 * since the scoped environment's current release was promoted). It is a
 * function so it is resolved at fetch time, like the window, and keyed by
 * `offsetKey` so a preset stays one cache entry rather than one per
 * millisecond.
 */
export interface PanelQueryOptions {
  offsetKey?: string;
  offsetMs?: () => number;
  /** Auto-refresh: re-read the panel this often (ms). */
  refetchInterval?: number | false;
  enabled?: boolean;
}

/**
 * One dashboard panel, computed server-side (QueryPanel). The dashboard's
 * service filter rides as `services`; search panels are never run here.
 *
 * Like every observability read it waits its turn in the page's read scheduler
 * (obsClient), and a refusal for the server's concurrent-read cap is retried
 * with backoff for as long as the panel is mounted: the result stays loading
 * with `failureCount > 0` and the cap error as `failureReason`, which the views
 * draw as "Waiting for capacity…", never as an error. Leaving the page or
 * changing the range cancels a queued or running read.
 */
export function usePanelQuery(
  scope: ObsScope | null,
  range: RangePreset,
  panel: Panel,
  services: string[],
  groupBy: string[] = [],
  options: PanelQueryOptions = {}
) {
  const api = useObsReadApi();
  const queryKey = obsKeys.panel(scope, range, panel, services, groupBy, options.offsetKey ?? "");
  const enabled = !!scope && panel.kind !== "search" && (options.enabled ?? true);
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => {
      const offset = options.offsetMs?.() ?? 0;
      return api.queryPanel({ ...(scope as ObsScope), window: resolveWindow(range, Date.now() - offset), panel, services, stepSeconds: 0, groupBy }, { signal });
    },
    enabled,
    staleTime: 30_000,
    retry: obsRetry,
    retryDelay: obsRetryDelay,
  });
  usePolled(queryKey, options.refetchInterval ?? false, enabled);
  return query;
}

// ── Profiles ─────────────────────────────────────────────────────────────────

export interface FlamegraphFilters {
  range: RangePreset;
  service: string;
  profileType: string;
  traceId?: string;
}

export function useFlamegraph(scope: ObsScope | null, filters: FlamegraphFilters) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: obsKeys.flamegraph(scope, filters),
    queryFn: () =>
      api.getFlamegraph({
        ...(scope as ObsScope),
        window: resolveWindow(filters.range),
        service: filters.service,
        profileType: filters.profileType,
        traceId: filters.traceId,
      }),
    enabled: !!scope && !!filters.service && !!filters.profileType,
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Service signals ──────────────────────────────────────────────────────────

/**
 * Which signals each service in the scope sends and declares, over `range`
 * (pass serviceSignalsRange of the view's window). One request per scope and
 * range, shared by every picker, tab and link that asks, and keyed without
 * the attribute filters: the server ignores them, so a filter change must not
 * re-ask. Never retried on Unimplemented (a control plane that predates the
 * RPC): the caller treats an error as "unknown" and offers every view.
 */
export function useServiceSignals(scope: ObsScope | null, range: RangePreset) {
  const api = useObsReadApi();
  const unfiltered = scope && scope.filters.length ? { ...scope, filters: [] } : scope;
  return useQuery({
    queryKey: obsKeys.serviceSignals(unfiltered, range),
    queryFn: () => api.getServiceSignals({ ...(unfiltered as ObsScope), window: resolveWindow(range) }),
    enabled: !!unfiltered,
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Usage and billing ────────────────────────────────────────────────────────

/**
 * The org's month-to-date usage against its allowance. Keyed by org alone (the
 * allowance is per org, not per environment), and served from Postgres, so it
 * works when the tenant store is unreachable. It lags real time by about an
 * hour; polling faster would only re-read the same hour.
 */
export function useObservabilityUsage(orgId: string | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: obsKeys.usage(orgId),
    queryFn: () => api.getObservabilityUsage(orgId as string),
    enabled: !!orgId,
    staleTime: 5 * 60_000,
    refetchInterval: 15 * 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Filter autocomplete ──────────────────────────────────────────────────────

/**
 * The values `key` takes in the scope. The server caches each answer a minute,
 * so the client keeps it as long and asks again only when the prefix moves.
 */
export function useAttributeValues(
  scope: ObsScope | null,
  range: RangePreset,
  key: string,
  prefix: string,
  signal?: "traces" | "logs" | "metrics"
) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: obsKeys.attributeValues(scope, range, key, prefix, signal ?? ""),
    queryFn: () =>
      // Value lookups are capped at 7 days server-side, which is the widest preset.
      api.listAttributeValues({ ...(scope as ObsScope), window: resolveWindow(range), key, prefix, signal }),
    enabled: !!scope && !!key,
    staleTime: 60_000,
    placeholderData: (previous) => previous,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Metrics explorer ─────────────────────────────────────────────────────────

/**
 * The metrics explorer's keys. Every one starts with `metricKeys.all(scope)`, which
 * itself starts with the whole scope, so Refresh (invalidate `obsKeys.all(scope)`)
 * re-reads them too, and one scope never shares an answer with another.
 */
export const metricKeys = {
  all: (scope: ObsScope | null) => obsKeys.metrics(scope),
  catalog: (scope: ObsScope | null, range: RangePreset, name: string, limit: number) =>
    [...metricKeys.all(scope), "catalog", range, name, limit] as const,
  detail: (scope: ObsScope | null, range: RangePreset, name: string, kind: MetricKind | "") =>
    [...metricKeys.all(scope), "detail", range, name, kind] as const,
  exemplars: (scope: ObsScope | null, range: RangePreset, request: ExemplarsQuery) =>
    [...metricKeys.all(scope), "exemplars", range, request] as const,
};

/** The catalog's window: the server caps it at 24 hours, so a longer preset would only be clamped there. */
const CATALOG_MAX_RANGE: RangePreset = "24h";

function catalogRange(range: RangePreset): RangePreset {
  return range === "7d" ? CATALOG_MAX_RANGE : range;
}

/**
 * The metric catalog: every metric the scope's telemetry carries with its kind,
 * unit, series estimate and services. The window is the explorer's, capped at 24
 * hours (a metric last seen three days ago is still reachable by name). The
 * server caches the answer a minute; the client keeps it as long.
 */
export function useMetricCatalog(scope: ObsScope | null, range: RangePreset, name = "", limit = 0) {
  const api = useObsReadApi();
  const window = catalogRange(range);
  return useQuery({
    queryKey: metricKeys.catalog(scope, window, name, limit),
    queryFn: () => api.listMetricCatalog({ ...(scope as ObsScope), window: resolveWindow(window), name, limit }),
    enabled: !!scope,
    staleTime: 60_000,
    placeholderData: (previous) => previous,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

/** One metric's detail: attributes with cardinality, export interval and suggested breakdowns. */
export function useMetric(scope: ObsScope | null, range: RangePreset, name: string | undefined, kind?: GetMetricRequest["kind"]) {
  const api = useObsReadApi();
  const window = catalogRange(range);
  return useQuery({
    queryKey: metricKeys.detail(scope, window, name ?? "", kind ?? ""),
    queryFn: () => api.getMetric({ ...(scope as ObsScope), window: resolveWindow(window), name: name as string, kind }),
    enabled: !!scope && !!name,
    staleTime: 60_000,
    // NotFound is the answer "no data for this metric in this window": a state to show, not a fault to retry.
    retry: (failureCount, error) => !(error instanceof ConnectError && error.code === Code.NotFound) && obsRetry(failureCount, error),
    retryDelay: obsRetryDelay,
  });
}

/** What an exemplar read is keyed by: the metric, its filters and the bucket asked about (epoch ms, so two clicks on one bucket share an answer). */
export type ExemplarsQuery = ListMetricExemplarsRequest;

/** Exemplars of one metric (a sampled observation and its trace), stored traces first. Maximum range 7 days. */
export function useMetricExemplars(scope: ObsScope | null, range: RangePreset, request: ExemplarsQuery | null, options: { enabled?: boolean } = {}) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: metricKeys.exemplars(scope, range, request ?? ({ name: "" } as ExemplarsQuery)),
    queryFn: () => api.listMetricExemplars({ ...(scope as ObsScope), window: resolveWindow(range), ...(request as ExemplarsQuery) }),
    enabled: !!scope && !!request?.name && (options.enabled ?? true),
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Workloads (customer metadata) ────────────────────────────────────────────

export function useWorkloads(scope: ObsScope | null, range: RangePreset, services: string[] = []) {
  const api = useObsReadApi();
  const queryKey = obsKeys.workloads(scope, range, services);
  const query = useQuery({
    queryKey,
    queryFn: () => api.listWorkloads({ ...(scope as ObsScope), window: resolveWindow(range), services }),
    enabled: !!scope,
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
  usePolled(queryKey, 60_000, !!scope);
  return query;
}

export function useWorkload(scope: ObsScope | null, range: RangePreset, workload: string | undefined, container?: string) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: obsKeys.workload(scope, range, workload ?? "", container ?? ""),
    queryFn: () =>
      api.getWorkload({ ...(scope as ObsScope), window: resolveWindow(range), workload: workload as string, container }),
    enabled: !!scope && !!workload,
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

// ── Alerting (AlertingService) ───────────────────────────────────────────────
//
// Org-scoped, not env-scoped: the server takes one optional env id on the
// alert reads, so an env page passes its own and the project area filters the
// org's list to the environments it has selected. Firing alerts and the
// notification count poll; receivers and silences change only by hand.

export const alertKeys = {
  all: (orgId: string | null) => ["observability", "alerting", orgId ?? ""] as const,
  alerts: (orgId: string | null, envId: string) => [...alertKeys.all(orgId), "alerts", envId] as const,
  history: (orgId: string | null, envId: string, range: RangePreset) => [...alertKeys.all(orgId), "history", envId, range] as const,
  silences: (orgId: string | null, includeExpired: boolean) => [...alertKeys.all(orgId), "silences", includeExpired] as const,
  receivers: (orgId: string | null) => [...alertKeys.all(orgId), "receivers"] as const,
  notifications: (orgId: string | null) => [...alertKeys.all(orgId), "notifications"] as const,
};

/** Pending and firing alerts. One env id narrows server-side; undefined is every env of the org. */
export function useAlerts(orgId: string | null, envId?: string) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: alertKeys.alerts(orgId, envId ?? ""),
    queryFn: () => api.listAlerts(orgId as string, envId),
    enabled: !!orgId,
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: alertingRetry,
  });
}

export function useAlertHistory(orgId: string | null, range: RangePreset, envId?: string) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: alertKeys.history(orgId, envId ?? "", range),
    queryFn: () => api.listAlertHistory(orgId as string, { envId, sinceMs: resolveWindow(range).startMs, limit: 500 }),
    enabled: !!orgId,
    staleTime: 30_000,
    retry: alertingRetry,
  });
}

export function useSilences(orgId: string | null, includeExpired: boolean) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: alertKeys.silences(orgId, includeExpired),
    queryFn: () => api.listSilences(orgId as string, includeExpired),
    enabled: !!orgId,
    staleTime: 30_000,
    retry: alertingRetry,
  });
}

export function useReceivers(orgId: string | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: alertKeys.receivers(orgId),
    queryFn: () => api.listReceivers(orgId as string),
    enabled: !!orgId,
    staleTime: 60_000,
    retry: alertingRetry,
  });
}

/**
 * The org's in-app notifications and unread count — the indicator in the
 * Observability header and the forge nav. Polls once a minute; Alertmanager
 * groups and repeats on its own schedule, so faster would show nothing new.
 */
export function useNotifications(orgId: string | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: alertKeys.notifications(orgId),
    queryFn: () => api.listNotifications(orgId as string, { limit: 50 }),
    enabled: !!orgId,
    staleTime: 30_000,
    // Stop polling an org whose alerting answered "not configured" or "denied".
    refetchInterval: (query) => (query.state.error ? false : 60_000),
    retry: alertingRetry,
  });
}

/**
 * Every alerting write: it runs, then re-reads everything alerting shows for
 * the org (a silence changes what fires; a receiver changes what routes).
 */
export function useAlertingMutation<TArgs, TResult>(orgId: string | null, run: (api: ObservabilityApi, orgId: string, args: TArgs) => Promise<TResult>) {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: TArgs) => run(api, orgId as string, args),
    onSettled: () => queryClient.invalidateQueries({ queryKey: alertKeys.all(orgId) }),
  });
}

// ── Delivery (DeliveryInsightsService) ───────────────────────────────────────

/**
 * The release ledger and DORA, keyed by the delivery scope (org, project,
 * environments) — not the telemetry scope: attribute filters mean nothing to
 * the ledger beyond the environments they name (deliveryScope.ts).
 */
export const deliveryKeys = {
  root: () => ["observability", "delivery"] as const,
  all: (scope: DeliveryScope | null) => [...deliveryKeys.root(), scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? []] as const,
};

/**
 * Promotions over a window resolved when the query runs (`window()`), cached
 * under `windowKey` ("range:1h", "days:30", "around:<promotion>"…) so a preset
 * stays one entry rather than one per millisecond.
 */
export function useReleases(
  scope: DeliveryScope | null,
  windowKey: string,
  window: () => TimeWindow,
  options: { enabled?: boolean; limit?: number } = {}
) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: [...deliveryKeys.all(scope), "releases", windowKey, options.limit ?? 0] as const,
    queryFn: () => api.listReleases({ ...(scope as DeliveryScope), window: window(), limit: options.limit }),
    enabled: !!scope && (options.enabled ?? true),
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

/** DORA over the last `days`, failing a deployment rolled back within `failureHours`. */
export function useDoraMetrics(scope: DeliveryScope | null, days: number, failureHours: number) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: [...deliveryKeys.all(scope), "dora", days, failureHours] as const,
    queryFn: () => {
      const end = Date.now();
      return api.getDoraMetrics({ ...(scope as DeliveryScope), window: { startMs: end - days * 86_400_000, endMs: end }, failureWindowHours: failureHours });
    },
    enabled: !!scope,
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

/**
 * The server's verdict on one promotion (GetReleaseHealth). Under the delivery
 * keys, so the panel's Refresh re-reads it with the ledger. The verdict of a
 * promotion whose after window is still open changes as traffic arrives, so
 * it is re-read every minute until the server says the window is complete.
 */
export function useReleaseHealth(scope: DeliveryScope | null, promotionId: string, windowMs: number) {
  const api = useObsReadApi();
  const queryKey = [...deliveryKeys.all(scope), "health", promotionId, windowMs] as const;
  const enabled = !!scope && !!promotionId;
  const query = useQuery({
    queryKey,
    queryFn: () => api.getReleaseHealth({ ...(scope as DeliveryScope), promotionId, windowMs }),
    enabled,
    staleTime: 30_000,
    retry: releaseHealthRetry, retryDelay: obsRetryDelay,
  });
  usePolled(queryKey, query.data && !query.data.afterComplete ? 60_000 : false, enabled);
  return query;
}

// ── Issues (IssuesService) ────────────────────────────────────────────────────

export interface IssueFilters {
  range: RangePreset;
  /** Empty: the open ones (the server's default). */
  statuses: IssueStatus[];
  platform?: IssuePlatform;
  service?: string;
  /** Only the issues assigned to this user id (the server filters). */
  assignee?: string;
  sort: IssueSort;
  text: string;
}

export const ISSUE_PAGE_SIZE = 50;

/**
 * One root for every issue read, so a triage (which can move an issue between
 * lists, change its detail and add to its history) re-reads all of them, and
 * the panel's Refresh does too.
 */
export const issueKeys = {
  root: () => ["observability", "issues"] as const,
  list: (scope: IssueScope | null, filters: IssueFilters) =>
    [...issueKeys.root(), "list", scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? [], filters] as const,
  detail: (issueId: string, range: RangePreset) => [...issueKeys.root(), "detail", issueId, range] as const,
  events: (issueId: string) => [...issueKeys.root(), "events", issueId] as const,
  timelines: () => [...issueKeys.root(), "timeline"] as const,
  timeline: (issueId: string) => [...issueKeys.timelines(), issueId] as const,
};

const issueRevocations = new WeakMap<QueryClient, Map<string, { generation: number; error: unknown }>>();

/** A membership refusal revokes previously cached sensitive reads of this issue too. */
async function readIssue<T>(client: QueryClient, issueId: string, key: QueryKey, read: () => Promise<T>): Promise<T> {
  let revocations = issueRevocations.get(client);
  if (!revocations) { revocations = new Map(); issueRevocations.set(client, revocations); }
  const generation = revocations.get(issueId)?.generation ?? 0;
  let result: T;
  try {
    result = await read();
  } catch (error) {
    if (error instanceof ConnectError && (error.code === Code.PermissionDenied || error.code === Code.Unauthenticated)) {
      revocations.set(issueId, { generation: (revocations.get(issueId)?.generation ?? 0) + 1, error });
      const queries = client.getQueryCache().findAll({ predicate: (query) => {
        const k = query.queryKey;
        return k[0] === "observability" && k[1] === "issues" && ["detail", "events", "timeline"].includes(String(k[2])) && k[3] === issueId;
      } });
      // Cancel siblings before scrubbing them: an older in-flight response must not restore revoked data.
      await client.cancelQueries({ predicate: (query) => queries.includes(query) && hashKey(query.queryKey) !== hashKey(key) });
      for (const query of queries) query.setState({ data: undefined, dataUpdatedAt: 0, error, status: "error" });
    }
    throw error;
  }
  const revoked = revocations.get(issueId);
  // Some transports ignore cancellation. Never accept a response started before a refusal.
  if (revoked && revoked.generation !== generation) throw revoked.error;
  return result;
}

/** People change rarely; the roster and the reader are read once a while, not per issue. */
export const peopleKeys = {
  members: (orgId: string) => ["observability", "people", "members", orgId] as const,
  me: () => ["observability", "people", "me"] as const,
};

/** The scope's issues, a page at a time; later pages reuse the first page's window (the token is only valid with the same request). */
export function useIssues(scope: IssueScope | null, filters: IssueFilters) {
  const api = useObsReadApi();
  return useInfiniteQuery({
    queryKey: issueKeys.list(scope, filters),
    queryFn: async ({ pageParam }): Promise<IssueList & { window: TimeWindow }> => {
      const window = pageParam?.window ?? resolveWindow(filters.range);
      const result = await api.listIssues({
        ...(scope as IssueScope),
        services: filters.service ? [filters.service] : [],
        window,
        statuses: filters.statuses,
        platform: filters.platform,
        assignee: filters.assignee,
        sort: filters.sort,
        text: filters.text,
        pageSize: ISSUE_PAGE_SIZE,
        pageToken: pageParam?.cursor,
      });
      return { ...result, window };
    },
    initialPageParam: null as PageParam | null,
    getNextPageParam: (last) => (last.nextPageToken ? { cursor: last.nextPageToken, window: last.window } : undefined),
    enabled: !!scope,
    staleTime: 30_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
  });
}

/** One issue with its latest event, frames, versions and first release; counts over `range`. */
export function useIssue(issueId: string | undefined, range: RangePreset) {
  const api = useObsReadApi();
  const client = useQueryClient();
  const key = issueKeys.detail(issueId ?? "", range);
  return useQuery({
    queryKey: key,
    queryFn: () => readIssue(client, issueId as string, key, () => api.getIssue({ issueId: issueId as string, window: resolveWindow(range) })),
    enabled: !!issueId,
    staleTime: 30_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
  });
}

export const ISSUE_EVENTS_PAGE_SIZE = 20;

/** One issue's events, newest first. */
export function useIssueEvents(issueId: string | undefined) {
  const api = useObsReadApi();
  const client = useQueryClient();
  const key = issueKeys.events(issueId ?? "");
  return useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }): Promise<IssueEventList> =>
      readIssue(client, issueId as string, key, () => api.listIssueEvents({ issueId: issueId as string, pageSize: ISSUE_EVENTS_PAGE_SIZE, pageToken: pageParam ?? undefined })),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!issueId,
    staleTime: 30_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
  });
}

/**
 * A triage. It never retries and never re-sends: a stale version comes back
 * as an error (conflict) for the view to show, and every issue read is
 * re-fetched either way — on success to show the new state everywhere, on a
 * conflict to show the change the reader had not seen.
 */
export function useUpdateIssueStatus() {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation<Issue, unknown, UpdateIssueStatusRequest>({
    mutationFn: (req) => api.updateIssueStatus(req),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: issueKeys.root() });
      void queryClient.invalidateQueries({ queryKey: activityKeys.root() });
    },
  });
}

export const ISSUE_TIMELINE_PAGE_SIZE = 100;

/** An issue's timeline, oldest first, a page at a time. A merged issue answers with the timeline of the issue it was merged into. */
export function useIssueTimeline(issueId: string | undefined) {
  // Discussion reads Postgres, not ClickHouse: quota pauses must not block it.
  const api = useObservabilityApi();
  const client = useQueryClient();
  const key = issueKeys.timeline(issueId ?? "");
  return useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }): Promise<IssueTimelineList> =>
      readIssue(client, issueId as string, key, () => api.listIssueTimeline({ issueId: issueId as string, pageSize: ISSUE_TIMELINE_PAGE_SIZE, pageToken: pageParam ?? undefined })),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!issueId,
    staleTime: 15_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
  });
}

/** The signed-in person: who "me" is, and whose comments are editable. */
export function useCurrentIssueUser() {
  const api = useObservabilityApi();
  return useQuery<IssueUser | null>({
    queryKey: peopleKeys.me(),
    queryFn: () => api.getCurrentIssueUser(),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/** The explicit issue org's roster and caller role, independently cached across org switches. */
export function useOrgMembers(orgId: string) {
  const api = useObservabilityApi();
  const query = useQuery({
    queryKey: peopleKeys.members(orgId),
    queryFn: () => api.listOrgMembers(orgId),
    enabled: !!orgId,
    staleTime: 5 * 60_000,
    retry: false,
  });
  // Never offer cached people or admin controls after a membership refusal.
  return { ...query, data: query.error ? undefined : query.data };
}

/** Every write below never retries and never re-sends; a refusal is shown, and the reads are re-fetched either way. */
export function useAddIssueComment() {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation<IssueComment, unknown, AddIssueCommentRequest>({
    mutationFn: (req) => api.addIssueComment(req),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: issueKeys.timelines() }),
  });
}

export function useUpdateIssueComment() {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation<IssueComment, unknown, UpdateIssueCommentRequest>({
    mutationFn: (req) => api.updateIssueComment(req),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: issueKeys.timelines() }),
  });
}

export function useDeleteIssueComment() {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation<void, unknown, DeleteIssueCommentRequest>({
    mutationFn: (req) => api.deleteIssueComment(req),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: issueKeys.timelines() }),
  });
}

/**
 * Assign or unassign. Like a triage it echoes the version read, never retries,
 * and on a stale one (Aborted: a conflict) re-reads the issue instead of
 * re-sending, so the reader sees what changed.
 */
export function useAssignIssue() {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation<Issue, unknown, AssignIssueRequest>({
    mutationFn: (req) => api.assignIssue(req),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: issueKeys.root() });
      void queryClient.invalidateQueries({ queryKey: activityKeys.root() });
    },
  });
}

/** Record a chat started from an issue. Idempotent per (issue, chat), so a repeat is harmless. */
export function useLinkIssueChat() {
  const api = useObservabilityApi();
  const queryClient = useQueryClient();
  return useMutation<LinkIssueChatResult, unknown, LinkIssueChatRequest>({
    mutationFn: (req) => api.linkIssueChat(req),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: issueKeys.timelines() }),
  });
}

// ── Activity (ActivityService) ────────────────────────────────────────────────

export const activityKeys = {
  root: () => ["observability", "activity"] as const,
  feed: (scope: DeliveryScope | null, target: { kind?: string; id?: string }) =>
    [...activityKeys.root(), scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? [], target.kind ?? "", target.id ?? ""] as const,
};

export const ACTIVITY_PAGE_SIZE = 50;

/** The scope's activity, newest first; narrowed to one object for its History. */
export function useActivity(scope: DeliveryScope | null, target: { kind?: string; id?: string } = {}, options: { enabled?: boolean } = {}) {
  const api = useObservabilityApi();
  return useInfiniteQuery({
    queryKey: activityKeys.feed(scope, target),
    queryFn: ({ pageParam }): Promise<ActivityList> =>
      api.listActivity({
        ...(scope as DeliveryScope),
        targetKind: target.kind,
        targetId: target.id,
        pageSize: ACTIVITY_PAGE_SIZE,
        pageToken: pageParam ?? undefined,
      }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!scope && (options.enabled ?? true),
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}
