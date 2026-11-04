// Copyright (c) 2025 Reliant Labs

import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { useAddIssueComment, useIssueTimeline } from "@/hooks/observability-queries";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObsQuotaProvider, useObsQuota } from "@/services/controlPlane/observability/ObsQuotaContext";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";

// Exhaust the actual quota store BEFORE the timeline observer mounts.
function Paused({ children }: { children: ReactNode }) {
  const { store } = useObsQuota();
  useState(() => {
    store.report({ limit: "queries", lane: "interactive", resetsAtMs: Date.now() + 3_600_000, soft: false });
    return true;
  });
  return children;
}

function rig() {
  const api = createFixtureApi();
  const list = vi.spyOn(api, "listIssueTimeline");
  const add = vi.spyOn(api, "addIssueComment");
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>
        <ObsQuotaProvider><Paused>{children}</Paused></ObsQuotaProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
  const hook = renderHook(() => ({ timeline: useIssueTimeline("iss-orders-deadline"), add: useAddIssueComment(), quota: useObsQuota() }), { wrapper });
  return { ...hook, list, add };
}

describe("Postgres issue discussion while ClickHouse is paused", () => {
  it("calls the timeline RPC on mount and explicit refresh with the quota already exhausted", async () => {
    const r = rig();
    await waitFor(() => expect(r.result.current.timeline.isSuccess).toBe(true));
    expect(r.result.current.quota.store.getState().kind).toBe("exhausted");
    expect(r.list).toHaveBeenCalledTimes(1);
    expect(r.list).toHaveBeenCalledWith({ issueId: "iss-orders-deadline", pageSize: 100, pageToken: undefined });
    await act(async () => { await r.result.current.timeline.refetch(); });
    expect(r.list).toHaveBeenCalledTimes(2);
    expect(r.result.current.timeline.data?.pages[0].entries.length).toBeGreaterThan(0);
    expect(r.result.current.quota.store.getState().kind).toBe("exhausted");
  });

  it("refreshes the timeline after a successful comment even while ClickHouse stays paused", async () => {
    const r = rig();
    await waitFor(() => expect(r.result.current.timeline.isSuccess).toBe(true));
    const before = r.result.current.timeline.data!.pages[0].entries.length;
    await act(async () => {
      await r.result.current.add.mutateAsync({ issueId: "iss-orders-deadline", body: "Postgres discussion remains available" });
    });
    await waitFor(() => expect(r.result.current.timeline.data?.pages[0].entries).toHaveLength(before + 1));
    expect(r.add).toHaveBeenCalledTimes(1);
    expect(r.list).toHaveBeenCalledTimes(2);
    expect(r.result.current.timeline.data!.pages[0].entries.at(-1)?.body).toMatchObject({ kind: "comment", comment: { body: "Postgres discussion remains available" } });
    expect(r.result.current.quota.store.getState().kind).toBe("exhausted");
  });
});
