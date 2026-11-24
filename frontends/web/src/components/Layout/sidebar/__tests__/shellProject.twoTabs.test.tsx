// Copyright (c) 2026 Reliant Labs

/**
 * PER-TAB STATE NEVER LEAKS INTO ANOTHER TAB'S OPEN PAGE (2026-11-23).
 *
 * A choice of several projects is held per tab (useShellLastPick.several, the
 * tab's copy) and remembered for new tabs in the person's `sidebar.projects`
 * setting, which every tab shares and re-reads on focus. The shell used to
 * read the shared setting FIRST, so choosing projects in one tab replaced the
 * projects another tab had open the next time it re-read the setting. The
 * setting now only fills in a tab with no choice of its own.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

import type { ProjectCatalog, ProjectCatalogRow } from "@/services/projects/catalog";
import type { Project } from "@/store/projectStore";

const { shared } = vi.hoisted(() => ({
  /** The shared setting, as another tab last wrote it. */
  shared: { filter: [] as string[] },
}));

const row = (id: string): ProjectCatalogRow => ({
  kind: "project",
  key: id,
  name: id,
  activityAt: 0,
  project: { id, name: id, path: `/${id}`, is_git_repo: true, last_active: "", created_at: "", updated_at: "", is_forge: false } as Project,
  shared: false,
  repos: [],
  apps: [],
  checkouts: [],
  hasCheckout: true,
});
const catalog: ProjectCatalog = { orgId: "org", hostedAppsState: "ok", rows: ["p1", "p2", "p3", "p4"].map(row) };

vi.mock("../../../Projects/ProjectSelector", () => ({ useSelectorCatalog: () => ({ catalog, loading: false }) }));
vi.mock("@/hooks/chat-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/chat-queries")>()),
  useChat: () => ({ data: undefined }),
}));
vi.mock("@/hooks/sidebar-project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/sidebar-project-queries")>()),
  useSidebarProjectPrefs: () => ({ prefs: { filter: shared.filter } }),
}));

const { SEVERAL_PROJECTS_PICK, useShellLastPick, useShellProject } = await import("../shellProject");
const { useChatProjectFilterStore } = await import("@/hooks/useChatProjectFilter");

beforeEach(() => {
  shared.filter = [];
  useChatProjectFilterStore.setState({ many: [] });
});
afterEach(() => {
  useShellLastPick.setState({ key: null, several: [], focus: null });
  useChatProjectFilterStore.setState({ many: [] });
});

describe("two tabs choosing several projects", () => {
  it("this tab keeps its own choice when another tab's choice reaches the shared setting", () => {
    // This tab chose p1 + p2 (its own copy); the other tab then chose p3 + p4.
    useShellLastPick.setState({ key: SEVERAL_PROJECTS_PICK, several: ["p1", "p2"] });
    shared.filter = ["p3", "p4"];

    const { result } = renderHook(() => useShellProject("/", {}));

    expect(result.current.mode).toBe("many");
    expect([...result.current.ids].sort()).toEqual(["p1", "p2"]);
    expect([...useChatProjectFilterStore.getState().many].sort()).toEqual(["p1", "p2"]);
  });

  it("a tab with no choice of its own fills in from the shared setting", () => {
    useShellLastPick.setState({ key: SEVERAL_PROJECTS_PICK, several: [] });
    shared.filter = ["p3", "p4"];

    const { result } = renderHook(() => useShellProject("/", {}));

    expect(result.current.mode).toBe("many");
    expect([...result.current.ids].sort()).toEqual(["p3", "p4"]);
  });
});
