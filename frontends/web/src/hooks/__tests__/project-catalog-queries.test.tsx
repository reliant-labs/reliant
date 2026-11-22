// Copyright (c) 2025 Reliant Labs

/**
 * useProjectCatalog: the org is in the request and the key, nothing is asked
 * before the org is known, and it asks on the forge brand too (the forge host
 * mounts the one route tree, U6).
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { HostedAppsState } from "@/gen/reliant/v1/project_pb";
import { setTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useAuthStore } from "@/store/authStore";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { projectRow } from "@/test/projectList";

import { projectCatalogRequest, useProjectCatalog } from "../project-catalog-queries";
import { projectServiceKeys } from "../project-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/**
 * The catalog query is mounted and has asked nothing. A query that is
 * enabled starts its fetch when its observer subscribes, inside the render's
 * act, so "idle while observed" right after rendering is the proof that no
 * request went out, with no wait on a clock.
 */
function expectObservedAndIdle() {
  const queries = queryClient.getQueryCache().findAll({ queryKey: projectServiceKeys.all });
  expect(queries.length).toBeGreaterThan(0);
  for (const q of queries) {
    expect(q.getObserversCount()).toBeGreaterThan(0);
    expect(q.state.fetchStatus).toBe("idle");
  }
  expect(queryClient.isFetching()).toBe(0);
}

function server() {
  const requests: { orgId?: string }[] = [];
  const transport = mockTransport({
    "ProjectService.ListProjectsWithApps": (req) => {
      const orgId = (req as { orgId?: string }).orgId;
      requests.push({ orgId });
      return {
        orgId: orgId ?? "",
        projects: [{ project: projectRow({ id: `p-${orgId}`, name: `in ${orgId}`, path: "/x" }), repos: [], apps: [], checkouts: [] }],
        unlinkedApps: [{ name: "solo", createdAt: "2026-01-01T00:00:00Z", environments: [], hasStaticSite: false, sources: [], warning: "" }],
        hostedAppsState: HostedAppsState.OK,
      };
    },
  });
  return { transport, requests };
}

beforeEach(() => {
  useAuthStore.setState({ session: { access_token: "t" } as never });
  useActiveOrgStore.setState({ userId: "u-1", effectiveId: null, resolved: false, failure: null });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  resetShellForTests(null);
  useAuthStore.setState({ session: null });
  useActiveOrgStore.setState({ userId: null, effectiveId: null, resolved: false, failure: null });
});

describe("useProjectCatalog", () => {
  it("asks nothing until the tab's org is known, then asks for that org", async () => {
    const s = server();
    setTransport(s.transport);
    const view = renderHook(() => useProjectCatalog(), { wrapper });
    expect(view.result.current.isLoading).toBe(true);
    expectObservedAndIdle();
    expect(s.requests).toEqual([]);

    act(() => useActiveOrgStore.setState({ effectiveId: "org-1", resolved: true }));
    await waitFor(() => expect(view.result.current.catalog?.rows.map((r) => r.key)).toEqual(["p-org-1", "app:solo"]));
    expect(s.requests).toEqual([{ orgId: "org-1" }]);
    expect(queryClient.getQueryData(projectServiceKeys.listProjectsWithApps(projectCatalogRequest("org-1")))).toBeDefined();
  });

  it("reads the org it is given, under that org's key", async () => {
    const s = server();
    setTransport(s.transport);
    const view = renderHook(() => useProjectCatalog("org-2"), { wrapper });
    await waitFor(() => expect(view.result.current.catalog?.orgId).toBe("org-2"));
    expect(s.requests).toEqual([{ orgId: "org-2" }]);
    expect(queryClient.getQueryData(projectServiceKeys.listProjectsWithApps(projectCatalogRequest("org-1")))).toBeUndefined();
  });

  it("asks nothing signed out", async () => {
    useAuthStore.setState({ session: null });
    const s = server();
    setTransport(s.transport);
    const view = renderHook(() => useProjectCatalog("org-2"), { wrapper });
    expectObservedAndIdle();
    expect(s.requests).toEqual([]);
    expect(view.result.current.isLoading).toBe(false);
  });

  it("asks on the forge brand too, app-only rows included", async () => {
    resetShellForTests("forge");
    const s = server();
    setTransport(s.transport);
    const view = renderHook(() => useProjectCatalog("org-2"), { wrapper });
    expect(view.result.current.isLoading).toBe(true);
    await waitFor(() => expect(view.result.current.catalog?.rows.map((r) => r.key)).toEqual(["p-org-2", "app:solo"]));
    expect(s.requests).toEqual([{ orgId: "org-2" }]);
  });
});
