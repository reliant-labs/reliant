/**
 * Workspace layout actions for journey steps' `onEnter` / `onComplete`
 * hooks — e.g. the workflows outro collapsing the panels it just explained.
 *
 * Each goes through the existing owner of that panel, persisting exactly as
 * the user's own toggle does; nothing here keeps layout state of its own:
 *   - conversations sidebar: workspaceStateStore.setLeftSidebarExpandedGlobal
 *   - terminal: terminalStore.showTerminal / hideTerminal
 *   - files panel: ModernApp's file-browser state, via its window events
 *     ("open-file-browser" / "close-file-browser")
 *
 * Reliant shell only; harmless no-ops elsewhere (nothing listens).
 */

import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useTerminalStore } from "../../../store/terminalStore";

export const layoutActions = {
  showSidebar: () => useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true),
  hideSidebar: () => useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(false),
  showTerminal: () => useTerminalStore.getState().showTerminal(),
  hideTerminal: () => useTerminalStore.getState().hideTerminal(),
  showFiles: () => window.dispatchEvent(new CustomEvent("open-file-browser")),
  hideFiles: () => window.dispatchEvent(new CustomEvent("close-file-browser")),
  /** Collapse the sidebar, the files panel and the terminal: the chat alone. */
  collapseAll: () => {
    layoutActions.hideSidebar();
    layoutActions.hideFiles();
    layoutActions.hideTerminal();
  },
};
