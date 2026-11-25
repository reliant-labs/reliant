// Copyright (c) 2025 Reliant Labs

/**
 * useDashboardIndex: a project string narrows the list to that app (existing
 * callers), undefined is the whole org, and the two never share a cache entry
 * — nor does the project-less "" list share one with either. The client's
 * own built-ins (the person's LLM usage) join a project's list only where the
 * console reads the person's own account, never on sample data (a read-only
 * console: the public demo, the forge tour's sample app).
 */

import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObsReadOnlyProvider } from "@/components/Forge/Observability/obsReadOnly";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { LLM_USAGE_KEY } from "@/services/controlPlane/observability/clientBuiltins";
import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { dashboardKeys, useDashboard, useDashboardIndex } from "../dashboard-queries";

const SERVER_BUILTIN = { ref: { kind: "builtin", projectId: "", key: "org-costs" }, source: "builtin", title: "Costs" } as unknown as DashboardSummary;

function setup({ readOnly = false, dashboards = [] as DashboardSummary[] } = {}) {
  const listDashboards = vi.fn(async () => ({ dashboards, canWrite: true }));
  const getDashboard = vi.fn(async () => {
    throw new Error("not found");
  });
  const api = { listDashboards, getDashboard } as unknown as ObservabilityApi;
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObsReadOnlyProvider value={{ readOnly }}>
        <ObservabilityApiProvider api={api}>{children}</ObservabilityApiProvider>
      </ObsReadOnlyProvider>
    </QueryClientProvider>
  );
  return { listDashboards, getDashboard, client, wrapper };
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

  it("lists the person's LLM usage in a project's dashboards, and never on sample data", async () => {
    const keys = async (readOnly: boolean) => {
      const { wrapper } = setup({ readOnly, dashboards: [SERVER_BUILTIN] });
      const { result } = renderHook(() => useDashboardIndex("org-1", "shop", undefined), { wrapper });
      await waitFor(() => expect(result.current.isSuccess).toBe(true));
      return result.current.data!.dashboards.map((d) => d.ref.key);
    };
    expect(await keys(false)).toEqual(["org-costs", LLM_USAGE_KEY]);
    expect(await keys(true)).toEqual(["org-costs"]);
  });

  it("opens the LLM usage built-in without a request on the person's console, and asks the console's own API on sample data", async () => {
    const ref = { kind: "builtin", projectId: "", key: LLM_USAGE_KEY } as const;
    const own = setup();
    const opened = renderHook(() => useDashboard("org-1", ref), { wrapper: own.wrapper });
    await waitFor(() => expect(opened.result.current.isSuccess).toBe(true));
    expect(own.getDashboard).not.toHaveBeenCalled();

    const sample = setup({ readOnly: true });
    const refused = renderHook(() => useDashboard("org-1", ref), { wrapper: sample.wrapper });
    await waitFor(() => expect(sample.getDashboard).toHaveBeenCalled());
    expect(refused.result.current.data).toBeUndefined();
  });
});
