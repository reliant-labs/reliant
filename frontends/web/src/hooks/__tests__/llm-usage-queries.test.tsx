// Copyright (c) 2026 Reliant Labs

/**
 * useOwnLLMUsage against a mock UsageService: the six reads it makes (by day,
 * by model, by day per access mode) over inclusive UTC dates, assembled into
 * one answer, and the honest states when the server fails or does not serve
 * the RPC.
 */

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { LLMAuthMode, LLMUsageGroupBy, type GetLLMUsageRequest } from "@/gen/reliant/v1/usage_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { answer, LLM_END_MS, LLM_START_MS } from "@/components/Forge/Observability/Dashboards/__tests__/llmUsageFixtures";

import { useOwnLLMUsage, useOwnManagedPurposeUsage } from "../llm-usage-queries";

let queryClient: QueryClient;
beforeEach(() => {
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
});
afterEach(() => {
  cleanup();
  queryClient.clear();
});

const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;

describe("useOwnLLMUsage", () => {
  it("reads by day, by model, by purpose and by day per access mode over inclusive UTC dates", async () => {
    const seen: GetLLMUsageRequest[] = [];
    setTransport(mockTransport({ "UsageService.GetLLMUsage": (req: unknown) => (seen.push(req as GetLLMUsageRequest), answer(req as GetLLMUsageRequest)) }));
    const { result } = renderHook(() => useOwnLLMUsage({ startMs: LLM_START_MS, endMs: LLM_END_MS }), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(seen.every((r) => r.startDate === "2026-09-26" && r.endDate === "2026-10-25")).toBe(true);
    expect(seen.map((r) => [r.groupBy, r.authMode]).sort()).toEqual(
      [
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, undefined],
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_MODEL, undefined],
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE, undefined],
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, LLMAuthMode.LLM_AUTH_MODE_MANAGED],
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, LLMAuthMode.LLM_AUTH_MODE_BYOK],
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, LLMAuthMode.LLM_AUTH_MODE_SUBSCRIPTION],
        [LLMUsageGroupBy.LLM_USAGE_GROUP_BY_DAY, LLMAuthMode.LLM_AUTH_MODE_LOCAL],
      ].sort()
    );
    if (result.current.status !== "ready") throw new Error("not ready");
    const usage = result.current.usage;
    expect(usage.totals.calls).toBe(52);
    expect(usage.byModel.map((b) => b.key)).toEqual(["claude-sonnet-5", "claude-haiku-4-5", "gpt-6"]);
    expect(usage.byDayByMode.local).toEqual([]);
    expect(usage.byPurpose.map((b) => b.key)).toEqual(["chat", "compaction", "title"]);
    expect(usage.completeness).toBe("complete");
  });

  it("refreshes every read when ready, so newer calls within the same day are fetched", async () => {
    let calls = 0;
    setTransport(mockTransport({ "UsageService.GetLLMUsage": (req: unknown) => (calls++, answer(req as GetLLMUsageRequest)) }));
    const { result } = renderHook(() => useOwnLLMUsage({ startMs: LLM_START_MS, endMs: LLM_END_MS }), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(calls).toBe(7);
    act(() => result.current.refresh());
    await waitFor(() => expect(calls).toBe(14));
  });

  it("reads the caller's managed calls by purpose alone", async () => {
    const seen: GetLLMUsageRequest[] = [];
    setTransport(mockTransport({ "UsageService.GetLLMUsage": (req: unknown) => (seen.push(req as GetLLMUsageRequest), answer(req as GetLLMUsageRequest)) }));
    const { result } = renderHook(() => useOwnManagedPurposeUsage({ startMs: LLM_START_MS, endMs: LLM_END_MS }), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatchObject({ groupBy: LLMUsageGroupBy.LLM_USAGE_GROUP_BY_PURPOSE, authMode: LLMAuthMode.LLM_AUTH_MODE_MANAGED });
  });

  it("is not-deployed on a server that does not serve the RPC", async () => {
    setTransport(mockTransport({ "UsageService.GetLLMUsage": { error: new ConnectError("no", Code.Unimplemented) } }));
    const { result } = renderHook(() => useOwnLLMUsage({ startMs: LLM_START_MS, endMs: LLM_END_MS }), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("not-deployed"));
  });

  it("is an error, never an empty answer, when a read fails", async () => {
    setTransport(mockTransport({ "UsageService.GetLLMUsage": { error: new ConnectError("down", Code.Unavailable) } }));
    const { result } = renderHook(() => useOwnLLMUsage({ startMs: LLM_START_MS, endMs: LLM_END_MS }), { wrapper });
    await waitFor(() => expect(result.current.status).toBe("error"));
  });
});
