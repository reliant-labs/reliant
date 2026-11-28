// Copyright (c) 2025 Reliant Labs

/**
 * ObservabilityService's panel, graph and paged reads (PR 1b of the hooks
 * migration): QueryPanel on its generated hook carries the dashboard's
 * Forge-Panel label in a header and a comparison as `ends_ago` (or, before a
 * release, absolute bounds ending at it); the graph reads send their window
 * as given; and paged logs pin absolute bounds at fetch time, asking for later
 * pages with the first response's effective_time_range and the cursor. The
 * generated read's relative-preset key still lets Refresh (invalidateObsScope)
 * reach the query.
 */

import type { ReactNode } from "react";
import { create } from "@bufbuild/protobuf";
import { QueryClient } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createRouterTransport, type HandlerContext } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ObservabilityService,
  QueryPanelResponseSchema,
  SearchLogsResponseSchema,
  type GetServiceGraphRequest,
  type QueryPanelRequest,
  type SearchLogsRequest,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import { observabilityServiceKeys } from "@/hooks/controlplane/observability-service-hooks_gen";
import { ts } from "@/fixtures/shop/handlers/wire";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { searchLogsRequest } from "@/services/controlPlane/observability/api";
import { obsReadInterceptor } from "@/services/controlPlane/observability/obsInterceptors";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { resetObsQuotaStore } from "@/services/controlPlane/observability/quotaStore";
import { resetObsReadScheduler } from "@/services/controlPlane/observability/readScheduler";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";

import { invalidateObsScope, panelQueryKey, useSearchLogs, useServiceGraph, usePanelQuery, type LogFilters } from "../observability-queries";

const SCOPE: ObsScope = { orgId: "org", projectId: "shop", envIds: ["env-prod"], filters: [] };
const HOUR = 3_600_000;
const seconds = (d: { seconds: bigint } | undefined) => Number(d?.seconds ?? 0n);
const PANEL: Panel = { id: "requests", title: "Requests", kind: "number", signal: "traces", series: [{ label: "requests", agg: "count" }] };
const FILTERS: LogFilters = { range: "1h", query: "", services: [], severities: [] };
/** The window the server resolved for the logs' first page. */
const RESOLVED = { startMs: Date.UTC(2026, 10, 10, 14), endMs: Date.UTC(2026, 10, 10, 15) };

interface Arrival {
  method: string;
  panel: string | null;
  request: QueryPanelRequest | SearchLogsRequest | GetServiceGraphRequest;
}

function setup() {
  const arrivals: Arrival[] = [];
  const note = (method: string, request: Arrival["request"], ctx: HandlerContext) => arrivals.push({ method, panel: ctx.requestHeader.get("Forge-Panel"), request });
  const transport = createRouterTransport(
    (router) =>
      router.service(ObservabilityService, {
        queryPanel: (request, ctx) => {
          note("queryPanel", request, ctx);
          return create(QueryPanelResponseSchema, { kind: "number" });
        },
        getServiceGraph: (request, ctx) => {
          note("getServiceGraph", request, ctx);
          return {};
        },
        searchLogs: (request, ctx) => {
          note("searchLogs", request, ctx);
          const first = !request.pageToken;
          return create(SearchLogsResponseSchema, {
            logs: [{ body: first ? "newest" : "older", timestamp: ts(RESOLVED.endMs - (first ? 1 : 2)) }],
            nextPageToken: first ? "page-2" : "",
            // Echoed for the first page only, as a server resolves it once.
            effectiveTimeRange: first ? { start: ts(RESOLVED.startMs), end: ts(RESOLVED.endMs) } : undefined,
          });
        },
      }),
    { transport: { interceptors: [obsReadInterceptor()] } }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const scope = createTransportScope({ id: "obs-paged-panel-reads", transport, queryClient: client });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TransportScopeProvider scope={scope}>
      <ObsQuotaProvider>{children}</ObsQuotaProvider>
    </TransportScopeProvider>
  );
  return { arrivals, client, wrapper };
}

beforeEach(() => {
  resetObsQuotaStore();
  resetObsReadScheduler();
});
afterEach(() => {
  resetObsReadScheduler();
  vi.restoreAllMocks();
});

describe("QueryPanel on its generated hook", () => {
  it("shares identical reads only when their Forge-Panel attribution matches", async () => {
    const { arrivals, client, wrapper } = setup();
    const { result } = renderHook(
      () => [
        usePanelQuery(SCOPE, "1h", PANEL, [], [], { dashboardId: "overview" }),
        usePanelQuery(SCOPE, "1h", PANEL, [], [], { dashboardId: "traffic" }),
        usePanelQuery(SCOPE, "1h", { ...PANEL, title: "Same computation" }, [], [], { dashboardId: "overview" }),
        usePanelQuery(SCOPE, "1h", PANEL, []),
      ],
      { wrapper }
    );
    await waitFor(() => expect(result.current.every((query) => query.isSuccess)).toBe(true));
    expect(arrivals.map((a) => a.panel).sort()).toEqual([null, "overview/requests", "traffic/requests"]);
    expect(arrivals.every((a) => a.method === "queryPanel")).toBe(true);
    for (const arrival of arrivals) expect(arrival.request).toEqual(arrivals[0]!.request);

    // Exact lookups (such as the drawer's) and scoped refresh must still find the reads.
    for (const dashboardId of ["overview", "traffic", undefined]) {
      const key = panelQueryKey(SCOPE, "1h", PANEL, [], [], undefined, dashboardId);
      expect(client.getQueryData(key)).toMatchObject({ kind: "number" });
    }
    await act(async () => void (await invalidateObsScope(client, SCOPE)));
    expect(arrivals.slice(3).map((a) => a.panel).sort()).toEqual([null, "overview/requests", "traffic/requests"]);
  });

  it("labels a dashboard's read with Forge-Panel, sends a previous period as ends_ago, and a release comparison as bounds ending at it", async () => {
    const { arrivals, wrapper } = setup();
    const promotedAtMs = Date.UTC(2026, 10, 9, 12);
    const { result } = renderHook(
      () => [
        usePanelQuery(SCOPE, "1h", PANEL, [], [], { dashboardId: "overview" }),
        usePanelQuery(SCOPE, "1h", PANEL, [], [], { offset: { endsAgoMs: HOUR } }),
        usePanelQuery(SCOPE, "1h", PANEL, [], [], { offset: { endsAtMs: promotedAtMs } }),
      ],
      { wrapper }
    );
    await waitFor(() => expect(arrivals).toHaveLength(3));
    const [labelled, previous, release] = [0, 1, 2].map((i) => arrivals[i]!);
    expect(labelled.panel).toBe("overview/requests");
    expect(previous.panel).toBeNull();
    const range = (a: Arrival) => (a.request as QueryPanelRequest).scope?.timeRange;
    expect(seconds(range(labelled)?.relative?.last)).toBe(HOUR / 1000);
    expect(range(labelled)?.relative?.endsAgo).toBeUndefined();
    expect(seconds(range(previous)?.relative?.endsAgo)).toBe(HOUR / 1000);
    expect(range(release)?.relative).toBeUndefined();
    expect(Number(range(release)?.end?.seconds) * 1000).toBe(promotedAtMs);
    expect(Number(range(release)?.start?.seconds) * 1000).toBe(promotedAtMs - HOUR);
    // Presentation is not sent; the id is.
    expect((labelled.request as QueryPanelRequest).panel).toMatchObject({ id: "requests", title: "" });
    // The answer carries the window it covers, resolved here for a relative read.
    await waitFor(() => expect(result.current[2]!.data?.window).toEqual({ startMs: promotedAtMs - HOUR, endMs: promotedAtMs }));
    await waitFor(() => expect(result.current[0]!.data).toBeDefined());
    const labelledWindow = result.current[0]!.data!.window!;
    expect(labelledWindow.endMs - labelledWindow.startMs).toBe(HOUR);
  });
});

describe("the graph reads", () => {
  it("send the window they are given: relative for the overview's, absolute for the graph page's frozen snapshot", async () => {
    const { arrivals, wrapper } = setup();
    const filter = { rpcIds: [], rpcService: "", services: [], includeInternal: false };
    renderHook(() => useServiceGraph(SCOPE, { lastMs: HOUR }, filter, { staleTime: 60_000 }), { wrapper });
    renderHook(() => useServiceGraph(SCOPE, RESOLVED, filter, { staleTime: Infinity }), { wrapper });
    await waitFor(() => expect(arrivals).toHaveLength(2));
    const ranges = arrivals.map((a) => (a.request as GetServiceGraphRequest).scope?.timeRange);
    expect(ranges.some((r) => seconds(r?.relative?.last) === HOUR / 1000 && !r?.start)).toBe(true);
    expect(ranges.some((r) => Number(r?.end?.seconds) * 1000 === RESOLVED.endMs && !r?.relative)).toBe(true);
  });
});

describe("the paged logs read", () => {
  it("pins page one and sends its effective response window and cursor for page two", async () => {
    const now = RESOLVED.endMs + HOUR;
    const clock = vi.spyOn(Date, "now").mockReturnValue(now);
    const { arrivals, client, wrapper } = setup();
    const { result } = renderHook(() => useSearchLogs(SCOPE, FILTERS, false), { wrapper });
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(1));
    const first = arrivals[0]!.request as SearchLogsRequest;
    expect(first.scope?.timeRange?.relative).toBeUndefined();
    expect(seconds(first.scope?.timeRange?.start) * 1000).toBe(now - HOUR);
    expect(seconds(first.scope?.timeRange?.end) * 1000).toBe(now);
    expect(first.pageToken).toBe("");
    expect(result.current.data!.pages[0]!.window).toEqual(RESOLVED);

    await act(async () => void (await result.current.fetchNextPage()));
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
    const second = arrivals[1]!.request as SearchLogsRequest;
    expect(second.pageToken).toBe("page-2");
    expect(second.scope?.timeRange?.relative).toBeUndefined();
    expect(Number(second.scope?.timeRange?.start?.seconds) * 1000).toBe(RESOLVED.startMs);
    expect(Number(second.scope?.timeRange?.end?.seconds) * 1000).toBe(RESOLVED.endMs);
    expect(second.scope).toMatchObject({ orgId: "org", projectId: "shop", envIds: ["env-prod"] });
    expect(result.current.data!.pages.map((p) => p.records[0]!.body)).toEqual(["newest", "older"]);

    // The generated paged key stays relative even though the wire read is pinned.
    const keys = client.getQueryCache().getAll().map((q) => q.queryKey);
    expect(keys).toEqual([observabilityServiceKeys.searchLogsInfinite(searchLogsRequest({
      ...SCOPE, ...FILTERS, window: { lastMs: HOUR }, limit: first.pageSize,
    }))]);
    const refreshedAt = now + 10_000;
    clock.mockReturnValue(refreshedAt);
    await act(async () => void (await invalidateObsScope(client, SCOPE)));
    expect(arrivals).toHaveLength(4);
    const refreshedFirst = arrivals[2]!.request as SearchLogsRequest;
    expect(refreshedFirst.pageToken).toBe("");
    expect(seconds(refreshedFirst.scope?.timeRange?.start) * 1000).toBe(refreshedAt - HOUR);
    expect(seconds(refreshedFirst.scope?.timeRange?.end) * 1000).toBe(refreshedAt);
    expect(arrivals[3]!.request).toEqual(second);
  });
});
