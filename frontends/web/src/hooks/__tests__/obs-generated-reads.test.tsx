// Copyright (c) 2025 Reliant Labs

/**
 * ObservabilityService's reads on generated hooks: the request names its window
 * RELATIVE to the server's clock (so the request, and its cache key, stay the
 * same as time moves and a refetch re-reads "the last hour" as of now), each
 * call is marked for the control-plane read stage at the moment it starts (a
 * poll reaches control-plane with Forge-Poll), and the shop fixture pack
 * resolves a relative window against its own clock.
 */

import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createRouterTransport, type HandlerContext } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { ObservabilityService, type GetServiceOverviewRequest, type ListWorkloadsRequest } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { observabilityServiceKeys } from "@/hooks/controlplane/observability-service-hooks_gen";
import { modelScope } from "@/fixtures/shop/handlers/observability";
import { windowOf } from "@/fixtures/shop/handlers/wire";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { relativeWindow, serviceOverviewRequest, wireTimeRange } from "@/services/controlPlane/observability/api";
import { obsReadInterceptor } from "@/services/controlPlane/observability/obsInterceptors";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { resetObsQuotaStore, runAsPoll } from "@/services/controlPlane/observability/quotaStore";
import { resetObsReadScheduler } from "@/services/controlPlane/observability/readScheduler";
import type { ObsScope } from "@/services/controlPlane/observability/types";

import { useServiceOverview, useWorkloads } from "../observability-queries";

const SCOPE: ObsScope = { orgId: "org", projectId: "shop", envIds: ["env-prod"], filters: [] };
const HOUR = 3_600_000;
const seconds = (d: { seconds: bigint } | undefined) => Number(d?.seconds ?? 0n);

interface Arrival {
  method: string;
  poll: boolean;
  request: GetServiceOverviewRequest | ListWorkloadsRequest;
}

function setup() {
  const arrivals: Arrival[] = [];
  const record = (method: string) => (request: GetServiceOverviewRequest | ListWorkloadsRequest, ctx: HandlerContext) => {
    arrivals.push({ method, poll: ctx.requestHeader.get("Forge-Poll") === "1", request });
    return {};
  };
  // control-plane behind the app's read stage, as on the app's transport.
  const transport = createRouterTransport(
    (router) => router.service(ObservabilityService, { getServiceOverview: record("getServiceOverview"), listWorkloads: record("listWorkloads") }),
    { transport: { interceptors: [obsReadInterceptor()] } }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const scope = createTransportScope({ id: "obs-generated-reads", transport, queryClient: client });
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

describe("ObservabilityService reads on generated hooks", () => {
  it("send the preset as a relative window, capped at what the read keeps, and no absolute bounds", async () => {
    const { arrivals, wrapper } = setup();
    renderHook(() => useServiceOverview(SCOPE, "1h"), { wrapper });
    renderHook(() => useWorkloads(SCOPE, "30d"), { wrapper });
    await waitFor(() => expect(arrivals).toHaveLength(2));
    for (const { request } of arrivals) {
      expect(request.scope?.timeRange?.start).toBeUndefined();
      expect(request.scope?.timeRange?.end).toBeUndefined();
      expect(request.scope).toMatchObject({ orgId: "org", projectId: "shop", envIds: ["env-prod"] });
    }
    expect(seconds(arrivals.find((a) => a.method === "getServiceOverview")!.request.scope?.timeRange?.relative?.last)).toBe(HOUR / 1000);
    // Workloads are kept 7 days: "Last 30 days" asks for the 7 there are.
    expect(seconds(arrivals.find((a) => a.method === "listWorkloads")!.request.scope?.timeRange?.relative?.last)).toBe((7 * 24 * HOUR) / 1000);
  });

  it("keep one cache key as time moves, so a refetch re-reads the same entry", () => {
    const at = (now: number) => {
      const real = Date.now;
      Date.now = () => now;
      try {
        return observabilityServiceKeys.getServiceOverview(serviceOverviewRequest({ ...SCOPE, window: relativeWindow(HOUR) }));
      } finally {
        Date.now = real;
      }
    };
    expect(at(1_000)).toEqual(at(1_000 + 5 * HOUR));
  });

  it("mark a read made inside a poll with Forge-Poll, and a person's read without it", async () => {
    const { arrivals, client, wrapper } = setup();
    renderHook(() => useServiceOverview(SCOPE, "1h"), { wrapper });
    await waitFor(() => expect(arrivals).toHaveLength(1));
    expect(arrivals[0]!.poll).toBe(false);

    const key = observabilityServiceKeys.getServiceOverview(serviceOverviewRequest({ ...SCOPE, window: relativeWindow(HOUR), services: [], groupBy: [] }));
    await act(async () => runAsPoll(() => void client.refetchQueries({ queryKey: key, exact: true })));
    await waitFor(() => expect(arrivals).toHaveLength(2));
    expect(arrivals[1]!.poll).toBe(true);
  });
});

describe("the shop pack's handlers", () => {
  it("resolve a relative window against the pack's clock", () => {
    const now = Date.UTC(2026, 10, 10, 15, 0, 0);
    expect(windowOf(wireTimeRange(relativeWindow(HOUR)) as never, now)).toEqual({ startMs: now - HOUR, endMs: now });
    expect(windowOf(wireTimeRange(relativeWindow(HOUR, 2 * HOUR)) as never, now)).toEqual({ startMs: now - 3 * HOUR, endMs: now - 2 * HOUR });
    expect(windowOf(wireTimeRange({ startMs: 5, endMs: 9 }) as never, now)).toEqual({ startMs: 5, endMs: 9 });
    const scope = modelScope({ orgId: "org", envIds: [], services: [], filters: [], timeRange: wireTimeRange(relativeWindow(6 * HOUR)) } as never, now);
    expect(scope.window).toEqual({ startMs: now - 6 * HOUR, endMs: now });
  });
});
