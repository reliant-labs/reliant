// Copyright (c) 2025 Reliant Labs

/**
 * useProjectCatalog: the org is in the request and the key, and nothing is
 * asked before the org is known.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { HostedAppsState } from "@/gen/reliant/v1/project_pb";
import { setTransport } from "@/lib/connect";
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
  useAuthStore.setState({ session: null });
  useActiveOrgStore.setState({ userId: null, effectiveId: null, resolved: false, failure: null });
});

describe("useProjectCatalog", () => {
  it("asks nothing until the tab's org is known, then asks for that org", async () => {
    const s = server();
    setTransport(s.transport);
    const view = renderHook(() => useProjectCatalog(), { wrapper });
    expect(view.result.current.isLoading).toBe(true);
    await new Promise((r) => setTimeout(r, 20));
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
    await new Promise((r) => setTimeout(r, 20));
    expect(s.requests).toEqual([]);
    expect(view.result.current.isLoading).toBe(false);
  });
});
