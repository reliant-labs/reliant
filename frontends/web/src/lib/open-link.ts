import { readActiveDaemonPreviewTemplate } from "../hooks/useActiveDaemonPreviewTemplate";
import { useBrowserStore } from "../store/browserStore";
import { useProjectStore } from "../store/projectStore";
import { useViewerStore } from "../store/viewerStore";
import { isElectron } from "./constants";
import { isDevUrl, isPreviewUrl, toPreviewUrl } from "./dev-preview";
import { logger } from "./logger";
import { signedInPreviewUrl } from "./proxy-session";

/**
 * Opens a link the user clicked.
 *
 * A dev URL — loopback, or a preview URL of the active daemon — opens in
 * Reliant's embedded browser, where the app under development is previewed.
 * Everything else (GitHub, docs, …) opens in the user's default browser, which
 * has their logins, password manager and extensions.
 *
 * With a cloud daemon, `localhost:N` means the workspace pod's port N, so a
 * loopback URL is first rewritten to the active daemon's preview URL. With a
 * local daemon it is left alone: this machine's localhost IS the daemon's.
 *
 * @param url - The URL to open
 * @param worktreeId - The worktree whose embedded browser opens a dev URL
 */
export async function openLink(url: string, worktreeId?: string): Promise<void> {
  const previewTemplate = await readActiveDaemonPreviewTemplate();
  if (!isDevUrl(url, previewTemplate)) {
    return openExternalLink(url);
  }
  const target = toPreviewUrl(url, previewTemplate) ?? url;

  // The embedded browser is an Electron <webview> and does not exist in the web
  // build — never mount it there. In web, a dev URL opens in a new browser tab.
  //
  // A browser tab is only data; the <webview> is rendered by a "browser" viewer
  // in the viewer panel, which needs a project ID. Without a project to hang the
  // viewer on there is nothing to render the tab, so the system browser is the
  // only outcome that actually shows the user the page.
  const projectId = useProjectStore.getState().currentProject?.id;
  if (!worktreeId || !projectId || !isElectron()) {
    return openExternalLink(target);
  }

  try {
    const tabUrl = await embeddedBrowserUrl(target, previewTemplate);
    const tabId = await useBrowserStore.getState().createTab(worktreeId, tabUrl, projectId);
    await useViewerStore.getState().openBrowserViewer(projectId, worktreeId, tabId);
    logger.debug("[openLink] Opened in embedded browser:", target);
  } catch (error) {
    logger.error("[openLink] Embedded browser failed, falling back to external:", error);
    return openExternalLink(target);
  }
}

/**
 * The URL a new embedded-browser tab should load for a dev URL: the cloud
 * daemon's preview URL for a loopback one, opened already signed in to the
 * workspace-proxy (the embedded browser has no Reliant session of its own; see
 * lib/proxy-session).
 */
export async function embeddedBrowserUrl(url: string, previewTemplate?: string): Promise<string> {
  const target = toPreviewUrl(url, previewTemplate) ?? url;
  return isPreviewUrl(target, previewTemplate) ? signedInPreviewUrl(target) : target;
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
