import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import { QueryPanelResponseSchema } from "@/gen/controlplane/services/observability/v1/observability_pb";
import type { PanelResult } from "@/services/controlPlane/observability/types";
import { readPanelWithWindow } from "./observability-queries";

describe("panel response window", () => {
  it("retains the fetched window while a stale relative query refetches", async () => {
    const clock = vi.spyOn(Date, "now").mockReturnValue(10_000_000);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const response = create(QueryPanelResponseSchema, { kind: "line" });
    let complete: ((value: typeof response) => void) | undefined;
    let calls = 0;
    const queryFn = () => readPanelWithWindow(
      () => ++calls === 1 ? Promise.resolve(response) : new Promise<typeof response>((resolve) => { complete = resolve; }),
      { lastMs: 3_600_000, endsAgoMs: 3_600_000 },
    );
    const observer = new QueryObserver(client, { queryKey: ["panel-window"], queryFn, staleTime: 30_000 });
    const unsubscribe = observer.subscribe(() => {});
    try {
      await observer.refetch();
      const original = observer.getCurrentResult().data;
      expect(original?.window).toEqual({ startMs: 2_800_000, endMs: 6_400_000 });

      clock.mockReturnValue(10_060_000);
      const pending = observer.refetch();
      // A new selector must not relabel the retained samples with today's bounds.
      observer.setOptions({ queryKey: ["panel-window"], queryFn, staleTime: 30_000, select: (data: PanelResult) => ({ ...data }) });
      expect(observer.getCurrentResult().isFetching).toBe(true);
      expect(observer.getCurrentResult().data?.window).toEqual(original?.window);
      expect(complete).toBeDefined();
      complete!(response);
      await pending;
      expect(observer.getCurrentResult().data?.window).toEqual({ startMs: 2_860_000, endMs: 6_460_000 });
    } finally {
      unsubscribe();
      client.clear();
      clock.mockRestore();
    }
  });
});
