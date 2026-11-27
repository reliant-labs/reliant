import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { billingHandlers } from "@/test/billingTransport";

import {
  hostingBillingKeys,
  useFreeHostingStatus,
  useOrgHostingUsageSummary,
  useUsageBillingBudgetVersions,
} from "../hostingBillingQueries";

afterEach(() => setTransport(null));

it("reads from the fixture scope and refreshes through the existing org keys", async () => {
  const app = mockTransport();
  const appRead = vi.spyOn(app, "unary");
  setTransport(app);
  const listUsageBillingBudgetVersions = vi.fn(() => ({ versions: [{ version: 1n }] }));
  const getOrgHostingUsageSummary = vi.fn(() => ({ spentCents: 300n }));
  const getFreeHostingStatus = vi.fn(() => ({ isDesignated: true }));
  const scope = createTransportScope({
    id: "hosting-fixture",
    readOnly: true,
    transport: mockTransport(billingHandlers({
      listUsageBillingBudgetVersions,
      getOrgHostingUsageSummary,
      getFreeHostingStatus,
    })),
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>
  );
  const initialProps: { orgId: string | null } = { orgId: null };
  const { result, rerender } = renderHook(({ orgId }) => ({
    versions: useUsageBillingBudgetVersions(orgId ?? ""),
    usage: useOrgHostingUsageSummary(orgId),
    status: useFreeHostingStatus(orgId),
  }), { wrapper, initialProps });

  expect(result.current.versions.fetchStatus).toBe("idle");
  expect(result.current.usage.fetchStatus).toBe("idle");
  expect(result.current.status.fetchStatus).toBe("idle");
  expect(listUsageBillingBudgetVersions).not.toHaveBeenCalled();
  expect(getOrgHostingUsageSummary).not.toHaveBeenCalled();
  expect(getFreeHostingStatus).not.toHaveBeenCalled();

  const orgId = "org-fixture";
  rerender({ orgId });
  await waitFor(() => {
    expect(result.current.versions.data?.versions[0]?.version).toBe(1n);
    expect(result.current.usage.data?.spentCents).toBe(300n);
    expect(result.current.status.data?.isDesignated).toBe(true);
  });

  const reads = [
    [listUsageBillingBudgetVersions, hostingBillingKeys.budgetVersions(orgId)],
    [getOrgHostingUsageSummary, hostingBillingKeys.usageSummary(orgId)],
    [getFreeHostingStatus, hostingBillingKeys.freeTier(orgId)],
  ] as const;
  for (const [read, key] of reads) {
    expect(read).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ orgId }));
    expect(scope.queryClient.getQueryData(key)).toBeDefined();
  }
  await act(async () => {
    await Promise.all(reads.map(([, queryKey]) => scope.queryClient.invalidateQueries({ queryKey, exact: true })));
  });
  for (const [read] of reads) expect(read).toHaveBeenCalledTimes(2);
  expect(appRead).not.toHaveBeenCalled();
});
