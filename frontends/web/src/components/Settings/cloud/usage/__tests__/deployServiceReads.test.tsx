// Copyright (c) 2026 Reliant Labs

/**
 * The Settings usage surface's DeployService reads run on the generated hooks
 * (hooks/controlplane/deploy-service-hooks_gen.ts), over the app transport:
 *
 *  - useDeployUsage's attribution rows are DeployService.ListUsage, asked in the
 *    tab's org for the billing period, and its refetch re-reads them;
 *  - the collection policy's roster is DeployService.ListEnvironments, asked in
 *    the org it is given.
 *
 * Both keys sit under "controlplane.v1.DeployService", which an org switch resets.
 */

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

// The overage read still goes through getControlPlaneClient, which needs a control-plane URL.
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import type { ListDeployUsageRequest } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { deployServiceKeys } from "@/hooks/controlplane/deploy-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { ORG_SCOPED_QUERY_ROOTS, useActiveOrgStore } from "@/store/activeOrgStore";

import { ObservabilityCollectionPolicy } from "../ObservabilityCollectionPolicy";
import { useDeployUsage } from "../useDeployUsage";

const PERIOD_START = new Date(Date.UTC(2025, 10, 1));
const PERIOD_END = new Date(Date.UTC(2025, 10, 3));

function client() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

beforeEach(() => {
  useActiveOrgStore.setState({ userId: "user", chosenId: "org-a", effectiveId: "org-a", resolved: true, failure: null });
});
afterEach(() => {
  // Unmount first: resetting the org below would otherwise re-key the reads and send them live.
  cleanup();
  setTransport(null);
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("useDeployUsage", () => {
  it("reads ListUsage in the tab's org for the billing period, and refetch re-reads it", async () => {
    const listUsage = vi.fn((_req: unknown) => ({ rows: [], costsWithheld: true }));
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserInfraOverage": {
          response: { periodStart: timestampFromDate(PERIOD_START), periodEnd: timestampFromDate(PERIOD_END) },
        },
        "DeployService.ListUsage": listUsage,
        "DeployService.ListDeployments": { response: { deployments: [] } },
        "DeployService.ListEnvironments": { response: { environments: [] } },
      }),
    );
    const queryClient = client();
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useDeployUsage(), { wrapper });

    await waitFor(() => expect(listUsage).toHaveBeenCalledTimes(1));
    const req = listUsage.mock.calls[0]![0] as ListDeployUsageRequest;
    expect(req.orgId).toBe("org-a");
    expect(req.startTime?.seconds).toBe(BigInt(PERIOD_START.getTime() / 1000));
    expect(req.endTime?.seconds).toBe(BigInt(PERIOD_END.getTime() / 1000));
    await waitFor(() => expect(result.current.summary.costsWithheld).toBe(true));

    act(() => result.current.refetch());
    await waitFor(() => expect(listUsage).toHaveBeenCalledTimes(2));
    expect((listUsage.mock.calls[1]![0] as ListDeployUsageRequest).orgId).toBe("org-a");
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
  });
});

describe("ObservabilityCollectionPolicy", () => {
  it("lists the org's environments from ListEnvironments, asked in that org", async () => {
    const listEnvironments = vi.fn((_req: unknown) => ({ environments: [{ id: "env-1", name: "prod", project: "shop" }] }));
    setTransport(mockTransport({ "DeployService.ListEnvironments": listEnvironments }));
    const queryClient = client();
    render(
      <QueryClientProvider client={queryClient}>
        <ObservabilityCollectionPolicy orgId="org-b" />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("shop / prod")).toBeInTheDocument();
    expect(screen.getByText("No release configuration reported yet.")).toBeInTheDocument();
    expect(listEnvironments).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-b" }));
    expect(queryClient.getQueryCache().findAll({ queryKey: deployServiceKeys.listEnvironments.all })).toHaveLength(1);
  });
});

it("an org switch resets the generated DeployService reads", () => {
  expect(ORG_SCOPED_QUERY_ROOTS).toContain(deployServiceKeys.all[0]);
});
