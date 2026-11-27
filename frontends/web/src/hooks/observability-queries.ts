import { useCallback, useEffect } from "react";
// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for the observability area.
 *
 * Keys carry the time PRESET, never resolved timestamps. The ObservabilityService
 * reads on its generated hooks send the preset itself (a RelativeWindow the
 * server resolves against its own clock), so their request and key stay put;
 * the hand-written ones resolve the window inside the query function, at fetch
 * time. Either way a refetch (Refresh, live tail, the overview's minute poll)
 * re-reads "the last hour" as of now, and two components asking for the same
 * preset share one request.
 *
 * Paged queries pin the window their FIRST page was read over (the server
 * echoes the window it resolved, effective_time_range) and pass it to every
 * later page with the cursor — page two of "the last hour" must be the same
 * hour as page one (the server's page token is only valid with the same
 * request), or rows slip between pages as time moves.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { hashKey, useInfiniteQuery, type InfiniteData, useMutation, useQueries, useQuery, useQueryClient, type QueryClient, type QueryKey } from "@tanstack/react-query";

import { requestKey } from "@reliantlabs/forge-web-runtime/service-hooks";
import { scopedClient } from "@reliantlabs/forge-web-runtime/transport-scope";

import { ObservabilityService, QueryScopeSchema, type GetMetricResponse, type GetTraceResponse, type QueryPanelResponse } from "@/gen/controlplane/services/observability/v1/observability_pb";
import {
  observabilityServiceKeys,
  observabilityServiceQueries,
  useGetFlamegraph,
  useGetMetric,
  useGetReleaseHealth,
  useGetServiceGraph,
  useGetServiceGraphCatalog,
  useGetServiceOverview,
  useGetServiceSignals,
  useGetTrace,
  useGetWorkload,
  useListAttributeValues,
  useListMetricExemplars,
  useListMetrics,
  useListWorkloads,
} from "@/hooks/controlplane/observability-service-hooks_gen";
import { issuesServiceKeys, issuesServiceMutations, issuesServiceQueries } from "@/hooks/controlplane/issues-service-hooks_gen";
import {
  IssuesService,
  type ListIssueEventsResponse,
  type ListIssuesResponse,
  type ListIssueTimelineResponse,
} from "@/gen/controlplane/services/issues/v1/issues_pb";
import { connectClient } from "@/lib/connect";
import {
  attributeValuesRequest,
  effectiveWindowOf,
  flamegraphRequest,
  listTracesRequest,
  PANEL_HEADER,
  panelHeaders,
  queryPanelRequest,
  relativeWindow,
  searchLogsRequest,
  serviceOverviewRequest,
  serviceSignalsRequest,
  toAttributeValues,
  toFlamegraph,
  toListTraces,
  toObservabilityUsage,
  toPanelResult,
  toSearchLogs,
  toServiceOverview,
  toServiceSignals,
  toTrace,
  toWorkloadDetail,
  toWorkloads,
  traceRequest,
  wireScope,
  wireTimeRange,
  workloadRequest,
  workloadsRequest,
  type ObsWindow,
} from "@/services/controlPlane/observability/api";
import { serviceGraphCatalogRequest, serviceGraphRequest, toServiceGraph, toServiceGraphCatalog } from "@/services/controlPlane/observability/graphApi";
import type { CatalogRequest, GraphRequest } from "@/services/controlPlane/observability/graphTypes";
import { mapReleaseHealth, releaseHealthRequest } from "@/services/controlPlane/observability/deliveryApi";
import {
  getIssueRequest,
  type IssueTimeRangeInit,
  type ListIssuesInput,
  listIssueEventsRequest,
  listIssuesRequest,
  listIssueTimelineRequest,
  mapIssueDetail,
  toComment,
  toIssue,
  toIssueEventList,
  toIssueList,
  toIssueTimelineList,
  toLinkIssueChatResult,
  wireAssign,
  wireLinkIssueChat,
  wireStatusUpdate,
} from "@/services/controlPlane/observability/issuesApi";
import { metricCatalogRequest, metricExemplarsRequest, metricRequest, toMetricCatalog, toMetricDetail, toMetricExemplars } from "@/services/controlPlane/observability/metricsApi";
import { obsCall } from "@/services/controlPlane/observability/obsInterceptors";
import { alertHistoryOf, alertsOf, listAlertHistoryRequest, listAlertsRequest, listNotificationsRequest, listSilencesRequest, notificationsOf, receiversOf, silencesOf } from "@/services/controlPlane/observability/alertingApi";
import { alertingServiceKeys, useListAlertHistory, useListAlerts, useListNotifications, useListReceivers, useListSilences } from "@/hooks/controlplane/alerting-service-hooks_gen";
import { useObservabilityApi, type ObservabilityContextApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { useGetObservabilityUsage } from "@/hooks/controlplane/observability-billing-service-hooks_gen";
import { useObsQuota } from "@/services/controlPlane/observability/ObsQuotaContext";
import type { ActivityList } from "@/services/controlPlane/observability/activityApi";
import type { DeliveryScope, ReleaseHealth } from "@/services/controlPlane/observability/deliveryTypes";
import { alertingRetry, FilterUnsupportedError, issuesRetry, obsRetry, obsRetryDelay, releaseHealthRetry } from "@/services/controlPlane/observability/errors";
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
import type { GetMetricRequest, ListMetricExemplarsRequest } from "@/services/controlPlane/observability/metricsTypes";
import type {
  ObsScope,
  ListTracesResult,
  Panel,
  PanelResult,
  SearchLogsResult,
  Severity,
  TimeWindow,
  TraceSummary,
} from "@/services/controlPlane/observability/types";
import { panelRunsQuery } from "@/services/controlPlane/observability/panelKinds";
import { rangeMs, readRange, type RangePreset, type RangeRead } from "@/services/controlPlane/observability/window";
import { keepPreviousInOrg, keyNamesOrg, useRouteOrgIdOverride } from "@/lib/orgScope";

// Presentation never changes QueryPanel's computation: two dashboard tiles
// with different ids/titles/layout but the same query share one in-flight read.
function panelRequestKey(panel: Panel) {
  const { id: _id, title: _title, description: _description, width: _width, height: _height, ...request } = panel;
  return request;
}

/**
 * The hand-written reads' keys. Every key starts with the whole scope — org,
 * project, environments and attribute filters — so Refresh (invalidateObsScope)
 * re-reads exactly what is on screen, and two scopes never share an answer.
 * ObservabilityService's reads on generated hooks are keyed by their request
 * (observabilityServiceKeys), which carries the same scope.
 */
export const obsKeys = {
  org: () => ["observability", "org"] as const,
  all: (scope: ObsScope | null) =>
    ["observability", scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? [], scope?.filters ?? [], scope?.platformScope ?? false] as const,
  /** A postgres panel's read (AppDatabaseService); a telemetry panel's is the generated QueryPanel read's (panelQueryKey). */
  panel: (scope: ObsScope | null, range: RangePreset, panel: Panel, services: string[], groupBy: string[], offset?: PanelOffset) =>
    [...obsKeys.all(scope), "panel", range, panelRequestKey(panel), services, groupBy, offset ?? null] as const,
};

/** What names a scope in a QueryScope's protojson: everything but the window and the services filter. */
function scopeIdentity(scope: Record<string, unknown> | undefined): string | undefined {
  return scope ? hashKey([scope.orgId, scope.projectId, scope.envIds, scope.filters, scope.platformScope]) : undefined;
}

/**
 * The scope a generated read's key names: its request (protojson) carries a
 * QueryScope. The request is the key's last object, before any string suffixes
 * for pagination or panel attribution.
 */
function keyScope(key: QueryKey | undefined): string | undefined {
  const request = key ? [...key].reverse().find((part) => !!part && typeof part === "object") : undefined;
  return request ? scopeIdentity((request as { scope?: Record<string, unknown> }).scope) : undefined;
}

function scopeOf(scope: ObsScope): string | undefined {
  return scopeIdentity(requestKey(QueryScopeSchema, wireScope(scope, { startMs: 0, endMs: 0 })) as Record<string, unknown>);
}

/**
 * A read's placeholder while a new key loads: the previous answer when it was
 * for the same scope (a new range keeps the last rows on screen instead of a
 * blank table), and nothing when the scope changed (another project's or
 * environment's rows must never stand in for this one's). For the generated
 * reads, whose key is their request: the scope is read from it.
 */
export function sameScopePlaceholder<T>(scope: ObsScope | null, previous: T | undefined, previousKey: QueryKey | undefined): T | undefined {
  if (!scope || !previousKey) return undefined;
  const named = keyScope(previousKey);
  return named !== undefined && named === scopeOf(scope) ? previous : undefined;
}

/**
 * Refresh: every observability read of `scope` re-reads — the hand-written
 * ones under obsKeys.all(scope), and ObservabilityService's generated ones
 * whose request names this scope.
 */
export function invalidateObsScope(client: QueryClient, scope: ObsScope | null): Promise<void> {
  const named = scope ? scopeOf(scope) : undefined;
  return Promise.all([
    client.invalidateQueries({ queryKey: obsKeys.all(scope) }),
    named === undefined ? undefined : client.invalidateQueries({ queryKey: observabilityServiceKeys.all, predicate: (query) => keyScope(query.queryKey) === named }),
  ]).then(() => undefined);
}

/**
 * Every observability read, whatever its scope: the hand-written ones under
 * `["observability"]` and ObservabilityService's generated ones (workloads, a
 * workload, the overview, …), which that prefix no longer covers. For a write
 * that changes what all of them show (a workload restart).
 */
export function invalidateAllObservability(client: QueryClient): Promise<void> {
  return Promise.all([
    client.invalidateQueries({ queryKey: ["observability"] }),
    client.invalidateQueries({ queryKey: observabilityServiceKeys.all }),
  ]).then(() => undefined);
}

/**
 * The call options of every read on a generated hook: the read marked, at the
 * moment it starts, as a poll, a prefetch or a person's read, for the
 * control-plane transport's read stage (obsInterceptors.ts). The runtime calls
 * it synchronously inside the query function, where runAsPoll's mark holds.
 */
const obsRead = () => obsCall();

/** The request of a read that is disabled because there is no scope yet: it is never sent. */
const NO_SCOPE: ObsScope = { orgId: "", envIds: [], filters: [] };

/** A preset as the window the server resolves, capped at what `read` keeps (readRange). */
function lastOf(range: RangePreset, read?: RangeRead) {
  return relativeWindow(rangeMs(read ? readRange(range, read) : range));
}

/** A relative window as resolved here, now: `lastMs` long, ending `endsAgoMs` before now. */
function windowAsOfNow(lastMs: number, endsAgoMs = 0): TimeWindow {
  const endMs = Date.now() - endsAgoMs;
  return { startMs: endMs - lastMs, endMs };
}

/**
 * The control-plane ObservabilityService client of a hand-written query (the
 * paged reads): its QueryClient's TransportScope's, else the app's, as the
 * generated hooks resolve theirs.
 */
const observabilityClient = scopedClient(ObservabilityService, connectClient(ObservabilityService));

// ── Quota-aware reads and polls ──────────────────────────────────────────────
//
// Every read that spends the org's ClickHouse query quota (logs, traces,
// metrics, profiles, workloads, issues, release health) is guarded by the
// org's quota state on the control-plane transport (obsInterceptors.ts): while
// the quota is gone it answers with the quota error at once, without a
// request. Every poll of one goes through `usePolled`. Alerts, notifications,
// usage, delivery and activity read Postgres and are left alone: a quota pause
// must not stop them.

/**
 * Polls `queryKey` every `intervalMs` through the console's ONE poll scheduler
 * (pollRegistry.ts): a single timer per key however many panels watch it,
 * stopped while the quota is gone, a quarter as fast while it is tight, idle in
 * a hidden tab, and each poll marked `Forge-Poll` for the server. `false` or a
 * disabled query does not poll. `quota: false` is for a read that does not
 * spend the ClickHouse quota (a postgres panel): its polls ignore the quota.
 */
function usePolled(queryKey: QueryKey, intervalMs: number | false, enabled = true, quota = true) {
  const client = useQueryClient();
  const { polls } = useObsQuota();
  const keyHash = hashKey(queryKey);
  useEffect(() => {
    if (!enabled || intervalMs === false) return;
    return polls.subscribe(client, queryKey, intervalMs, { quota });
    // The key is compared by hash: a new array with the same content is the same key.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, polls, keyHash, intervalMs, enabled, quota]);
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
  // On an org page, the route's org (lib/orgScope.ts); the query only outside the org tree.
  const routeOrg = useRouteOrgIdOverride();
  const org = useQuery({
    queryKey: obsKeys.org(),
    queryFn: () => api.currentOrgId(),
    staleTime: 5 * 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    enabled: routeOrg === undefined,
  });
  if (routeOrg !== undefined) return { orgId: routeOrg, isLoading: routeOrg === null, error: null, refetch: org.refetch };
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error, refetch: org.refetch };
}

// ── Overview ─────────────────────────────────────────────────────────────────

export function useServiceOverview(scope: ObsScope | null, range: RangePreset, services: string[] = [], groupBy: string[] = []) {
  const request = serviceOverviewRequest({ ...(scope ?? NO_SCOPE), window: lastOf(range, "overview"), services, groupBy });
  const query = useGetServiceOverview(request, {
    select: toServiceOverview,
    enabled: !!scope,
    staleTime: 30_000,
    placeholderData: (previous, previousQuery) => sameScopePlaceholder(scope, previous, previousQuery?.queryKey),
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
  usePolled(observabilityServiceKeys.getServiceOverview(request), 60_000, !!scope);
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

/**
 * A later page's request: the first page's, asked over the window the server
 * resolved for the first page (absolute, so the page token stays valid), with
 * the cursor.
 */
function pageRequest<R extends { scope?: object; pageToken?: string }>(first: R, page: PageParam | null): R {
  return page ? { ...first, scope: { ...first.scope, timeRange: wireTimeRange(page.window) }, pageToken: page.cursor } : first;
}

/**
 * The window the first page was read over: the server's (effective_time_range),
 * else, from a server that names none, the absolute bounds sent with the request.
 */
function firstPageWindow(res: { effectiveTimeRange?: { start?: Timestamp; end?: Timestamp } }, page: PageParam | null, pinned: TimeWindow): TimeWindow {
  return page?.window ?? effectiveWindowOf(res) ?? pinned;
}

/**
 * The logs, newest first, a page at a time. Hand-written over the generated
 * client rather than useSearchLogsInfinite: a generated paged read sends the
 * first page's request again with only the page token changed, and a later
 * page must also carry the first page's resolved window. Page one pins absolute
 * bounds at fetch time; the key keeps the relative preset so refetches read anew.
 * Keyed like the generated read of the first page, so
 * ObservabilityService's invalidations reach it.
 */
export function useSearchLogs(scope: ObsScope | null, filters: LogFilters, live: boolean) {
  const window = lastOf(filters.range, "logs");
  const request = searchLogsRequest({
    ...(scope ?? NO_SCOPE),
    window,
    query: filters.query,
    services: filters.services,
    severities: filters.severities,
    traceId: filters.traceId,
    spanId: filters.spanId,
    limit: LOG_PAGE_SIZE,
  });
  // Keep the live head separate from paged history: polling must never refetch
  // every retained page, nor start at an older cursor after page eviction.
  const historyKey = observabilityServiceKeys.searchLogsInfinite(request);
  const liveKey = [...historyKey, "live"];
  const queryKey = live ? liveKey : historyKey;
  const client = useQueryClient();
  const query = useInfiniteQuery({
    queryKey,
    // Keep rows on screen while switching modes, but only for this exact
    // request. Placeholder data never becomes a cached page or cursor.
    placeholderData: () => {
      const sibling = client.getQueryData<InfiniteData<SearchLogsResult & { window: TimeWindow }, PageParam | null>>(
        live ? historyKey : liveKey
      );
      const first = sibling?.pages[0];
      return first ? { pages: [first], pageParams: [null] } : undefined;
    },
    queryFn: async (context): Promise<SearchLogsResult & { window: TimeWindow }> => {
      const page = context.pageParam;
      // A refetch starts at the null first-page param; React Query derives each
      // subsequent param from the freshly fetched page, not the retained data.
      const pinned = page ?? { cursor: "", window: windowAsOfNow(window.lastMs) };
      const res = await observabilityClient(context).searchLogs(pageRequest(request, pinned), obsCall({ signal: context.signal }));
      return { ...toSearchLogs(res, page?.cursor), window: firstPageWindow(res, page, pinned.window) };
    },
    initialPageParam: null as PageParam | null,
    // Live mode reads only the latest page. Pause it to browse history using
    // the separate paged cache, which retains page one's null param.
    getNextPageParam: (last) => (!live && last.nextCursor ? { cursor: last.nextCursor, window: last.window } : undefined),
    maxPages: live ? 1 : undefined,
    enabled: !!scope,
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
  /** Only traces of this user id. */
  userId?: string;
  /** Only traces of this browser session. */
  sessionId?: string;
  /** Only traces a browser of this client / OS started (exact). */
  browser?: string;
  os?: string;
  /** Only traces with a browser span of this frontend build / a server span of this backend build. */
  frontendVersion?: string;
  backendVersion?: string;
}

export const TRACE_PAGE_SIZE = 100;

/**
 * Refuse rows a control plane older than a filter answered without it (it
 * ignores the unknown field, so the list would look filtered and not be).
 *
 * A row proves the filter was ignored when it names a DIFFERENT value. A list
 * where no row names any value is an older server's signature too, unless
 * the request narrowed the counted spans (a service or an attribute filter):
 * then the browser spans that carry the client, the build and often the user
 * are not part of any row, and a server that applied the filter answers with
 * the fields empty. "The api's traces from Chrome" are api spans only.
 */
export function assertTraceFiltersApplied(filters: TraceFilters, traces: TraceSummary[], narrowed: boolean) {
  if (traces.length === 0) return;
  const ignored = (wanted: string | undefined, valuesOf: (t: TraceSummary) => string[], what: string) => {
    if (!wanted) return;
    const contradicted = traces.some((t) => {
      const values = valuesOf(t).filter(Boolean);
      return values.length > 0 && !values.includes(wanted);
    });
    const neverNamed = !narrowed && traces.every((t) => valuesOf(t).every((v) => !v));
    if (contradicted || neverNamed) throw new FilterUnsupportedError(what, "traces");
  };
  ignored(filters.userId, (t) => [t.userId], "user");
  ignored(filters.sessionId, (t) => [t.sessionId], "browser session");
  ignored(filters.browser, (t) => [t.client.browserName], "browser");
  ignored(filters.os, (t) => [t.client.osName], "OS");
  ignored(filters.frontendVersion, (t) => [t.frontendVersion], "frontend version");
  ignored(filters.backendVersion, (t) => t.backendBuilds.map((b) => b.version), "backend version");
}

/**
 * The traces, a page at a time. The key carries the relative preset, but page
 * one sends absolute bounds pinned at fetch time, so even a server without
 * effective_time_range can paginate over exactly the same interval.
 */
export function useListTraces(scope: ObsScope | null, filters: TraceFilters) {
  const window = lastOf(filters.range, "traces");
  const request = listTracesRequest({
    ...(scope ?? NO_SCOPE),
    window,
    rootSpanName: filters.rootSpanName,
    services: filters.services,
    errorsOnly: filters.errorsOnly,
    minDurationMs: filters.minDurationMs,
    groupBy: filters.groupBy,
    userId: filters.userId,
    sessionId: filters.sessionId,
    browser: filters.browser,
    os: filters.os,
    frontendVersion: filters.frontendVersion,
    backendVersion: filters.backendVersion,
    limit: TRACE_PAGE_SIZE,
  });
  return useInfiniteQuery({
    queryKey: observabilityServiceKeys.listTracesInfinite(request),
    queryFn: async (context): Promise<ListTracesResult & { window: TimeWindow }> => {
      const page = context.pageParam;
      // Pin BEFORE page one, including on servers that do not echo the effective
      // range. Keep the relative preset in the cache key so refetches read anew.
      const pinned = page ?? { cursor: "", window: windowAsOfNow(window.lastMs) };
      const res = await observabilityClient(context).listTraces(pageRequest(request, pinned), obsCall({ signal: context.signal }));
      const result = toListTraces(res);
      assertTraceFiltersApplied(filters, result.traces, filters.services.length > 0 || (scope?.filters.length ?? 0) > 0);
      return { ...result, window: page?.window ?? effectiveWindowOf(res) ?? pinned.window };
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
  const id = traceId ?? "";
  const select = useCallback((res: GetTraceResponse) => toTrace(res, id), [id]);
  return useGetTrace(traceRequest({ ...(scope ?? NO_SCOPE), traceId: id, window: lastOf(range, "traces") }), {
    select,
    enabled: !!scope && !!traceId,
    staleTime: 15_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

// ── Service graph ────────────────────────────────────────────────────────────

/** How a graph or catalog read is cached: the views differ (the graph page's frozen snapshot, the overview's minute). */
export interface GraphReadOptions {
  enabled?: boolean;
  staleTime: number;
}

/**
 * The RPC catalog over `window`: every method the scope served or declares,
 * narrowed to `filter`. A view that pairs it with the graph (GraphView) passes
 * both the same absolute window, so they describe one instant.
 */
export function useServiceGraphCatalog(scope: ObsScope | null, window: ObsWindow, filter: Pick<CatalogRequest, "rpcService" | "rpcIds">, options: GraphReadOptions) {
  return useGetServiceGraphCatalog(serviceGraphCatalogRequest({ ...(scope ?? NO_SCOPE), window, ...filter }), {
    select: toServiceGraphCatalog,
    enabled: !!scope && (options.enabled ?? true),
    staleTime: options.staleTime,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

/** The service graph over `window`: its nodes and edges, narrowed to `filter`. */
export function useServiceGraph(scope: ObsScope | null, window: ObsWindow, filter: Omit<GraphRequest, keyof ObsScope | "window">, options: GraphReadOptions) {
  return useGetServiceGraph(serviceGraphRequest({ ...(scope ?? NO_SCOPE), window, ...filter }), {
    select: toServiceGraph,
    enabled: !!scope && (options.enabled ?? true),
    staleTime: options.staleTime,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

// ── Metrics and dashboards ───────────────────────────────────────────────────

/**
 * Where a comparison read's window sits. `endsAgoMs`: that long before now,
 * the dashboard's "previous period" (the preset's own length back), which the
 * server resolves against its clock, so the request and key stay put.
 * `endsAtMs`: ending at that instant, "previous release" (the moment the
 * scoped environment's current release was promoted), fixed, so absolute bounds.
 */
export type PanelOffset = { endsAgoMs: number } | { endsAtMs: number };

/** How a panel query is run beyond its scope and window. */
export interface PanelQueryOptions {
  /** A comparison: the window shifted back in time. */
  offset?: PanelOffset;
  /** Auto-refresh: re-read the panel this often (ms). */
  refetchInterval?: number | false;
  enabled?: boolean;
  /** The dashboard the panel belongs to: labels the read in control-plane's query_log (Forge-Panel). */
  dashboardId?: string;
}

/** The window of a panel read over `range` (capped at what `read` keeps), shifted back by `offset`. */
export function panelWindow(range: RangePreset, read: RangeRead | undefined, offset?: PanelOffset): ObsWindow {
  const lastMs = rangeMs(read ? readRange(range, read) : range);
  if (offset && "endsAtMs" in offset) return { startMs: offset.endsAtMs - lastMs, endMs: offset.endsAtMs };
  return relativeWindow(lastMs, offset?.endsAgoMs ?? 0);
}

/** A window as bounds: a relative one resolved here, as of now. */
export function boundsOf(window: ObsWindow): TimeWindow {
  return "lastMs" in window ? windowAsOfNow(window.lastMs, window.endsAgoMs) : window;
}

/**
 * Cache a panel's bounds with its samples, never resolve them in select:
 * observers may re-select a retained response long after it was fetched.
 * QueryPanel names no effective range, so use the clock at answer time.
 */
export async function readPanelWithWindow(read: () => Promise<QueryPanelResponse>, window: ObsWindow): Promise<PanelResult> {
  const response = await read();
  return toPanelResult(response, boundsOf(window));
}

/**
 * The cache key of usePanelQuery's read: the generated QueryPanel key with
 * any Forge-Panel attribution, or the hand-written postgres panel key.
 */
export function panelQueryKey(scope: ObsScope | null, range: RangePreset, panel: Panel, services: string[], groupBy: string[] = [], offset?: PanelOffset, dashboardId?: string): QueryKey {
  if (panel.kind === "postgres") return obsKeys.panel(scope, range, panel, services, groupBy, offset);
  const key = observabilityServiceKeys.queryPanel(panelRequest(scope, range, panel, services, groupBy, offset));
  return attributedPanelKey(key, panelHeaders(dashboardId, panel.id));
}

function attributedPanelKey(key: QueryKey, headers: ReturnType<typeof panelHeaders>): QueryKey {
  const attribution = headers?.[PANEL_HEADER];
  // Call options do not affect the generated key. Keep its prefix for scoped
  // refreshes, but do not deduplicate requests that log under different labels.
  return attribution ? [...key, PANEL_HEADER, attribution] : key;
}

function panelRequest(scope: ObsScope | null, range: RangePreset, panel: Panel, services: string[], groupBy: string[], offset?: PanelOffset) {
  return queryPanelRequest({ ...(scope ?? NO_SCOPE), window: panelWindow(range, panelRead(panel), offset), panel, services, stepSeconds: 0, groupBy });
}

/** Shared generated identity and fetched panel shape for dashboard, explorer and release reads. */
export function queryPanelRead(request: ReturnType<typeof queryPanelRequest>, window: ObsWindow, headers?: ReturnType<typeof panelHeaders>) {
  return {
    queryKey: attributedPanelKey(observabilityServiceKeys.queryPanel(request), headers),
    queryFn: (context: { client: QueryClient; signal: AbortSignal }) => readPanelWithWindow(
      () => observabilityClient(context).queryPanel(request, obsCall({ headers, signal: context.signal })),
      window,
    ),
    staleTime: 30_000,
    retry: obsRetry,
    retryDelay: obsRetryDelay,
  };
}

/**
 * One dashboard panel, computed server-side (QueryPanel). The dashboard's
 * service filter rides as `services`; search and text panels, and a kind this
 * console does not draw, are never run here (panelRunsQuery).
 *
 * Like every observability read it waits its turn in the page's read scheduler
 * (obsInterceptors.ts), and a refusal for the server's concurrent-read cap is retried
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
  const api = useObservabilityApi();
  const window = panelWindow(range, panelRead(panel), options.offset);
  const enabled = !!scope && panelRunsQuery(panel.kind) && (options.enabled ?? true);
  // A postgres panel reads the environment's own database (AppDatabaseService), not ClickHouse: the telemetry quota does not apply to it.
  const postgres = panel.kind === "postgres";
  const request = panelRequest(scope, range, panel, services, groupBy, options.offset);
  const headers = panelHeaders(options.dashboardId, panel.id);
  // One query either way, so a panel is one cache entry: QueryPanel's generated read, or the postgres read.
  const telemetryRead = queryPanelRead(request, window, headers);
  const databaseRead = {
    queryKey: obsKeys.panel(scope, range, panel, services, groupBy, options.offset),
    queryFn: ({ signal }: { signal: AbortSignal }) => queryPostgresPanel(api, scope as ObsScope, boundsOf(window), panel, signal),
    staleTime: 30_000,
    retry: obsRetry,
    retryDelay: obsRetryDelay,
  };
  const read = postgres ? databaseRead : telemetryRead;
  const query = useQuery({ ...read, enabled });
  // Nor does the quota slow or stop a postgres panel's refresh.
  usePolled(read.queryKey, options.refetchInterval ?? false, enabled, !postgres);
  return query;
}

/**
 * A postgres panel's read (AppDatabaseService.QueryDatabasePanel): one
 * environment's database, so a scope over several environments, or none, is
 * refused here with the reason, as the server would refuse it.
 */
function queryPostgresPanel(api: ObservabilityContextApi, scope: ObsScope, window: TimeWindow, panel: Panel, signal: AbortSignal) {
  if (scope.envIds.length !== 1) {
    throw new ConnectError("a postgres panel reads one environment's database: choose one environment", Code.InvalidArgument);
  }
  if (!panel.postgres) throw new ConnectError("the panel has no postgres query", Code.InvalidArgument);
  return api.queryDatabasePanel({ environmentId: scope.envIds[0], window, query: panel.postgres, panelId: panel.id }, { signal });
}

/**
 * The environment's databases a postgres panel may read, with the tables and
 * columns its dashboard role may SELECT: the panel editor's schema browser.
 * Postgres, not ClickHouse, so no quota guard.
 */
export function useDashboardDatabases(environmentId: string | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: ["observability", "dashboard-databases", environmentId ?? ""] as const,
    queryFn: ({ signal }) => api.listDashboardDatabases(environmentId as string, { signal }),
    enabled: !!environmentId,
    staleTime: 60_000,
    retry: obsRetry,
    retryDelay: obsRetryDelay,
  });
}

// ── Profiles ─────────────────────────────────────────────────────────────────

export interface FlamegraphFilters {
  range: RangePreset;
  service: string;
  profileType: string;
  traceId?: string;
}

export function useFlamegraph(scope: ObsScope | null, filters: FlamegraphFilters) {
  const request = flamegraphRequest({
    ...(scope ?? NO_SCOPE),
    window: lastOf(filters.range),
    service: filters.service,
    profileType: filters.profileType,
    traceId: filters.traceId,
  });
  return useGetFlamegraph(request, {
    select: toFlamegraph,
    enabled: !!scope && !!filters.service && !!filters.profileType,
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
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
  const unfiltered = scope && scope.filters.length ? { ...scope, filters: [] } : scope;
  return useGetServiceSignals(serviceSignalsRequest({ ...(unfiltered ?? NO_SCOPE), window: lastOf(range, "overview") }), {
    select: toServiceSignals,
    enabled: !!unfiltered,
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
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
  // ObservabilityBillingService's generated read (the billing panel's own key
  // and fetch), mapped to the legacy panel's model.
  return useGetObservabilityUsage({ orgId: orgId ?? "" }, {
    select: toObservabilityUsage,
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
  // Value lookups are capped at 7 days server-side.
  return useListAttributeValues(attributeValuesRequest({ ...(scope ?? NO_SCOPE), window: lastOf(range, "overview"), key, prefix, signal }), {
    select: toAttributeValues,
    enabled: !!scope && !!key,
    staleTime: 60_000,
    // The previous prefix's values, but never another org's (lib/orgScope).
    placeholderData: keepPreviousInOrg(scope?.orgId ?? null),
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

// ── Metrics explorer ─────────────────────────────────────────────────────────

/**
 * What a panel reads, for its longest window (forge obsquery Panel.MaxRange):
 * a sql panel and the browser kinds by kind, the others by signal.
 */
export function panelRead(panel: Pick<Panel, "kind" | "signal">): RangeRead {
  switch (panel.kind) {
    case "sql":
    case "funnel":
    case "postgres":
      return "sql";
    case "issues":
      return "issues";
    case "sessions":
      return "sessions";
  }
  return panel.signal === "logs" ? "logs" : panel.signal === "metrics" ? "metrics" : "traces";
}

/** The catalog's window: the server caps it at 24 hours, so a longer preset would only be clamped there. */
const CATALOG_MAX_RANGE: RangePreset = "24h";

function catalogRange(range: RangePreset): RangePreset {
  return rangeMs(range) > rangeMs(CATALOG_MAX_RANGE) ? CATALOG_MAX_RANGE : range;
}

/**
 * The metric catalog: every metric the scope's telemetry carries with its kind,
 * unit, series estimate and services. The window is the explorer's, capped at 24
 * hours (a metric last seen three days ago is still reachable by name). The
 * server caches the answer a minute; the client keeps it as long.
 */
export function useMetricCatalog(scope: ObsScope | null, range: RangePreset, name = "", limit = 0) {
  return useListMetrics(metricCatalogRequest({ ...(scope ?? NO_SCOPE), window: lastOf(catalogRange(range)), name, limit }), {
    select: toMetricCatalog,
    enabled: !!scope,
    staleTime: 60_000,
    placeholderData: keepPreviousInOrg(scope?.orgId ?? null),
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

/** One metric's detail: attributes with cardinality, export interval and suggested breakdowns. */
export function useMetric(scope: ObsScope | null, range: RangePreset, name: string | undefined, kind?: GetMetricRequest["kind"]) {
  const metric = name ?? "";
  const select = useCallback((res: GetMetricResponse) => toMetricDetail(res, metric), [metric]);
  return useGetMetric(metricRequest({ ...(scope ?? NO_SCOPE), window: lastOf(catalogRange(range)), name: metric, kind }), {
    select,
    enabled: !!scope && !!name,
    staleTime: 60_000,
    // NotFound is the answer "no data for this metric in this window": a state to show, not a fault to retry.
    retry: (failureCount, error) => !(error instanceof ConnectError && error.code === Code.NotFound) && obsRetry(failureCount, error),
    retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

/** What an exemplar read is keyed by: the metric, its filters and the bucket asked about (epoch ms, so two clicks on one bucket share an answer). */
export type ExemplarsQuery = ListMetricExemplarsRequest;

/** Exemplars of one metric (a sampled observation and its trace), stored traces first. Maximum range 7 days. */
export function useMetricExemplars(scope: ObsScope | null, range: RangePreset, request: ExemplarsQuery | null, options: { enabled?: boolean } = {}) {
  return useListMetricExemplars(metricExemplarsRequest({ ...(scope ?? NO_SCOPE), window: lastOf(range, "overview"), ...(request ?? { name: "" }) }), {
    select: toMetricExemplars,
    enabled: !!scope && !!request?.name && (options.enabled ?? true),
    staleTime: 60_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

// ── Workloads (customer metadata) ────────────────────────────────────────────

export function useWorkloads(scope: ObsScope | null, range: RangePreset, services: string[] = []) {
  const request = workloadsRequest({ ...(scope ?? NO_SCOPE), window: lastOf(range, "overview"), services });
  const query = useListWorkloads(request, {
    select: toWorkloads,
    enabled: !!scope,
    staleTime: 30_000,
    placeholderData: (previous, previousQuery) => sameScopePlaceholder(scope, previous, previousQuery?.queryKey),
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
  usePolled(observabilityServiceKeys.listWorkloads(request), 60_000, !!scope);
  return query;
}

export function useWorkload(scope: ObsScope | null, range: RangePreset, workload: string | undefined, container?: string) {
  return useGetWorkload(workloadRequest({ ...(scope ?? NO_SCOPE), window: lastOf(range, "overview"), workload: workload ?? "", container }), {
    select: toWorkloadDetail,
    enabled: !!scope && !!workload,
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

// ── Alerting (AlertingService) ───────────────────────────────────────────────
//
// Org-scoped, not env-scoped: the server takes one optional env id on the
// alert reads, so an env page passes its own and the project area filters the
// org's list to the environments it has selected. Firing alerts and the
// notification count poll; receivers and silences change only by hand.
//
// Every read and write runs on AlertingService's generated hooks
// (hooks/controlplane/alerting-service-hooks_gen.ts), on its QueryClient's
// transport (a TransportScope's: the forge tour's sample app, a preview; else
// the app's). Each is keyed by its request, which names the org.

/** Pending and firing alerts. One env id narrows server-side; undefined is every env of the org. */
export function useAlerts(orgId: string | null, envId?: string) {
  return useListAlerts(listAlertsRequest(orgId ?? "", envId), {
    select: alertsOf,
    enabled: !!orgId,
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: alertingRetry,
  });
}

/** The history of the last `range`, as of whenever the server answers: the request (and key) stays the same as the window moves. */
export function useAlertHistory(orgId: string | null, range: RangePreset, envId?: string) {
  return useListAlertHistory(listAlertHistoryRequest(orgId ?? "", { envId, lookbackMs: rangeMs(range), limit: 500 }), {
    select: alertHistoryOf,
    enabled: !!orgId,
    staleTime: 30_000,
    retry: alertingRetry,
  });
}

export function useSilences(orgId: string | null, includeExpired: boolean) {
  return useListSilences(listSilencesRequest(orgId ?? "", includeExpired), {
    select: silencesOf,
    enabled: !!orgId,
    staleTime: 30_000,
    retry: alertingRetry,
  });
}

export function useReceivers(orgId: string | null) {
  return useListReceivers(
    { orgId: orgId ?? "" },
    {
      select: receiversOf,
      enabled: !!orgId,
      staleTime: 60_000,
      retry: alertingRetry,
    }
  );
}

/**
 * The org's in-app notifications and unread count — the indicator in the
 * Observability header and the forge nav. Polls once a minute; Alertmanager
 * groups and repeats on its own schedule, so faster would show nothing new.
 */
export function useNotifications(orgId: string | null) {
  return useListNotifications(listNotificationsRequest(orgId ?? "", { limit: 50 }), {
    select: notificationsOf,
    enabled: !!orgId,
    staleTime: 30_000,
    // Stop polling an org whose alerting answered "not configured" or "denied".
    refetchInterval: (query) => (query.state.error ? false : 60_000),
    retry: alertingRetry,
  });
}

/** Re-reads everything alerting shows for the org: every AlertingService read whose request names it. Another org's are left alone. */
export function invalidateOrgAlerting(client: QueryClient, orgId: string | null): Promise<void> {
  return client.invalidateQueries({ queryKey: alertingServiceKeys.all, predicate: (query) => keyNamesOrg(query.queryKey, orgId ?? "") });
}

/**
 * What every alerting write's generated mutation takes: no refresh of the
 * generated entity scope, and instead everything alerting shows for the org
 * re-read once the write settles, failed or not (a silence changes what
 * fires; a receiver changes what routes).
 */
export function useAlertingWriteOptions(orgId: string | null) {
  const queryClient = useQueryClient();
  return { invalidate: false as const, onSettled: () => invalidateOrgAlerting(queryClient, orgId) };
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
 * The server's verdict on one promotion (GetReleaseHealth). A generated read,
 * keyed by its request; the panel's Refresh re-reads every one with the
 * ledger. The verdict of a promotion whose after window is still open changes
 * as traffic arrives, so it is re-read every minute until the server says the
 * window is complete.
 */
export function useReleaseHealth(scope: DeliveryScope | null, promotionId: string, windowMs: number) {
  const request = releaseHealthRequest({ ...(scope ?? NO_SCOPE), promotionId, windowMs });
  const enabled = !!scope && !!promotionId;
  const query = useGetReleaseHealth(request, {
    select: mapReleaseHealth,
    enabled,
    staleTime: 30_000,
    retry: releaseHealthRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
  usePolled(observabilityServiceKeys.getReleaseHealth(request), query.data && !query.data.afterComplete ? 60_000 : false, enabled);
  return query;
}

/**
 * GetReleaseHealth for several environments' current promotions at once, by
 * environment id: the Environments page's obs-check column. The same cache
 * entries as useReleaseHealth, so a release's Health tab and this page share
 * one read. Not polled: the page is a summary, and a release's own page polls.
 */
export function useReleaseHealths(
  orgId: string | null,
  projectId: string,
  items: readonly { envId: string; promotionId: string }[],
  windowMs: number
): Map<string, { data?: ReleaseHealth; isLoading: boolean; error: unknown }> {
  const results = useQueries({
    queries: items.map(({ envId, promotionId }) =>
      observabilityServiceQueries.getReleaseHealth(releaseHealthRequest({ orgId: orgId ?? "", projectId, envIds: [envId], promotionId, windowMs }), {
        select: mapReleaseHealth,
        enabled: !!orgId && !!promotionId,
        staleTime: 30_000,
        retry: releaseHealthRetry,
        retryDelay: obsRetryDelay,
        callOptions: obsRead,
      })
    ),
  });
  const byEnv = new Map<string, { data?: ReleaseHealth; isLoading: boolean; error: unknown }>();
  items.forEach(({ envId }, index) => {
    const result = results[index];
    byEnv.set(envId, { data: result?.data, isLoading: !!result?.isLoading, error: result?.error ?? null });
  });
  return byEnv;
}

/**
 * GetReleaseHealth for each environment's CURRENT promotion (no promotion id:
 * the server judges the env's running release against the one it replaced),
 * by environment id: a Releases dashboard's release_health panel. Polled
 * while a verdict's after window is still open, like useReleaseHealth.
 */
export function useCurrentReleaseHealths(
  scope: DeliveryScope | null,
  envIds: readonly string[],
  windowMs: number
): Map<string, { data?: ReleaseHealth; isLoading: boolean; error: unknown }> {
  const results = useQueries({
    queries: envIds.map((envId) =>
      observabilityServiceQueries.getReleaseHealth(releaseHealthRequest({ orgId: scope?.orgId ?? "", projectId: scope?.projectId, envIds: [envId], windowMs }), {
        select: mapReleaseHealth,
        enabled: !!scope?.orgId,
        staleTime: 30_000,
        refetchInterval: (query) => (query.state.data && !query.state.data.afterComplete ? 60_000 : false),
        retry: releaseHealthRetry,
        retryDelay: obsRetryDelay,
        callOptions: obsRead,
      })
    ),
  });
  const byEnv = new Map<string, { data?: ReleaseHealth; isLoading: boolean; error: unknown }>();
  envIds.forEach((envId, index) => {
    const result = results[index];
    byEnv.set(envId, { data: result?.data, isLoading: !!result?.isLoading, error: result?.error ?? null });
  });
  return byEnv;
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
  /** Only the issues with an event of this user id in the window (the server filters). */
  userId?: string;
  /** Only the issues with an event of this browser / OS / build in the window (the server filters). */
  browser?: string;
  os?: string;
  serviceVersion?: string;
  sort: IssueSort;
  text: string;
}

export const ISSUE_PAGE_SIZE = 50;

/**
 * A value no one has, which useIssues asks for to learn whether a filter is
 * applied. User ids, browsers, OSes and builds are the application's own
 * strings, so no fixed value is reserved: each read draws a fresh random one
 * (122 bits) nobody can hold. 42 characters, inside every probed field's
 * validated length (ListIssuesRequest's browser and os take at most 64).
 */
export function issueFilterProbe(): string {
  return `probe-${crypto.randomUUID()}`;
}

/**
 * The filters useIssues probes, each by its own request field and its own
 * label. They did not all ship together (user_id came before the client and
 * build fields), so each is asked about alone.
 */
const ISSUE_PROBED_FILTERS = [
  { key: "userId", label: "user" },
  { key: "browser", label: "browser" },
  { key: "os", label: "OS" },
  { key: "serviceVersion", label: "build" },
] as const satisfies readonly { key: keyof IssueFilters; label: string }[];

export const ISSUE_EVENTS_PAGE_SIZE = 20;
export const ISSUE_TIMELINE_PAGE_SIZE = 100;

/** The requests of the reads that name one issue; their generated keys are issueKeys'. */
const issueDetailRequest = (issueId: string, range: RangePreset) => getIssueRequest({ issueId, window: lastOf(range) });
const issueEventsRequest = (issueId: string) => listIssueEventsRequest({ issueId, pageSize: ISSUE_EVENTS_PAGE_SIZE });
const issueTimelineRequest = (issueId: string) => listIssueTimelineRequest({ issueId, pageSize: ISSUE_TIMELINE_PAGE_SIZE });

/**
 * The generated key (issuesServiceKeys) each issue read is cached under, for
 * code that names one entry. Every issue read is under issuesServiceKeys.all,
 * so a triage (which can move an issue between lists, change its detail and
 * add to its history) re-reads all of them, and the panel's Refresh does too.
 */
export const issueKeys = {
  root: () => issuesServiceKeys.all,
  detail: (issueId: string, range: RangePreset) => issuesServiceKeys.getIssue(issueDetailRequest(issueId, range)),
  events: (issueId: string) => issuesServiceKeys.listIssueEventsInfinite(issueEventsRequest(issueId)),
  timelines: () => issuesServiceKeys.listIssueTimeline.all,
  timeline: (issueId: string) => issuesServiceKeys.listIssueTimelineInfinite(issueTimelineRequest(issueId)),
};

/**
 * The issue reads that name one issue — its detail, events and timeline — on
 * their generated keys (issuesServiceKeys), whose request carries the id.
 */
const ISSUE_READS: readonly QueryKey[] = [issuesServiceKeys.getIssue.all, issuesServiceKeys.listIssueEvents.all, issuesServiceKeys.listIssueTimeline.all];

/** Whether `key` is one of ISSUE_READS for `issueId`: the request in a generated key is its one object entry. */
function namesIssue(key: QueryKey, issueId: string): boolean {
  if (!ISSUE_READS.some((prefix) => prefix.every((part, i) => key[i] === part))) return false;
  return key.some((part) => !!part && typeof part === "object" && !Array.isArray(part) && (part as { issueId?: unknown }).issueId === issueId);
}

/** A triage (status or assignee) can move an issue between lists, change its detail and add to its history: every issue read, and the activity feed. */
function invalidateIssueTriage(client: QueryClient) {
  void client.invalidateQueries({ queryKey: issueKeys.root() });
  void client.invalidateQueries({ queryKey: activityKeys.root() });
}

/** A comment or a chat link changes only the issue's timeline. */
function invalidateIssueTimelines(client: QueryClient) {
  void client.invalidateQueries({ queryKey: issueKeys.timelines() });
}

/** IssuesService on the QueryClient's transport (a TransportScope's, else the app's), as the generated hooks resolve it. */
const issuesClient = scopedClient(IssuesService, connectClient(IssuesService));

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
      const queries = client.getQueryCache().findAll({ predicate: (query) => namesIssue(query.queryKey, issueId) });
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

/**
 * A generated read's options with its fetch run through readIssue: the key,
 * the client call and the call options stay the generated ones.
 */
function guardedByIssue<O extends { queryKey: QueryKey; queryFn?: unknown }>(client: QueryClient, issueId: string, options: O): O {
  const fetch = options.queryFn as (context: never) => Promise<unknown>;
  return { ...options, queryFn: (context: never) => readIssue(client, issueId, options.queryKey, () => fetch(context)) };
}

/** People change rarely; the roster and the reader are read once a while, not per issue. */
export const peopleKeys = {
  members: (orgId: string) => ["observability", "people", "members", orgId] as const,
  me: () => ["observability", "people", "me"] as const,
};

/** The request of a disabled issue read (no scope yet): it is never sent. */
const NO_ISSUE_SCOPE: IssueScope = { orgId: "", envIds: [] };

/** A page of useIssues: the response, and the absolute range it read (the next page reads the same one). */
interface IssuePage {
  response: ListIssuesResponse;
  range: IssueTimeRangeInit;
}

interface IssuePageParam {
  token: string;
  range: IssuePage["range"];
}

const toIssuePages = (data: InfiniteData<IssuePage, IssuePageParam | null>): InfiniteData<IssueList, IssuePageParam | null> => ({
  pages: data.pages.map((page) => toIssueList(page.response)),
  pageParams: data.pageParams,
});

/**
 * The scope's issues, a page at a time. Page one sends the preset as a
 * relative window, so its request — and its generated key — stay put while
 * time moves; every later page sends the absolute range page one read
 * (ListIssuesResponse.effective_time_range) with the cursor, because the
 * token is only valid with the same request.
 *
 * Hand-written on the generated key and the scope's client, not
 * useListIssuesInfinite: a later page's request differs from page one beyond
 * its token, and one fetch also asks the probes below.
 */
export function useIssues(scope: IssueScope | null, filters: IssueFilters) {
  const input: ListIssuesInput = {
    ...(scope ?? NO_ISSUE_SCOPE),
    services: filters.service ? [filters.service] : [],
    window: lastOf(filters.range, "issues"),
    statuses: filters.statuses,
    platform: filters.platform,
    assignee: filters.assignee,
    userId: filters.userId,
    browser: filters.browser,
    os: filters.os,
    serviceVersion: filters.serviceVersion,
    sort: filters.sort,
    text: filters.text,
    pageSize: ISSUE_PAGE_SIZE,
  };
  const relative = wireTimeRange(input.window);
  return useInfiniteQuery({
    queryKey: issuesServiceKeys.listIssuesInfinite(listIssuesRequest(input)),
    queryFn: async (context): Promise<IssuePage> => {
      const { pageParam } = context;
      // Marked (poll, prefetch or a person's read) before the first await, where runAsPoll's mark holds.
      const call = obsCall({ signal: context.signal });
      const issues = issuesClient(context);
      const response = await issues.listIssues(listIssuesRequest(input, pageParam ?? undefined), call);
      // A control plane that does not echo the range it read: the next page asks for page one's range again.
      const range = response.effectiveTimeRange ?? pageParam?.range ?? relative;
      // An issue row names no user, so whether the server applied user_id is
      // asked, not inferred: the same first page for a user nobody is. A server
      // that knows the filter answers it with nothing; one that ignores it
      // answers with rows. The client and build filters are asked the same way.
      // Each probe asks about one filter alone, the other probed filters
      // removed: a server that applies user_id but ignores browser would answer
      // a combined probe with nothing (the random user matches no one) and pass
      // off a user-filtered list as a browser-filtered one. A probe reads the
      // range the list just read (its effective_time_range), not "the last
      // hour" resolved again: its answer must be about the rows on screen.
      const probed = pageParam ? [] : ISSUE_PROBED_FILTERS.filter(({ key }) => !!filters[key]);
      const unprobed = Object.fromEntries(ISSUE_PROBED_FILTERS.map(({ key }) => [key, undefined]));
      const probes = await Promise.all(
        probed.map(({ key }) => issues.listIssues(listIssuesRequest({ ...input, ...unprobed, [key]: issueFilterProbe(), pageSize: 1 }, { token: "", range }), call))
      );
      const ignored = probed.find((_, i) => probes[i].issues.length > 0);
      if (ignored) throw new FilterUnsupportedError(ignored.label, "issues");
      return { response, range };
    },
    initialPageParam: null as IssuePageParam | null,
    getNextPageParam: (last) => (last.response.nextPageToken ? { token: last.response.nextPageToken, range: last.range } : undefined),
    select: toIssuePages,
    enabled: !!scope,
    staleTime: 30_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
  });
}

/** One issue with its latest event, frames, versions and first release; counts over `range`. */
export function useIssue(issueId: string | undefined, range: RangePreset) {
  const client = useQueryClient();
  const id = issueId ?? "";
  return useQuery(guardedByIssue(client, id, issuesServiceQueries.getIssue(issueDetailRequest(id, range), {
    select: mapIssueDetail,
    enabled: !!issueId,
    staleTime: 30_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  })));
}

const toIssueEventPages = (data: InfiniteData<ListIssueEventsResponse, string>): InfiniteData<IssueEventList, string> => ({
  pages: data.pages.map(toIssueEventList),
  pageParams: data.pageParams,
});

/** One issue's events, newest first. */
export function useIssueEvents(issueId: string | undefined) {
  const client = useQueryClient();
  const id = issueId ?? "";
  return useInfiniteQuery(guardedByIssue(client, id, issuesServiceQueries.listIssueEventsInfinite(issueEventsRequest(id), {
    select: toIssueEventPages,
    enabled: !!issueId,
    staleTime: 30_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  })));
}

/**
 * A triage. It never retries and never re-sends: a stale version comes back
 * as an error (conflict) for the view to show, and every issue read is
 * re-fetched either way — on success to show the new state everywhere, on a
 * conflict to show the change the reader had not seen. The generated write
 * refreshes only on success, so its refresh is replaced by this one.
 */
export function useUpdateIssueStatus() {
  const queryClient = useQueryClient();
  return useMutation<Issue, unknown, UpdateIssueStatusRequest>({
    mutationFn: async (req) => toIssue(await issuesServiceMutations(queryClient).updateIssueStatus(wireStatusUpdate(req), { invalidate: false }), "UpdateIssueStatus"),
    onSettled: () => invalidateIssueTriage(queryClient),
  });
}

/** An issue's timeline, oldest first, a page at a time. A merged issue answers with the timeline of the issue it was merged into. */
export function useIssueTimeline(issueId: string | undefined) {
  // Discussion reads Postgres, not ClickHouse: quota pauses must not block it (it takes no read slot).
  const client = useQueryClient();
  const id = issueId ?? "";
  const select = useCallback(
    (data: InfiniteData<ListIssueTimelineResponse, string>): InfiniteData<IssueTimelineList, string> => ({
      pages: data.pages.map((page) => toIssueTimelineList(page, id)),
      pageParams: data.pageParams,
    }),
    [id]
  );
  return useInfiniteQuery(guardedByIssue(client, id, issuesServiceQueries.listIssueTimelineInfinite(issueTimelineRequest(id), {
    select,
    enabled: !!issueId,
    staleTime: 15_000,
    retry: issuesRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  })));
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

/**
 * Every write below never retries and never re-sends; a refusal is shown, and
 * the reads are re-fetched either way (onSettled, where the generated write's
 * own refresh runs on success only).
 */
export function useAddIssueComment() {
  const queryClient = useQueryClient();
  return useMutation<IssueComment, unknown, AddIssueCommentRequest>({
    mutationFn: async (req) => toComment(await issuesServiceMutations(queryClient).addIssueComment({ issueId: req.issueId, body: req.body }, { invalidate: false }), "AddIssueComment"),
    onSettled: () => invalidateIssueTimelines(queryClient),
  });
}

export function useUpdateIssueComment() {
  const queryClient = useQueryClient();
  return useMutation<IssueComment, unknown, UpdateIssueCommentRequest>({
    mutationFn: async (req) =>
      toComment(await issuesServiceMutations(queryClient).updateIssueComment({ issueId: req.issueId, commentId: req.commentId, body: req.body }, { invalidate: false }), "UpdateIssueComment"),
    onSettled: () => invalidateIssueTimelines(queryClient),
  });
}

export function useDeleteIssueComment() {
  const queryClient = useQueryClient();
  return useMutation<void, unknown, DeleteIssueCommentRequest>({
    mutationFn: async (req) => {
      await issuesServiceMutations(queryClient).deleteIssueComment({ issueId: req.issueId, commentId: req.commentId }, { invalidate: false });
    },
    onSettled: () => invalidateIssueTimelines(queryClient),
  });
}

/**
 * Assign or unassign. Like a triage it echoes the version read, never retries,
 * and on a stale one (Aborted: a conflict) re-reads the issue instead of
 * re-sending, so the reader sees what changed.
 */
export function useAssignIssue() {
  const queryClient = useQueryClient();
  return useMutation<Issue, unknown, AssignIssueRequest>({
    mutationFn: async (req) => toIssue(await issuesServiceMutations(queryClient).assignIssue(wireAssign(req), { invalidate: false }), "AssignIssue"),
    onSettled: () => invalidateIssueTriage(queryClient),
  });
}

/** Record a chat started from an issue. Idempotent per (issue, chat), so a repeat is harmless. */
export function useLinkIssueChat() {
  const queryClient = useQueryClient();
  return useMutation<LinkIssueChatResult, unknown, LinkIssueChatRequest>({
    mutationFn: (req) => linkIssueChat(queryClient, req),
    onSettled: () => invalidateIssueTimelines(queryClient),
  });
}

/** LinkIssueChat on `queryClient`'s transport (a TransportScope's, else the app's), with no hook: linkIssueChat.ts calls it after the view unmounted. */
export async function linkIssueChat(queryClient: QueryClient, req: LinkIssueChatRequest): Promise<LinkIssueChatResult> {
  return toLinkIssueChatResult(await issuesServiceMutations(queryClient).linkIssueChat(wireLinkIssueChat(req), { invalidate: false }));
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
