// Copyright (c) 2025 Reliant Labs

/**
 * Open the sidebar's project switcher from anywhere (a shortcut, the header,
 * the tray). The switcher lives in the chat sidebar; when the sidebar is
 * collapsed it is expanded first, so there is always a menu to open.
 */

import { useWorkspaceStateStore } from "../store/workspaceStateStore";

export const OPEN_PROJECT_SWITCHER_EVENT = "open-project-switcher";

export function openProjectSwitcher(): void {
  const workspaceState = useWorkspaceStateStore.getState();
  const wasCollapsed = !workspaceState.leftSidebarExpanded;
  if (wasCollapsed) workspaceState.setLeftSidebarExpandedGlobal(true);
  // A sidebar that was collapsed mounts on the next frame; ask it then.
  const dispatch = () => window.dispatchEvent(new CustomEvent(OPEN_PROJECT_SWITCHER_EVENT));
  if (wasCollapsed) requestAnimationFrame(dispatch);
  else dispatch();
}
