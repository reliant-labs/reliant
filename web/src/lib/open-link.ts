import { api } from "../api/client";
import { useBrowserStore } from "../store/browserStore";
import { useProjectStore } from "../store/projectStore";
import { useViewerStore } from "../store/viewerStore";
import { isElectron } from "./constants";
import { logger } from "./logger";

/**
 * Opens a URL either in Reliant's embedded browser or in the system default browser,
 * based on user preference.
 * 
 * @param url - The URL to open
 * @param worktreeId - The worktree ID for opening in embedded browser
 * @param forceExternal - If true, always open in system browser (for OAuth flows, etc.)
 */
export async function openLink(
  url: string,
  worktreeId?: string,
  forceExternal = false
): Promise<void> {
  // Always use external browser if forced
  if (forceExternal) {
    return openExternalLink(url);
  }

  try {
    // Check user preference
    const preferences = await api.settings.getPreferences();
    const openInApp = preferences.additional?.browserOpenLinksInApp !== "false"; // Default true

    // The embedded browser is an Electron <webview> and does not exist in the web
    // build — never mount it there. In web, always fall through to a new browser tab.
    //
    // A browser tab is only data; the <webview> is rendered by a "browser" viewer
    // in the viewer panel, which needs a project ID. Without a project to hang the
    // viewer on there is nothing to render the tab, so the system browser is the
    // only outcome that actually shows the user the page.
    const projectId = useProjectStore.getState().currentProject?.id;
    if (openInApp && worktreeId && projectId && isElectron()) {
      const browserStore = useBrowserStore.getState();
      const tabId = await browserStore.createTab(worktreeId, url, projectId);
      await useViewerStore.getState().openBrowserViewer(projectId, worktreeId, tabId);
      logger.debug("[openLink] Opened in embedded browser:", url);
    } else {
      // Open in system browser (or a new tab in web)
      return openExternalLink(url);
    }
  } catch (error) {
    logger.error("[openLink] Error checking preference, falling back to external:", error);
    return openExternalLink(url);
  }
}

/**
 * Opens a URL in the system's default browser.
 */
export async function openExternalLink(url: string): Promise<void> {
  if (window.electronAPI?.openExternal) {
    try {
      await window.electronAPI.openExternal(url);
      logger.debug("[openLink] Opened in system browser:", url);
    } catch (error) {
      logger.error("[openLink] Failed to open external URL:", error);
      // Fallback: try window.open
      window.open(url, "_blank");
    }
  } else {
    // Web fallback
    window.open(url, "_blank");
  }
}
