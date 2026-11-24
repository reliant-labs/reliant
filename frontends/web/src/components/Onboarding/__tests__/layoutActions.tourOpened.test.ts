/**
 * The workflows outro opens the file browser to explain it, and its Finish
 * closes only what the tour opened: a panel the person already had open stays
 * open, and the left nav (the one shell's navigation) is never touched.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() },
}));

import { __resetTourOpenedPanels, layoutActions } from "../journeys/layoutActions";
import { useProjectStore } from "../../../store/projectStore";
import { useTerminalStore } from "../../../store/terminalStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useWorktreeStore } from "../../../store/worktreeStore";

const openFileBrowser = vi.fn();
const closeFileBrowser = vi.fn();

function setFilesOpen(open: boolean) {
  useWorkspaceStateStore.getState().setRightPanelState("project-1", null, { fileBrowser: open });
}

beforeEach(() => {
  localStorage.clear();
  __resetTourOpenedPanels();
  useWorkspaceStateStore.getState().reset();
  useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
  useProjectStore.setState({ currentProject: { id: "project-1" } } as never);
  useWorktreeStore.setState({ currentWorktreeId: null } as never);
  useTerminalStore.setState({ isOpen: false });
  openFileBrowser.mockClear();
  closeFileBrowser.mockClear();
  window.addEventListener("open-file-browser", openFileBrowser);
  window.addEventListener("close-file-browser", closeFileBrowser);
});

afterEach(() => {
  window.removeEventListener("open-file-browser", openFileBrowser);
  window.removeEventListener("close-file-browser", closeFileBrowser);
});

describe("layoutActions: the panels a tour opened", () => {
  it("closes the file browser it opened, and leaves the left nav open", () => {
    setFilesOpen(false);

    layoutActions.showFilesForTour();
    expect(openFileBrowser).toHaveBeenCalledTimes(1);

    layoutActions.hidePanelsTourOpened();
    expect(closeFileBrowser).toHaveBeenCalledTimes(1);
    expect(useWorkspaceStateStore.getState().leftSidebarExpanded).toBe(true);
  });

  it("leaves a file browser the person had open", () => {
    setFilesOpen(true);

    layoutActions.showFilesForTour();
    layoutActions.hidePanelsTourOpened();

    expect(closeFileBrowser).not.toHaveBeenCalled();
  });

  it("closes a terminal only when the tour opened it", () => {
    layoutActions.showTerminalForTour();
    expect(useTerminalStore.getState().isOpen).toBe(true);
    layoutActions.hidePanelsTourOpened();
    expect(useTerminalStore.getState().isOpen).toBe(false);

    useTerminalStore.setState({ isOpen: true });
    layoutActions.showTerminalForTour();
    layoutActions.hidePanelsTourOpened();
    expect(useTerminalStore.getState().isOpen).toBe(true);
  });

  it("forgets what it opened once it has closed it", () => {
    setFilesOpen(false);
    layoutActions.showFilesForTour();
    layoutActions.hidePanelsTourOpened();
    layoutActions.hidePanelsTourOpened();

    expect(closeFileBrowser).toHaveBeenCalledTimes(1);
  });
});
