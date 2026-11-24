/**
 * Workspace layout actions for journey steps' `onEnter` / `onComplete`
 * hooks — e.g. the workflows outro opening the file browser it explains, and
 * closing it again when the tour ends.
 *
 * Each goes through the existing owner of that panel, persisting exactly as
 * the user's own toggle does; nothing here keeps layout state of its own
 * beyond which panels a tour opened:
 *   - the left nav: workspaceStateStore.setLeftSidebarExpandedGlobal
 *   - terminal: terminalStore.showTerminal / hideTerminal
 *   - files panel: ModernApp's file-browser state, via its window events
 *     ("open-file-browser" / "close-file-browser")
 *
 * Reliant shell only; harmless no-ops elsewhere (nothing listens).
 */

import { useProjectStore } from "../../../store/projectStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useTerminalStore } from "../../../store/terminalStore";
import { useWorktreeStore } from "../../../store/worktreeStore";

/** Whether the file browser is open, as the user's own toggle last recorded it (per workspace). */
function filesOpen(): boolean {
  const projectId = useProjectStore.getState().currentProject?.id;
  if (!projectId) return false;
  const worktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
  return useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId).rightPanelState.fileBrowser;
}

/** The panels a tour step opened for itself: the only ones the tour closes again. */
const openedByTour = { files: false, terminal: false };

export const layoutActions = {
  showSidebar: () => useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true),
  hideSidebar: () => useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(false),
  showTerminal: () => useTerminalStore.getState().showTerminal(),
  hideTerminal: () => useTerminalStore.getState().hideTerminal(),
  showFiles: () => window.dispatchEvent(new CustomEvent("open-file-browser")),
  hideFiles: () => window.dispatchEvent(new CustomEvent("close-file-browser")),
  /** Open the file browser for a step, remembering that the tour opened it (a no-op when it is open). */
  showFilesForTour: () => {
    if (!filesOpen()) openedByTour.files = true;
    layoutActions.showFiles();
  },
  /** Open the terminal for a step, remembering that the tour opened it (a no-op when it is open). */
  showTerminalForTour: () => {
    if (useTerminalStore.getState().isOpen) return;
    openedByTour.terminal = true;
    layoutActions.showTerminal();
  },
  /**
   * Close the files panel and the terminal, but only those a step opened
   * (showFilesForTour / showTerminalForTour): a panel the person had open
   * stays open, and the left nav, the shell's navigation, is never touched.
   */
  hidePanelsTourOpened: () => {
    if (openedByTour.files) layoutActions.hideFiles();
    if (openedByTour.terminal) layoutActions.hideTerminal();
    openedByTour.files = false;
    openedByTour.terminal = false;
  },
  /** Collapse the left nav, the files panel and the terminal: the chat alone. */
  collapseAll: () => {
    layoutActions.hideSidebar();
    layoutActions.hideFiles();
    layoutActions.hideTerminal();
  },
};

/** Test-only: forget which panels a tour opened. */
export function __resetTourOpenedPanels(): void {
  openedByTour.files = false;
  openedByTour.terminal = false;
}
