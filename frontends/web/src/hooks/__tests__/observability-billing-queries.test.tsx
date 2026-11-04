import type { ReactNode } from "react";
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { GetObservabilityLimitsResponseSchema, UpdateObservabilityLimitsResponseSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { obsBillingKey, ObservabilityBillingApiContext, useObsBillingLimits, useUpdateObsLimits } from "../observability-billing-queries";

const limitsKey = (org: string) => [...obsBillingKey(org), "limits"];
describe("observability limits pending writes", () => {
  it("finishes A's save in A's cache without changing B's cache or mutation state, even if B's read fails", async () => {
    const a = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 5000n, version: 1n });
    const b = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 7500n, version: 9n, canUpdate: true });
    const savedA = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 6000n, version: 2n });
    let finish!: (response: ReturnType<typeof create<typeof UpdateObservabilityLimitsResponseSchema>>) => void;
    const pending = new Promise<ReturnType<typeof create<typeof UpdateObservabilityLimitsResponseSchema>>>(resolve => { finish = resolve; });
    const save = vi.fn(() => pending);
    const get = vi.fn().mockRejectedValue(new Error("B refresh unavailable"));
    const api = { getObservabilityLimits: get, updateObservabilityLimits: save, getObservabilityUsage: vi.fn(), getObservabilityStorage: vi.fn(), listObservabilityUsage: vi.fn() };
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
    cache.setQueryData(limitsKey("A"), a);
    cache.setQueryData(limitsKey("B"), b);
    const invalidate = vi.spyOn(cache, "invalidateQueries");
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={cache}><ObservabilityBillingApiContext.Provider value={api}>{children}</ObservabilityBillingApiContext.Provider></QueryClientProvider>;
    const { result, rerender, unmount } = renderHook(({ orgId }) => ({ update: useUpdateObsLimits(orgId), limits: useObsBillingLimits(orgId) }), { initialProps: { orgId: "A" }, wrapper });
    let completion!: Promise<unknown>;
    act(() => { completion = result.current.update.mutateAsync({ orgId: "A", expectedVersion: 1n, spendCapCents: 6000n }); });
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    rerender({ orgId: "B" });
    expect(result.current.limits.data).toEqual(b);
    await act(async () => { finish(create(UpdateObservabilityLimitsResponseSchema, { limits: savedA })); await completion; });
    expect(cache.getQueryData(limitsKey("A"))).toEqual(savedA);
    expect(cache.getQueryData(limitsKey("B"))).toEqual(b);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: obsBillingKey("A") });
    expect(result.current.update.isIdle).toBe(true);
    expect(result.current.update.data).toBeUndefined();
    await act(async () => {
      const refreshed = await result.current.limits.refetch();
      expect(refreshed.isError).toBe(true);
      expect(refreshed.error).toEqual(new Error("B refresh unavailable"));
    });
    expect(cache.getQueryState(limitsKey("B"))?.status).toBe("error");
    expect(result.current.limits.data).toEqual(b);
    expect(cache.getQueryData(limitsKey("B"))).toEqual(b);
    unmount(); cache.clear();
  });
});
