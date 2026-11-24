/**
 * useChatProjectFilter's org bridge: the org becoming known keeps the URL's
 * choice (it is that org's); a switch to another org drops it.
 */
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/hooks/project-queries", async (original) => ({
  ...(await original<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: [], isLoading: false, error: null, refetch: () => {} }),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({
    prefs: { pinned: new Set(), hidden: new Set(), showAll: false, filter: [] },
    writable: true,
    loadFailed: false,
    setFilter: () => {},
  }),
}));

import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

import { resetChatProjectFilterForTests, useChatProjectFilter } from "../useChatProjectFilter";

const AT_TWO: { pathname: string; search: Record<string, unknown> } = { pathname: "/", search: { projects: "a,b" } };

beforeEach(() => {
  resetChatProjectFilterForTests();
  useWorkspaceStateStore.setState({ projectFilterId: null });
  useActiveOrgStore.setState({ userId: "u-1", effectiveId: null, resolved: false, failure: null });
});

afterEach(() => {
  cleanup();
  useActiveOrgStore.setState({ userId: null, effectiveId: null, resolved: false, failure: null });
});

describe("useChatProjectFilter and the org", () => {
  it("keeps the URL's choice when the tab's org becomes known", () => {
    const view = renderHook(() => useChatProjectFilter({ sync: AT_TWO }));
    expect(view.result.current).toMatchObject({ mode: "many", ids: ["a", "b"] });
    act(() => useActiveOrgStore.setState({ effectiveId: "org-1", resolved: true }));
    expect(view.result.current).toMatchObject({ mode: "many", ids: ["a", "b"] });
  });

  it("drops it on a switch to another org", () => {
    useActiveOrgStore.setState({ effectiveId: "org-1", resolved: true });
    const view = renderHook(({ at }) => useChatProjectFilter({ sync: at }), { initialProps: { at: AT_TWO } });
    expect(view.result.current.mode).toBe("many");
    act(() => useActiveOrgStore.setState({ effectiveId: "org-2" }));
    // The switch lands on the other org's page, with no `projects`.
    view.rerender({ at: { pathname: "/", search: {} } });
    expect(view.result.current).toMatchObject({ mode: "all", ids: [] });
  });

  it("names one project by the single-project filter, which wins over a filter of several", () => {
    const view = renderHook(() => useChatProjectFilter({ sync: AT_TWO }));
    act(() => useWorkspaceStateStore.setState({ projectFilterId: "c" }));
    expect(view.result.current).toMatchObject({ mode: "one", ids: ["c"] });
  });
});
