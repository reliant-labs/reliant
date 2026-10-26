// Copyright (c) 2025 Reliant Labs

/**
 * useDashboardIndex: a project string narrows the list to that app (existing
 * callers), undefined is the whole org, and the two never share a cache entry
 * — nor does the project-less "" list share one with either.
 */

import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { dashboardKeys, useDashboardIndex } from "../dashboard-queries";

function setup() {
  const listDashboards = vi.fn(async () => ({ dashboards: [], canWrite: true }));
  const api = { listDashboards } as unknown as ObservabilityApi;
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>{children}</ObservabilityApiProvider>
    </QueryClientProvider>
  );
  return { listDashboards, client, wrapper };
}

describe("useDashboardIndex", () => {
  it("asks for the whole org when no project is given", async () => {
    const { listDashboards, wrapper } = setup();
    const { result } = renderHook(() => useDashboardIndex("org-1", undefined, undefined), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const req = (listDashboards.mock.calls as unknown as Array<[Record<string, unknown>]>)[0]![0];
    expect(req.orgId).toBe("org-1");
    expect(req.projectId).toBeUndefined();
  });

  it("still narrows to a project when given one", async () => {
    const { listDashboards, wrapper } = setup();
    const { result } = renderHook(() => useDashboardIndex("org-1", "shop", "env-prod"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(listDashboards).toHaveBeenCalledWith({ orgId: "org-1", projectId: "shop", envId: "env-prod" });
  });

  it("keeps org-wide, one app and project-less lists apart in the cache", () => {
    const keys = [
      dashboardKeys.index("org-1", null, ""),
      dashboardKeys.index("org-1", "", ""),
      dashboardKeys.index("org-1", "shop", ""),
    ].map((key) => JSON.stringify(key));
    expect(new Set(keys).size).toBe(3);
  });
});
