// Copyright (c) 2025 Reliant Labs

/**
 * RumService's reads on generated hooks: the session list names its window
 * RELATIVE to the server's clock (so its request, and cache key, stay the same
 * as time moves), a filter the server did not apply fails the fetch, the
 * replay evidence pages a relative window by sending back the range page one
 * read (effective_time_range), each call is marked for the control-plane read
 * stage when it starts, and the shop pack resolves a relative window against
 * its own clock and answers the range it read.
 */

import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createRouterTransport, type HandlerContext } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { ListSessionsRequestSchema, RumService, type ListSessionsRequest } from "@/gen/controlplane/services/rum/v1/rum_pb";
import { rumServiceKeys } from "@/hooks/controlplane/rum-service-hooks_gen";
import { rumHandlers } from "@/fixtures/shop/handlers/rum";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { relativeWindow, wireTimeRange } from "@/services/controlPlane/observability/api";
import { FilterUnsupportedError } from "@/services/controlPlane/observability/errors";
import { obsReadInterceptor } from "@/services/controlPlane/observability/obsInterceptors";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { resetObsQuotaStore, runAsPoll } from "@/services/controlPlane/observability/quotaStore";
import { resetObsReadScheduler } from "@/services/controlPlane/observability/readScheduler";
import { listSessionsRequest } from "@/services/controlPlane/observability/rumApi";
import type { RumScope } from "@/services/controlPlane/observability/rumTypes";
import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";

import { REPLAY_EVIDENCE_PAGE_SIZE, SESSION_PAGE_SIZE, useReplayEvidence, useSessions, type SessionFilters } from "../observability-session-queries";

const SCOPE: RumScope = { orgId: "org", projectId: "shop", envIds: ["env-prod"] };
const HOUR = 3_600_000;
const FILTERS: SessionFilters = { range: "24h", hasError: false, userId: "", pathContains: "" };
const seconds = (d: { seconds: bigint } | undefined) => Number(d?.seconds ?? 0n);
const ts = (ms: number) => create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });

interface Arrival {
  poll: boolean;
  request: ListSessionsRequest;
}

/** control-plane's ListSessions behind the app's read stage; `answer` says what each page returns. */
function setup(answer: (request: ListSessionsRequest, page: number) => object = () => ({})) {
  const arrivals: Arrival[] = [];
  const transport = createRouterTransport(
    (router) =>
      router.service(RumService, {
        listSessions: (request: ListSessionsRequest, ctx: HandlerContext) => {
          arrivals.push({ poll: ctx.requestHeader.get("Forge-Poll") === "1", request });
          return answer(request, arrivals.length);
        },
      }),
    { transport: { interceptors: [obsReadInterceptor()] } }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const scope = createTransportScope({ id: "rum-generated-reads", transport, queryClient: client });
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
});

describe("the session list on its generated hook", () => {
  it("sends the preset as a relative window, the filters and the page token, and no absolute bounds", async () => {
    const { arrivals, wrapper } = setup();
    renderHook(() => useSessions(SCOPE, { ...FILTERS, service: "shop-web", pathContains: "/cart", pageToken: "p2" }), { wrapper });
    await waitFor(() => expect(arrivals).toHaveLength(1));
    const { request } = arrivals[0]!;
    expect(request.scope?.timeRange?.start).toBeUndefined();
    expect(request.scope?.timeRange?.end).toBeUndefined();
    expect(seconds(request.scope?.timeRange?.relative?.last)).toBe((24 * HOUR) / 1000);
    expect(request.scope).toMatchObject({ orgId: "org", projectId: "shop", envIds: ["env-prod"], services: ["shop-web"] });
    expect(request).toMatchObject({ hasReplay: true, pathContains: "/cart", pageSize: SESSION_PAGE_SIZE, pageToken: "p2" });
  });

  it("keeps one cache key as time moves", () => {
    const at = (now: number) => {
      const real = Date.now;
      Date.now = () => now;
      try {
        return rumServiceKeys.listSessions(listSessionsRequest({ ...SCOPE, window: relativeWindow(HOUR), hasError: false, hasReplay: true, userId: "", pathContains: "" }));
      } finally {
        Date.now = real;
      }
    };
    expect(at(1_000)).toEqual(at(1_000 + 5 * HOUR));
  });

  it("fails the fetch when a row does not carry a requested filter, with no rows on screen", async () => {
    const { wrapper } = setup(() => ({ sessions: [{ sessionId: "s1", userId: "someone-else" }] }));
    const { result } = renderHook(() => useSessions(SCOPE, { ...FILTERS, userId: "u-1" }), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toBeInstanceOf(FilterUnsupportedError);
    expect(result.current.data).toBeUndefined();
  });

  it("marks a read made inside a poll with Forge-Poll, and a person's read without it", async () => {
    const { arrivals, client, wrapper } = setup();
    renderHook(() => useSessions(SCOPE, FILTERS), { wrapper });
    await waitFor(() => expect(arrivals).toHaveLength(1));
    expect(arrivals[0]!.poll).toBe(false);
    await act(async () => runAsPoll(() => void client.refetchQueries({ queryKey: rumServiceKeys.listSessions.all })));
    await waitFor(() => expect(arrivals).toHaveLength(2));
    expect(arrivals[1]!.poll).toBe(true);
  });
});

describe("the replay evidence", () => {
  it("sends page one relative, then the range page one read with each token", async () => {
    const effective = { start: ts(1_000_000), end: ts(1_000_000 + 7 * 24 * HOUR) };
    const { arrivals, wrapper } = setup((_, page) => ({ sessions: [], nextPageToken: page < 3 ? `t:${page}` : "", effectiveTimeRange: effective }));
    const { result } = renderHook(() => useReplayEvidence(SCOPE, ["env-staging"]), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data!.complete).toBe(true);
    expect(arrivals.map((a) => a.request.pageToken)).toEqual(["", "t:1", "t:2"]);
    const [first, ...rest] = arrivals.map((a) => a.request);
    expect(seconds(first!.scope?.timeRange?.relative?.last)).toBe((7 * 24 * HOUR) / 1000);
    expect(first!.scope?.timeRange?.start).toBeUndefined();
    expect(first).toMatchObject({ hasReplay: true, pageSize: REPLAY_EVIDENCE_PAGE_SIZE, scope: { envIds: ["env-staging"] } });
    for (const page of rest) {
      expect(page.scope?.timeRange?.relative).toBeUndefined();
      expect(page.scope?.timeRange?.start).toEqual(effective.start);
      expect(page.scope?.timeRange?.end).toEqual(effective.end);
      expect(page.scope?.envIds).toEqual(["env-staging"]);
    }
  });
});

describe("the shop pack's RumService handler", () => {
  it("resolves a relative window against the pack's clock and answers the range it read", async () => {
    const now = Date.UTC(2026, 10, 10, 15, 0, 0);
    const windows: { startMs: number; endMs: number }[] = [];
    const api = createFixtureApi();
    const handlers = rumHandlers({ ...api, listSessions: async (req) => (windows.push(req.window), { sessions: [], nextPageToken: "" }) }, { clock: () => now });
    const request = create(ListSessionsRequestSchema, { scope: { orgId: "org", envIds: [], services: [], timeRange: wireTimeRange(relativeWindow(6 * HOUR)) } });
    const res = (await handlers[`${RumService.typeName}/ListSessions`]!(request)) as { effectiveTimeRange?: { start?: { seconds: bigint }; end?: { seconds: bigint } } };
    expect(windows).toEqual([{ startMs: now - 6 * HOUR, endMs: now }]);
    expect(seconds(res.effectiveTimeRange?.start) * 1000).toBe(now - 6 * HOUR);
    expect(seconds(res.effectiveTimeRange?.end) * 1000).toBe(now);
  });
});
