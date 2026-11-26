import type { ReactNode } from "react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { billingServiceKeys } from "@/hooks/controlplane/billing-service-hooks_gen";
import { deployServiceKeys, useListEnvironments } from "@/hooks/controlplane/deploy-service-hooks_gen";
import { observabilityBillingServiceKeys } from "@/hooks/controlplane/observability-billing-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { GetObservabilityLimitsResponseSchema, UpdateObservabilityLimitsResponseSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { obsLimitHistoryRequest, useObsBillingLimits, useObsLimitHistory, useUpdateObsLimits } from "../observability-billing-queries";

const limitsKey = (org: string) => observabilityBillingServiceKeys.getObservabilityLimits({ orgId: org });
afterEach(() => setTransport(null));

describe("observability limits pending writes", () => {
  it("finishes A's save in A's cache without changing B's cache or mutation state, even if B's read fails", async () => {
    const a = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 5000n, version: 1n });
    const b = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 7500n, version: 9n, canUpdate: true });
    const savedA = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 6000n, version: 2n });
    let finish!: (response: ReturnType<typeof create<typeof UpdateObservabilityLimitsResponseSchema>>) => void;
    const pending = new Promise<ReturnType<typeof create<typeof UpdateObservabilityLimitsResponseSchema>>>(resolve => { finish = resolve; });
    const save = vi.fn(() => pending);
    const get = vi.fn(() => { throw new ConnectError("B refresh unavailable", Code.Unavailable); });
    setTransport(mockTransport({ "ObservabilityBillingService.GetObservabilityLimits": get, "ObservabilityBillingService.UpdateObservabilityLimits": save }));
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
    cache.setQueryData(limitsKey("A"), a);
    cache.setQueryData(limitsKey("B"), b);
    const invalidate = vi.spyOn(cache, "invalidateQueries");
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={cache}>{children}</QueryClientProvider>;
    const { result, rerender, unmount } = renderHook(({ orgId }) => ({ update: useUpdateObsLimits(orgId), limits: useObsBillingLimits(orgId) }), { initialProps: { orgId: "A" }, wrapper });
    let completion!: Promise<unknown>;
    act(() => { completion = result.current.update.mutateAsync({ orgId: "A", expectedVersion: 1n, spendCapCents: 6000n }); });
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    rerender({ orgId: "B" });
    expect(result.current.limits.data).toEqual(b);
    await act(async () => { finish(create(UpdateObservabilityLimitsResponseSchema, { limits: savedA })); await completion; });
    expect(cache.getQueryData(limitsKey("A"))).toEqual(savedA);
    expect(cache.getQueryData(limitsKey("B"))).toEqual(b);
    // A's billing reads, A's limit history and A's collection policy: nothing of B's.
    expect(invalidate).toHaveBeenCalledTimes(3);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: observabilityBillingServiceKeys.all, predicate: expect.any(Function) });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: billingServiceKeys.listOrgLimitChangesInfinite(obsLimitHistoryRequest("A")) });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: deployServiceKeys.listEnvironments({ orgId: "A" }) });
    const { predicate } = invalidate.mock.calls.find(([filters]) => filters?.predicate)![0]!;
    expect(predicate!(cache.getQueryCache().find({ queryKey: limitsKey("A"), exact: true })!)).toBe(true);
    expect(predicate!(cache.getQueryCache().find({ queryKey: limitsKey("B"), exact: true })!)).toBe(false);
    expect(cache.getQueryState(limitsKey("A"))?.isInvalidated).toBe(true);
    expect(cache.getQueryState(limitsKey("B"))?.isInvalidated).toBe(false);
    expect(result.current.update.isIdle).toBe(true);
    expect(result.current.update.data).toBeUndefined();
    await act(async () => {
      const refreshed = await result.current.limits.refetch();
      expect(refreshed.isError).toBe(true);
      expect(refreshed.error?.message).toContain("B refresh unavailable");
    });
    expect(cache.getQueryState(limitsKey("B"))?.status).toBe("error");
    expect(result.current.limits.data).toEqual(b);
    expect(cache.getQueryData(limitsKey("B"))).toEqual(b);
    unmount(); cache.clear();
  });
});

describe("saving observability limits", () => {
  it("refetches that org's collection policy (DeployService.ListEnvironments) and limit history, not another org's", async () => {
    const listEnvironments = vi.fn((_req: unknown) => ({ environments: [] }));
    const listOrgLimitChanges = vi.fn((_req: unknown) => ({ changes: [], nextPageToken: "" }));
    const saved = create(GetObservabilityLimitsResponseSchema, { spendCapCents: 6000n, version: 2n });
    setTransport(mockTransport({
      "DeployService.ListEnvironments": listEnvironments,
      "BillingService.ListOrgLimitChanges": listOrgLimitChanges,
      "ObservabilityBillingService.UpdateObservabilityLimits": { response: { limits: saved } },
    }));
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={cache}>{children}</QueryClientProvider>;
    const { result, unmount } = renderHook(() => ({
      update: useUpdateObsLimits("A"),
      policyA: useListEnvironments({ orgId: "A" }), policyB: useListEnvironments({ orgId: "B" }),
      historyA: useObsLimitHistory("A"), historyB: useObsLimitHistory("B"),
    }), { wrapper });
    await waitFor(() => expect([result.current.policyA, result.current.policyB, result.current.historyA, result.current.historyB].every((q) => q.isSuccess)).toBe(true));
    const orgsRead = (fn: { mock: { calls: unknown[][] } }) => fn.mock.calls.map(([req]) => (req as { orgId: string }).orgId);
    expect(orgsRead(listEnvironments).sort()).toEqual(["A", "B"]);
    expect(listOrgLimitChanges).toHaveBeenCalledWith(expect.objectContaining({ orgId: "A", pageSize: 50, pageToken: "" }));

    await act(async () => { await result.current.update.mutateAsync({ orgId: "A", expectedVersion: 1n, spendCapCents: 6000n }); });
    expect(cache.getQueryData(limitsKey("A"))).toEqual(saved);
    await waitFor(() => expect(orgsRead(listEnvironments).filter((org) => org === "A")).toHaveLength(2));
    await waitFor(() => expect(orgsRead(listOrgLimitChanges).filter((org) => org === "A")).toHaveLength(2));
    expect(orgsRead(listEnvironments).filter((org) => org === "B")).toHaveLength(1);
    expect(orgsRead(listOrgLimitChanges).filter((org) => org === "B")).toHaveLength(1);
    unmount(); cache.clear();
  });
});
