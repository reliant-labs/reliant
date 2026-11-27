// Copyright (c) 2025 Reliant Labs

/**
 * AlertingService on its generated hooks: the history names a LOOKBACK the
 * server resolves against its own clock (so its request, and cache key, stay
 * the same as time moves), every read and write runs on its QueryClient's
 * transport, and a write re-reads only its own org's alerting.
 */

import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { AlertingService, type ListAlertHistoryRequest } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { alertingServiceKeys, useExpireSilence } from "@/hooks/controlplane/alerting-service-hooks_gen";
import { alertingHandlers } from "@/fixtures/shop/handlers/alerting";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import type { MockHandlers } from "@/lib/test-utils";
import { listAlertHistoryRequest } from "@/services/controlPlane/observability/alertingApi";
import { createShopApi } from "@/fixtures/shop/model/shopApi";
import { demoNow } from "@/fixtures/shop/shopStory";

import { invalidateOrgAlerting, useAlertHistory, useAlertingWriteOptions } from "../observability-queries";

const HOUR = 3_600_000;
const seconds = (d: { seconds: bigint } | undefined) => Number(d?.seconds ?? 0n);

function setup() {
  const histories: ListAlertHistoryRequest[] = [];
  const expired: string[] = [];
  const transport = createRouterTransport((router) =>
    router.service(AlertingService, {
      listAlertHistory: (req) => {
        histories.push(req);
        return { events: [] };
      },
      expireSilence: (req) => {
        expired.push(`${req.orgId}/${req.id}`);
        return {};
      },
    })
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const scope = createTransportScope({ id: "alerting-queries", transport, queryClient: client });
  const wrapper = ({ children }: { children: ReactNode }) => <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
  return { histories, expired, client, wrapper };
}

describe("useAlertHistory", () => {
  it("sends the range as a lookback and no since", async () => {
    const { histories, wrapper } = setup();
    renderHook(() => useAlertHistory("org-1", "24h", "env-prod"), { wrapper });
    await waitFor(() => expect(histories).toHaveLength(1));
    expect(histories[0]).toMatchObject({ orgId: "org-1", envId: "env-prod", limit: 500 });
    expect(histories[0]!.since).toBeUndefined();
    expect(seconds(histories[0]!.lookback)).toBe((24 * HOUR) / 1000);
  });

  it("keeps one cache key as time moves", () => {
    const at = (now: number) => {
      const real = Date.now;
      Date.now = () => now;
      try {
        return alertingServiceKeys.listAlertHistory(listAlertHistoryRequest("org-1", { lookbackMs: HOUR, limit: 500 }));
      } finally {
        Date.now = real;
      }
    };
    expect(at(1_000)).toEqual(at(1_000 + 5 * HOUR));
  });
});

describe("alerting writes", () => {
  it("re-read only the org's alerting", async () => {
    const client = new QueryClient();
    const mine = alertingServiceKeys.listReceivers({ orgId: "org-1" });
    const theirs = alertingServiceKeys.listReceivers({ orgId: "org-2" });
    client.setQueryData(mine, { receivers: [] });
    client.setQueryData(theirs, { receivers: [] });
    await invalidateOrgAlerting(client, "org-1");
    expect(client.getQueryState(mine)?.isInvalidated).toBe(true);
    expect(client.getQueryState(theirs)?.isInvalidated).toBe(false);
  });

  it("run on the scope's transport, and re-read the org's alerting once settled", async () => {
    const { expired, client, wrapper } = setup();
    const mine = alertingServiceKeys.listSilences({ orgId: "org-1" });
    client.setQueryData(mine, { silences: [] });
    const { result } = renderHook(() => useExpireSilence(useAlertingWriteOptions("org-1")), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ orgId: "org-1", id: "sil-1" });
    });
    expect(expired).toEqual(["org-1/sil-1"]);
    await waitFor(() => expect(client.getQueryState(mine)?.isInvalidated).toBe(true));
  });
});

describe("the shop pack's handler", () => {
  it("resolves a lookback against the pack's clock, and a since wins over it", async () => {
    const read = (now: number, req: Parameters<typeof listAlertHistoryRequest>[1]) => {
      const handlers = alertingHandlers(createShopApi(), { clock: () => now }) as MockHandlers;
      const listAlertHistory = handlers[`${AlertingService.typeName}/ListAlertHistory`] as (r: unknown) => Promise<{ events: { at?: { seconds: bigint } }[] }>;
      return listAlertHistory(listAlertHistoryRequest("org-acme", req));
    };
    const now = demoNow();
    const day = await read(now, { lookbackMs: 24 * HOUR });
    expect(day.events.length).toBeGreaterThan(0);
    expect(day.events.every((e) => seconds(e.at) * 1000 >= now - 24 * HOUR - 1000)).toBe(true);
    // A month on, the same request's last day holds none of it.
    expect((await read(now + 30 * 24 * HOUR, { lookbackMs: 24 * HOUR })).events).toHaveLength(0);
    // A since is a fixed time, whatever the lookback and the clock say.
    expect((await read(now + 30 * 24 * HOUR, { sinceMs: now - 24 * HOUR, lookbackMs: HOUR })).events).toHaveLength(day.events.length);
  });
});
