// Copyright (c) 2026 Reliant Labs

/**
 * THE SHELL'S NAV NEVER MOVES THE PROJECT OFF A CHAT'S URL (the 2026-11-23
 * prod incident: the owner could not open any chat of their main project).
 *
 * The one shell's left nav draws the focused project's app sections on every
 * page (Layout/sidebar/ShellNavBody → useConsoleNavModel). That model used to
 * make the project DECLARING the app the current project, wherever it was
 * drawn. The owner's main project (a multi-repo checkout, no forge name of its
 * own) shows the `control-plane` app, which a second project declares; and
 * while a chat loads the nav shows the remembered pick's app. Either way the
 * nav selected the other project over `/c/<id>`, and selectProject then moved
 * the URL to that project's last chat or its `/new?project=…&ws=…` view.
 *
 * Only the app's own console page may make its project the current one.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook } from "@testing-library/react";

import type { Project } from "@/store/projectStore";

const { projects } = vi.hoisted(() => ({
  projects: [
    // The control-plane checkout: it declares the app.
    { id: "p-cp", name: "control-plane", path: "/cp", is_forge: true, forge_project_name: "control-plane" },
    // The multi-repo checkout the chats are in: it declares no app of its own.
    { id: "p-labs", name: "reliant-labs", path: "/labs", is_forge: false, forge_project_name: null },
  ] as unknown as Project[],
}));

// The model's data reads, stubbed at their module seams: only the project
// selection is under test.
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects, isLoading: false }),
}));
vi.mock("@/hooks/forge-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/forge-queries")>()),
  useForgeRoster: () => ({ isLoading: false, topology: { isLoading: false }, envs: [] }),
}));
vi.mock("@/hooks/observability-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/observability-queries")>()),
  useObservabilityOrg: () => ({ orgId: null }),
  useAlerts: () => ({ data: [] }),
}));
vi.mock("@/hooks/dashboard-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/dashboard-queries")>()),
  useDashboardIndex: () => ({ data: undefined }),
  useDashboard: () => ({ data: undefined }),
}));
vi.mock("../useOptionalViews", () => ({ useOptionalViews: () => [] }));
vi.mock("../../Tests/surfaces/useTestsNavCount", () => ({ useTestsNavCount: () => 0 }));

const { useConsoleNavModel } = await import("../useConsoleNavModel");
const { useProjectStore } = await import("@/store/projectStore");

const labs = projects[1]!;
const selectProject = vi.fn(async (project: Project) => {
  useProjectStore.setState({ currentProject: project });
});
const originalSelect = useProjectStore.getState().selectProject;

beforeEach(() => {
  selectProject.mockClear();
  // The chat's project is the one in context.
  useProjectStore.setState({ currentProject: labs, selectProject });
});
afterEach(() => {
  // Unmounted first: a mounted console page would select its project again
  // the moment the store is reset under it.
  cleanup();
  useProjectStore.setState({ currentProject: null, selectProject: originalSelect });
});

describe("the shell nav's app sections and the current project", () => {
  it("a chat's URL keeps its project, even when the nav shows an app another project declares", () => {
    // Direct load of `/c/<id>` (or a click from `/project/<id>` that pushed it).
    renderHook(() => useConsoleNavModel("control-plane", "/c/chat-labs", {}));
    expect(selectProject).not.toHaveBeenCalled();
    expect(useProjectStore.getState().currentProject?.id).toBe("p-labs");
  });

  it("the org form of a chat's URL, a thread, and the new-chat view keep it too", () => {
    for (const path of ["/o/acme/c/chat-labs", "/c/chat-labs/t/th-1", "/new"]) {
      renderHook(() => useConsoleNavModel("control-plane", path, {}));
    }
    expect(selectProject).not.toHaveBeenCalled();
  });

  it("a project's own page keeps it while the nav shows the remembered pick's app", () => {
    renderHook(() => useConsoleNavModel("control-plane", "/project/p-labs", {}));
    expect(selectProject).not.toHaveBeenCalled();
  });

  it("the app's own console still makes the declaring project current", () => {
    renderHook(() => useConsoleNavModel("control-plane", "/forge/p/control-plane/releases", {}));
    expect(selectProject).toHaveBeenCalledTimes(1);
    expect(selectProject.mock.calls[0]![0].id).toBe("p-cp");
  });

  it("another app's console does not select this app's project", () => {
    renderHook(() => useConsoleNavModel("control-plane", "/forge/p/shopfront", {}));
    expect(selectProject).not.toHaveBeenCalled();
  });
});
