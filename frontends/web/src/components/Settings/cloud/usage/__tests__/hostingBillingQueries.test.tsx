import { Code } from "@connectrpc/connect";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { createFixtureTransport } from "@/fixtures/transport";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { billingHandlers } from "@/test/billingTransport";

import {
  hostingBillingKeys,
  useFreeHostingStatus,
  useOrgHostingUsageSummary,
  useSetUsageBillingBudget,
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

it("refuses a budget write in a read-only fixture scope without reaching the live transport", async () => {
  const app = mockTransport(billingHandlers({ setUsageBillingBudget: () => ({}) }));
  const appCall = vi.spyOn(app, "unary");
  setTransport(app);
  const fixtureWrite = vi.fn(() => ({}));
  const readOnlyMessage = "Sample app: changes are turned off";
  const scope = createTransportScope({
    id: "hosting-fixture",
    readOnly: true,
    transport: createFixtureTransport({
      readOnly: true,
      readOnlyMessage,
      scenario: { [`${BillingService.typeName}/SetUsageBillingBudget`]: fixtureWrite },
    }),
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>
  );
  const { result } = renderHook(() => useSetUsageBillingBudget("org-fixture"), { wrapper });

  await act(async () => {
    await expect(result.current.mutateAsync({
      infraBudgetCents: 12000n,
      policyVersion: "p1",
      consent: true,
      expectedCurrentVersion: 1n,
    })).rejects.toMatchObject({ code: Code.PermissionDenied, rawMessage: readOnlyMessage });
  });
  expect(fixtureWrite).not.toHaveBeenCalled();
  expect(appCall).not.toHaveBeenCalled();
});
