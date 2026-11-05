/**
 * The workflows outro runs `layoutActions.collapseAll` whatever the layout is.
 * New workspaces now start with the terminal closed, so it must be a no-op for
 * the panels that are already shut, and safe to run twice.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() },
}));

import { layoutActions } from "../journeys/layoutActions";
import { useTerminalStore } from "../../../store/terminalStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

const closeFileBrowser = vi.fn();

beforeEach(() => {
  localStorage.clear();
  useWorkspaceStateStore.getState().reset();
  useTerminalStore.setState({ isOpen: false });
  useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
  closeFileBrowser.mockClear();
  window.addEventListener("close-file-browser", closeFileBrowser);
});

afterEach(() => {
  window.removeEventListener("close-file-browser", closeFileBrowser);
});

describe("layoutActions.collapseAll", () => {
  it("closes the sidebar and files panel and leaves an already-closed terminal closed", () => {
    expect(useTerminalStore.getState().isOpen).toBe(false);

    expect(() => layoutActions.collapseAll()).not.toThrow();

    expect(useTerminalStore.getState().isOpen).toBe(false);
    expect(useWorkspaceStateStore.getState().leftSidebarExpanded).toBe(false);
    expect(closeFileBrowser).toHaveBeenCalledTimes(1);
  });

  it("is idempotent: a second run changes nothing and never reopens a panel", () => {
    layoutActions.collapseAll();
    layoutActions.collapseAll();

    expect(useTerminalStore.getState().isOpen).toBe(false);
    expect(useWorkspaceStateStore.getState().leftSidebarExpanded).toBe(false);
    expect(closeFileBrowser).toHaveBeenCalledTimes(2);
  });

  it("still closes a terminal the user opened", () => {
    useTerminalStore.setState({ isOpen: true });

    layoutActions.collapseAll();

    expect(useTerminalStore.getState().isOpen).toBe(false);
  });
});
