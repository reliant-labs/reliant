// Copyright (c) 2025 Reliant Labs

/**
 * useDashboardIndex: a project string narrows the list to that app (existing
 * callers), undefined is the whole org, and the two never share a cache entry
 * — nor does the project-less "" list share one with either. The client's
 * own built-ins (the person's LLM usage) join a project's list only where the
 * console reads the person's own account, never on sample data (a read-only
 * console: the public demo, the forge tour's sample app).
 *
 * The hooks run on DashboardService's generated hooks, so the fake answers on
 * the wire (test/observabilityFixtures.tsx: the shop pack's handlers over it).
 */

import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObsReadOnlyProvider } from "@/components/Forge/Observability/obsReadOnly";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObservabilityFixtures } from "@/test/observabilityFixtures";
import { dashboardServiceKeys } from "@/hooks/controlplane/dashboard-service-hooks_gen";
import { listDashboardsRequest } from "@/services/controlPlane/observability/dashboardsApi";
import { LLM_USAGE_KEY } from "@/services/controlPlane/observability/clientBuiltins";
import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { invalidateOrgDashboards, useDashboard, useDashboardIndex, useToggleDashboardStar } from "../dashboard-queries";

/** A whole index row, as the wire carries every field. */
const row = (fields: Pick<DashboardSummary, "ref" | "source" | "title"> & Partial<DashboardSummary>): DashboardSummary => ({
  description: "",
  readOnly: false,
  starred: false,
  lastViewedAtMs: null,
  panelCount: 0,
  previewPanels: [],
  release: null,
  workspace: null,
  category: "",
  ...fields,
});

const SERVER_BUILTIN = row({ ref: { kind: "builtin", projectId: "", key: "org-costs" }, source: "builtin", title: "Costs" });

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
        <ObservabilityFixtures api={api}>{children}</ObservabilityFixtures>
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
      listDashboardsRequest({ orgId: "org-1" }),
      listDashboardsRequest({ orgId: "org-1", projectId: "" }),
      listDashboardsRequest({ orgId: "org-1", projectId: "shop" }),
    ].map((req) => JSON.stringify(dashboardServiceKeys.listDashboards(req)));
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

describe("dashboard writes", () => {
  const SHOP_ROW = row({ ref: { kind: "workspace", projectId: "shop", key: "ops" }, source: "workspace", title: "Ops" });

  it("re-read only the org's dashboards", async () => {
    const { wrapper, client, listDashboards } = setup({ dashboards: [SHOP_ROW] });
    const mine = renderHook(() => useDashboardIndex("org-1", "shop", undefined), { wrapper });
    const theirs = renderHook(() => useDashboardIndex("org-2", "shop", undefined), { wrapper });
    await waitFor(() => expect(mine.result.current.isSuccess && theirs.result.current.isSuccess).toBe(true));
    const asked = () => (listDashboards.mock.calls as unknown as Array<[{ orgId: string }]>).map(([req]) => req.orgId);
    expect(asked().sort()).toEqual(["org-1", "org-2"]);
    await invalidateOrgDashboards(client, "org-1");
    expect(asked().sort()).toEqual(["org-1", "org-1", "org-2"]);
  });

  it("star shows at once on every cached index of the org, and flips back when the server refuses", async () => {
    let refuse!: (error: Error) => void;
    const starDashboard = vi.fn(() => new Promise<void>((_resolve, reject) => (refuse = reject)));
    const { listDashboards } = setup({ dashboards: [SHOP_ROW] });
    const api = { listDashboards, starDashboard } as unknown as ObservabilityApi;
    const client = createTestQueryClient();
    const own = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        <ObservabilityFixtures api={api}>{children}</ObservabilityFixtures>
      </QueryClientProvider>
    );
    // Read the rows while rendering, as a component does (React Query re-renders for what was read).
    const index = renderHook(() => useDashboardIndex("org-1", "shop", undefined).data, { wrapper: own });
    await waitFor(() => expect(index.result.current).toBeDefined());
    const ops = () => index.result.current!.dashboards.find((d) => d.ref.key === "ops")!.starred;
    const star = renderHook(() => useToggleDashboardStar("org-1"), { wrapper: own });
    star.result.current.mutate({ ref: SHOP_ROW.ref, starred: true });
    await waitFor(() => expect(ops()).toBe(true));
    await waitFor(() => expect(starDashboard).toHaveBeenCalledWith("org-1", SHOP_ROW.ref, true));
    refuse(new Error("refused"));
    await waitFor(() => expect(star.result.current.isError).toBe(true));
    expect(ops()).toBe(false);
  });
});
