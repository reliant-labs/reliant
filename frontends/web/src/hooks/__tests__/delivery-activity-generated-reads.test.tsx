// Copyright (c) 2025 Reliant Labs

/**
 * DeliveryInsightsService's and ActivityService's reads on generated hooks:
 * the ledger and DORA name their window RELATIVE to the server's clock (so the
 * request and its cache key stay put as time moves), the activity feed pages
 * by token on its request's key, every read goes to the QueryClient's
 * TransportScope (never the app's transport), the panel's Refresh roots reach
 * them, and the shop pack's delivery handlers resolve a relative window
 * against the clock they are given.
 */

import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createClient, createRouterTransport, type Transport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { ActivityActorKind, ActivityService, type ListActivityRequest } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { DeliveryInsightsService, type GetDoraMetricsRequest, type ListReleasesRequest } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import { deliveryInsightsServiceKeys } from "@/hooks/controlplane/delivery-insights-service-hooks_gen";
import { deliveryHandlers } from "@/fixtures/shop/handlers/delivery";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { relativeWindow } from "@/services/controlPlane/observability/api";
import { releasesRequest } from "@/services/controlPlane/observability/deliveryApi";
import type { DeliveryScope } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { activityKeys, deliveryKeys, useActivity, useDoraMetrics, useReleases } from "../observability-queries";

const SCOPE: DeliveryScope = { orgId: "org", projectId: "shop", envIds: ["env-prod"] };
const DAY = 86_400_000;
const seconds = (d: { seconds: bigint } | undefined) => Number(d?.seconds ?? 0n);

function setup() {
  const releases: ListReleasesRequest[] = [];
  const dora: GetDoraMetricsRequest[] = [];
  const activity: ListActivityRequest[] = [];
  const transport = createRouterTransport((router) => {
    router.service(DeliveryInsightsService, {
      listReleases: (req) => {
        releases.push(req);
        return { releases: [{ promotionId: "p1", environmentId: "env-prod", environmentName: "prod", version: "v1.2.0", kind: "promote" }], truncated: true };
      },
      getDoraMetrics: (req) => {
        dora.push(req);
        return { failureWindowHours: 24, environmentCount: 1, deploymentFrequency: { deployments: 3, perDay: 0.1 } };
      },
    });
    router.service(ActivityService, {
      listActivity: (req) => {
        activity.push(req);
        const page = req.pageToken === "" ? 1 : 2;
        return {
          events: [{ id: `e${page}`, verb: "promote", actor: { id: "u1", name: "Maya", kind: ActivityActorKind.USER }, target: { kind: "release", id: "v1.2.0" } }],
          nextPageToken: page === 1 ? "p2" : "",
        };
      },
    });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const scope = createTransportScope({ id: "delivery-activity-reads", transport, queryClient: client });
  const wrapper = ({ children }: { children: ReactNode }) => <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
  return { releases, dora, activity, client, wrapper };
}

// Inside a scope, nothing may reach the app's transport.
const appCalls: string[] = [];
const appTransport: Transport = {
  unary: (method) => {
    appCalls.push(`${method.parent.typeName}/${method.name}`);
    return Promise.reject(new Error("the app's transport was called inside a scope"));
  },
  stream: (method) => {
    appCalls.push(`${method.parent.typeName}/${method.name}`);
    return Promise.reject(new Error("the app's transport was called inside a scope"));
  },
};
beforeEach(() => setTransport(appTransport));
afterEach(() => {
  setTransport(null);
  appCalls.length = 0;
});

describe("DeliveryInsightsService reads on generated hooks", () => {
  it("send the window relative to the server's clock, with no absolute bounds, and map in select", async () => {
    const { releases, dora, wrapper } = setup();
    const ledger = renderHook(() => useReleases(SCOPE, 30 * DAY, { limit: 2000 }), { wrapper });
    const metrics = renderHook(() => useDoraMetrics(SCOPE, 7, 24), { wrapper });
    await waitFor(() => expect(ledger.result.current.isSuccess && metrics.result.current.isSuccess).toBe(true));

    expect(releases).toHaveLength(1);
    expect(releases[0]!.scope?.timeRange?.start).toBeUndefined();
    expect(releases[0]!.scope?.timeRange?.end).toBeUndefined();
    expect(seconds(releases[0]!.scope?.timeRange?.relative?.last)).toBe((30 * DAY) / 1000);
    expect(releases[0]!.scope).toMatchObject({ orgId: "org", projectId: "shop", envIds: ["env-prod"] });
    // RELEASES_LIMIT.max caps the asked-for limit, as before.
    expect(releases[0]!.limit).toBeLessThanOrEqual(2000);
    expect(seconds(dora[0]!.scope?.timeRange?.relative?.last)).toBe((7 * DAY) / 1000);
    expect(dora[0]!.failureWindowHours).toBe(24);

    expect(ledger.result.current.data).toEqual({ promotions: [expect.objectContaining({ promotionId: "p1", envId: "env-prod", envName: "prod", version: "v1.2.0" })], truncated: true });
    expect(metrics.result.current.data?.deploymentFrequency).toMatchObject({ deployments: 3, perDay: 0.1 });
    expect(appCalls).toEqual([]);
  });

  it("keep one cache key as time moves", () => {
    const at = (now: number) => {
      const real = Date.now;
      Date.now = () => now;
      try {
        return deliveryInsightsServiceKeys.listReleases(releasesRequest({ ...SCOPE, window: relativeWindow(30 * DAY) }));
      } finally {
        Date.now = real;
      }
    };
    expect(at(1_000)).toEqual(at(1_000 + 5 * DAY));
  });

  it("send nothing without a scope", async () => {
    const { releases, dora, wrapper } = setup();
    renderHook(() => useReleases(null, DAY), { wrapper });
    renderHook(() => useDoraMetrics(null, 30, 24), { wrapper });
    await new Promise((r) => setTimeout(r, 20));
    expect(releases).toEqual([]);
    expect(dora).toEqual([]);
  });

  it("are re-read by the panel's Refresh root", async () => {
    const { releases, client, wrapper } = setup();
    renderHook(() => useReleases(SCOPE, DAY), { wrapper });
    await waitFor(() => expect(releases).toHaveLength(1));
    await client.invalidateQueries({ queryKey: deliveryKeys.root() });
    await waitFor(() => expect(releases).toHaveLength(2));
  });
});

describe("ActivityService's feed on its generated infinite hook", () => {
  it("pages by token on the scope and target it names, mapped in select, and the activity root re-reads it", async () => {
    const { activity, client, wrapper } = setup();
    const feed = renderHook(() => useActivity(SCOPE, { kind: "issue", id: "iss-1" }), { wrapper });
    await waitFor(() => expect(feed.result.current.isSuccess).toBe(true));
    expect(feed.result.current.data?.pages[0]).toEqual({
      events: [expect.objectContaining({ id: "e1", verb: "promote", actor: { id: "u1", name: "Maya", kind: "user" }, target: { kind: "release", id: "v1.2.0", label: "v1.2.0" }, env: null })],
      nextPageToken: "p2",
    });
    await feed.result.current.fetchNextPage();
    await waitFor(() => expect(feed.result.current.data?.pages).toHaveLength(2));
    expect(activity.map((r) => [r.pageToken, r.pageSize, r.targetKind, r.targetId, r.scope?.orgId, r.scope?.projectId])).toEqual([
      ["", 50, "issue", "iss-1", "org", "shop"],
      ["p2", 50, "issue", "iss-1", "org", "shop"],
    ]);
    expect(feed.result.current.hasNextPage).toBe(false);

    await client.invalidateQueries({ queryKey: activityKeys.root() });
    await waitFor(() => expect(activity.length).toBeGreaterThan(2));
    expect(appCalls).toEqual([]);
  });
});

describe("the shop pack's delivery handlers", () => {
  it("resolve a relative window against the clock they are given", async () => {
    const now = Date.UTC(2026, 10, 10, 15, 0, 0);
    const seen: { startMs: number; endMs: number }[] = [];
    const fixtures = {
      listReleases: async (req: { window: { startMs: number; endMs: number } }) => {
        seen.push(req.window);
        return { promotions: [], truncated: false };
      },
    } as unknown as ObservabilityApi;
    const client = createClient(DeliveryInsightsService, mockTransport(deliveryHandlers(fixtures, { now: () => now }) as never));
    await client.listReleases(releasesRequest({ ...SCOPE, window: relativeWindow(DAY) }));
    expect(seen).toEqual([{ startMs: now - DAY, endMs: now }]);
  });
});
