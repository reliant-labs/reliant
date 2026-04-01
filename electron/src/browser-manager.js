const { WebContentsView } = require("electron");
const log = require("./logger");

/**
 * BrowserManager - Manages WebContentsView instances for browser tabs
 */
class BrowserManager {
  constructor() {
    this.views = new Map(); // tabId -> WebContentsView
    this.tabToPane = new Map(); // tabId -> paneId (tracks which pane a tab belongs to)
    this.activeTabByPane = new Map(); // paneId -> tabId (tracks active tab per pane)
    this.activePaneId = null; // Currently visible pane
    this.window = null;
    this.bounds = { x: 0, y: 0, width: 800, height: 600 };
  }

  /**
   * Initialize the browser manager with a window
   * @param {BrowserWindow} window - The parent window
   */
  initialize(window) {
    this.window = window;
    log.info("[BrowserManager] Initialized");

    // Listen for window resize
    window.on("resize", () => {
      this.updateActiveBounds();
    });

    // Clean up on window close
    window.on("closed", () => {
      this.cleanup();
    });
  }

  /**
   * Set the bounds for browser content area
   * @param {Object} bounds - { x, y, width, height }
   * @param {string} paneId - The pane ID requesting the bounds update
   */
  setBounds(bounds, paneId) {
    this.bounds = bounds;
    this.activePaneId = paneId;
    log.info("[BrowserManager] setBounds called", {
      bounds,
      paneId,
      activePaneId: this.activePaneId,
      activeTabForPane: this.activeTabByPane.get(paneId),
      allActiveTabs: Array.from(this.activeTabByPane.entries())
    });
    this.updateActiveBounds();
  }

  /**
   * Hide all browser views (called when browser tab is not visible)
   */
  hideAll() {
    if (!this.window) {
      return;
    }

    log.info("[BrowserManager] Hiding all browser views");

    // Remove all views from the window
    for (const [tabId, view] of this.views.entries()) {
      try {
        this.window.contentView.removeChildView(view);
      } catch (e) {
        // View might not be attached, that's fine
      }
    }

    // Clear active pane so views won't show until setBounds is called again
    this.activePaneId = null;
  }

  /**
   * Create a new browser tab
   * @param {string} tabId - Unique tab identifier
   * @param {string} url - Initial URL to load
   * @param {string} paneId - Pane identifier this tab belongs to
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async createTab(tabId, url, paneId) {
    try {
      if (this.views.has(tabId)) {
        log.warn("[BrowserManager] Tab already exists", { tabId });
        return { success: false, error: "Tab already exists" };
      }

      log.info("[BrowserManager] Creating tab", { tabId, url, paneId });

      // Create WebContentsView with security settings
      const view = new WebContentsView({
        webPreferences: {
          sandbox: true,
          nodeIntegration: false,
          contextIsolation: true,
          webSecurity: true,
          allowRunningInsecureContent: false,
          javascript: true,
          images: true,
          webgl: true,
        },
      });

      // Set up event listeners for this tab
      this.setupViewListeners(tabId, view);

      // Store the view and pane association
      this.views.set(tabId, view);
      if (paneId) {
        this.tabToPane.set(tabId, paneId);
      }

      // Load the URL
      await view.webContents.loadURL(url);

      // Set this tab as active for its pane
      if (paneId) {
        this.activeTabByPane.set(paneId, tabId);
        log.info("[BrowserManager] Set tab as active for pane", { tabId, paneId });

        // If this pane is currently active, update the view
        if (this.activePaneId === paneId) {
          this.updateActiveBounds();
        }
      }

      return { success: true };
    } catch (error) {
      log.error("[BrowserManager] Failed to create tab", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Close a browser tab
   * @param {string} tabId - Tab identifier
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async closeTab(tabId) {
    try {
      const view = this.views.get(tabId);
      if (!view) {
        log.warn("[BrowserManager] Tab not found", { tabId });
        return { success: false, error: "Tab not found" };
      }

      log.info("[BrowserManager] Closing tab", { tabId });

      // Get pane this tab belongs to
      const paneId = this.tabToPane.get(tabId);

      // Remove from window if it's currently visible
      if (this.window) {
        try {
          this.window.contentView.removeChildView(view);
        } catch (e) {
          // View might not be attached, that's fine
        }
      }

      // If this was the active tab for its pane, clear it
      if (paneId && this.activeTabByPane.get(paneId) === tabId) {
        this.activeTabByPane.delete(paneId);
      }

      // Close the webContents
      if (!view.webContents.isDestroyed()) {
        view.webContents.close();
      }

      // Remove from maps
      this.views.delete(tabId);
      this.tabToPane.delete(tabId);

      return { success: true };
    } catch (error) {
      log.error("[BrowserManager] Failed to close tab", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Set the active tab for its pane
   * @param {string} tabId - Tab identifier
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async setActiveTab(tabId) {
    try {
      const view = this.views.get(tabId);
      if (!view) {
        log.warn("[BrowserManager] Tab not found", { tabId });
        return { success: false, error: "Tab not found" };
      }

      const paneId = this.tabToPane.get(tabId);
      if (!paneId) {
        log.warn("[BrowserManager] Tab has no associated pane", { tabId });
        return { success: false, error: "Tab has no associated pane" };
      }

      log.info("[BrowserManager] Setting active tab for pane", { tabId, paneId });

      // Set this tab as active for its pane
      this.activeTabByPane.set(paneId, tabId);

      // If this pane is currently active, update the view
      if (this.activePaneId === paneId) {
        this.updateActiveBounds();
      }

      return { success: true };
    } catch (error) {
      log.error("[BrowserManager] Failed to set active tab", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Navigate a tab to a URL
   * @param {string} tabId - Tab identifier
   * @param {string} url - URL to navigate to
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async navigateTab(tabId, url) {
    try {
      const view = this.views.get(tabId);
      if (!view) {
        log.warn("[BrowserManager] Tab not found", { tabId });
        return { success: false, error: "Tab not found" };
      }

      log.info("[BrowserManager] Navigating tab", { tabId, url });
      await view.webContents.loadURL(url);

      return { success: true };
    } catch (error) {
      log.error("[BrowserManager] Failed to navigate tab", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Go back in tab history
   * @param {string} tabId - Tab identifier
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async goBack(tabId) {
    try {
      const view = this.views.get(tabId);
      if (!view) {
        return { success: false, error: "Tab not found" };
      }

      if (view.webContents.navigationHistory.canGoBack()) {
        view.webContents.navigationHistory.goBack();
        return { success: true };
      }

      return { success: false, error: "Cannot go back" };
    } catch (error) {
      log.error("[BrowserManager] Failed to go back", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Go forward in tab history
   * @param {string} tabId - Tab identifier
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async goForward(tabId) {
    try {
      const view = this.views.get(tabId);
      if (!view) {
        return { success: false, error: "Tab not found" };
      }

      if (view.webContents.navigationHistory.canGoForward()) {
        view.webContents.navigationHistory.goForward();
        return { success: true };
      }

      return { success: false, error: "Cannot go forward" };
    } catch (error) {
      log.error("[BrowserManager] Failed to go forward", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Reload a tab
   * @param {string} tabId - Tab identifier
   * @returns {Promise<Object>} - { success: boolean, error?: string }
   */
  async reload(tabId) {
    try {
      const view = this.views.get(tabId);
      if (!view) {
        return { success: false, error: "Tab not found" };
      }

      view.webContents.reload();
      return { success: true };
    } catch (error) {
      log.error("[BrowserManager] Failed to reload tab", { tabId, error: error.message });
      return { success: false, error: error.message };
    }
  }

  /**
   * Set up event listeners for a WebContentsView
   * @param {string} tabId - Tab identifier
   * @param {WebContentsView} view - The view instance
   */
  setupViewListeners(tabId, view) {
    const webContents = view.webContents;

    // Send updates to renderer
    const sendUpdate = (data) => {
      if (this.window && !this.window.isDestroyed()) {
        this.window.webContents.send("browser-tab-update", {
          id: tabId,
          ...data,
        });
      }
    };

    // Page title updated
    webContents.on("page-title-updated", (event, title) => {
      log.debug("[BrowserManager] Title updated", { tabId, title });
      sendUpdate({ title });
    });

    // URL changed
    webContents.on("did-navigate", (event, url) => {
      log.debug("[BrowserManager] Navigation", { tabId, url });
      sendUpdate({
        url,
        canGoBack: webContents.navigationHistory.canGoBack(),
        canGoForward: webContents.navigationHistory.canGoForward(),
      });
    });

    webContents.on("did-navigate-in-page", (event, url) => {
      sendUpdate({
        url,
        canGoBack: webContents.navigationHistory.canGoBack(),
        canGoForward: webContents.navigationHistory.canGoForward(),
      });
    });

    // Loading state
    webContents.on("did-start-loading", () => {
      log.debug("[BrowserManager] Started loading", { tabId });
      sendUpdate({ isLoading: true });
    });

    webContents.on("did-stop-loading", () => {
      log.debug("[BrowserManager] Stopped loading", { tabId });
      sendUpdate({ isLoading: false });
    });

    // Favicon
    webContents.on("page-favicon-updated", (event, favicons) => {
      if (favicons && favicons.length > 0) {
        log.debug("[BrowserManager] Favicon updated", { tabId, favicon: favicons[0] });
        sendUpdate({ favicon: favicons[0] });
      }
    });

    // Load finish
    webContents.on("did-finish-load", () => {
      sendUpdate({
        isLoading: false,
        canGoBack: webContents.navigationHistory.canGoBack(),
        canGoForward: webContents.navigationHistory.canGoForward(),
      });
    });

    // Handle new window requests
    webContents.setWindowOpenHandler(({ url }) => {
      log.info("[BrowserManager] New window requested", { url });
      // For now, prevent new windows - could create new tab instead
      return { action: "deny" };
    });
  }

  /**
   * Update the bounds of the active view for the current active pane
   */
  updateActiveBounds() {
    if (!this.window || !this.activePaneId) {
      log.debug("[BrowserManager] Cannot update bounds - no window or active pane");
      return;
    }

    // Get the active tab for the current pane
    const activeTabId = this.activeTabByPane.get(this.activePaneId);
    if (!activeTabId) {
      log.debug("[BrowserManager] No active tab for pane", { paneId: this.activePaneId });
      return;
    }

    const view = this.views.get(activeTabId);
    if (!view) {
      log.warn("[BrowserManager] Active tab view not found", { tabId: activeTabId });
      return;
    }

    // Don't show the view if bounds are invalid
    if (this.bounds.width <= 0 || this.bounds.height <= 0) {
      log.warn("[BrowserManager] Invalid bounds, skipping view update", { bounds: this.bounds });
      return;
    }

    // Remove all other views first
    for (const [tabId, otherView] of this.views.entries()) {
      if (tabId !== activeTabId) {
        try {
          this.window.contentView.removeChildView(otherView);
        } catch (e) {
          // View might not be attached, that's fine
        }
      }
    }

    // Add and position the active pane's active tab
    try {
      // Remove it first in case it's already attached
      this.window.contentView.removeChildView(view);
    } catch (e) {
      // View might not be attached yet, that's fine
    }

    this.window.contentView.addChildView(view);
    view.setBounds(this.bounds);

    log.info("[BrowserManager] Updated active bounds", {
      paneId: this.activePaneId,
      tabId: activeTabId,
      bounds: this.bounds
    });
  }

  /**
   * Clean up all views
   */
  cleanup() {
    log.info("[BrowserManager] Cleaning up all tabs");

    for (const [tabId, view] of this.views.entries()) {
      try {
        if (!view.webContents.isDestroyed()) {
          view.webContents.close();
        }
      } catch (error) {
        log.error("[BrowserManager] Error closing tab", { tabId, error: error.message });
      }
    }

    this.views.clear();
    this.tabToPane.clear();
    this.activeTabByPane.clear();
    this.activePaneId = null;
    this.window = null;
  }

  /**
   * Get all active tab IDs
   * @returns {string[]} - Array of tab IDs
   */
  getActiveTabIds() {
    return Array.from(this.views.keys());
  }
}

module.exports = BrowserManager;
