import { useState, useEffect, useRef, useMemo } from "react";
import { Globe, Plus, X, ChevronLeft, ChevronRight, RotateCw, Search, Lock, Code } from "lucide-react";
import { cn } from "../../lib/utils";
import { useBrowserStore } from "../../store/browserStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { logger } from "../../lib/logger";

interface SimpleBrowserViewProps {
  worktreeId?: string; // Optional - will use main worktree if not provided
}

/**
 * SimpleBrowserView - A standalone browser component
 * Requires worktreeId for workspace-scoped browser sessions
 */
export function SimpleBrowserView({ worktreeId }: SimpleBrowserViewProps) {
  const [selectedTabId, setSelectedTabId] = useState<string | null>(null);
  const [addressBarValue, setAddressBarValue] = useState("");
  const [isAddressBarFocused, setIsAddressBarFocused] = useState(false);
  const hasAutoSelected = useRef(false);
  const hasAutoCreated = useRef(false);
  const addressBarRef = useRef<HTMLInputElement>(null);
  const webviewRefs = useRef<Map<string, HTMLElement>>(new Map());
  const initializedWebviews = useRef<Set<string>>(new Set());
  const initialUrls = useRef<Map<string, string>>(new Map());

  // Get browser tabs and actions
  const tabs = useBrowserStore((state) => state.tabs);
  const createTab = useBrowserStore((state) => state.createTab);
  const closeTab = useBrowserStore((state) => state.closeTab);
  const updateTabUrl = useBrowserStore((state) => state.updateTabUrl);
  const updateTabTitle = useBrowserStore((state) => state.updateTabTitle);
  const updateTabFavicon = useBrowserStore((state) => state.updateTabFavicon);
  const updateTabLoading = useBrowserStore((state) => state.updateTabLoading);
  const updateTabNavigation = useBrowserStore((state) => state.updateTabNavigation);

  // Get current project for associating tabs
  const currentProject = useProjectStore((state) => state.currentProject);
  
  // Get worktrees to find main worktree as fallback
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const mainWorktree = worktrees.find(w => w.is_main && w.project_id === currentProject?.id);
  const effectiveWorktreeId = worktreeId || mainWorktree?.id || '';
  
  // Subscribe to global resize state to disable pointer events during resize
  const isResizing = useSidebarStore((state) => state.isResizing);

  // Filter tabs for this worktree (workspace-scoped)
  const projectTabs = useMemo(
    () => effectiveWorktreeId
      ? tabs.filter((t) => t.worktreeId === effectiveWorktreeId && !t.paneId)
      : [],
    [effectiveWorktreeId, tabs]
  );

  // Find the selected tab
  const selectedTab = selectedTabId
    ? projectTabs.find((t) => t.id === selectedTabId)
    : null;

  // Update address bar when tab changes
  useEffect(() => {
    if (selectedTab && !isAddressBarFocused) {
      setAddressBarValue(selectedTab.url);
    }
  }, [selectedTab?.url, selectedTab, isAddressBarFocused]);

  // Auto-create and select first tab on mount if no tabs exist
  useEffect(() => {
    let cancelled = false;
    let focusTimer: ReturnType<typeof setTimeout> | undefined;
    if (!hasAutoCreated.current && projectTabs.length === 0 && effectiveWorktreeId) {
      const projectId = currentProject?.id;
      createTab(effectiveWorktreeId, undefined, projectId).then((tabId) => {
        if (cancelled) return;
        setSelectedTabId(tabId);
        hasAutoCreated.current = true;
        hasAutoSelected.current = true;
        logger.info("[SimpleBrowserView] Auto-created first tab", { tabId, worktreeId: effectiveWorktreeId });
        // Focus address bar when auto-creating first tab
        focusTimer = setTimeout(() => {
          addressBarRef.current?.focus();
        }, 100);
      });
    } else if (!hasAutoSelected.current && !selectedTabId && projectTabs.length > 0) {
      // If tabs exist but none selected, select the first one
      setSelectedTabId(projectTabs[0].id);
      hasAutoSelected.current = true;
      logger.info("[SimpleBrowserView] Auto-selected first existing tab", {
        tabId: projectTabs[0].id,
      });
    }
    return () => {
      cancelled = true;
      clearTimeout(focusTimer);
    };
  }, [projectTabs, selectedTabId, currentProject?.id, effectiveWorktreeId, createTab]);

  // Set up webview event listeners when a tab is selected
  useEffect(() => {
    if (!selectedTab) return;

    const webview = webviewRefs.current.get(selectedTab.id) as any;
    if (!webview) return;

    const handleLoadStart = () => {
      logger.debug("[SimpleBrowserView] Load started", { tabId: selectedTab.id });
      updateTabLoading(selectedTab.id, true);
    };

    const handleLoadStop = () => {
      logger.debug("[SimpleBrowserView] Load stopped", { tabId: selectedTab.id });
      updateTabLoading(selectedTab.id, false);

      // Guard: Electron <webview> APIs are absent in plain-browser (web) builds.
      if (typeof webview.getURL !== "function") return;

      // Update URL
      const url = webview.getURL();
      updateTabUrl(selectedTab.id, url);

      // Update title
      const title = webview.getTitle();
      if (title) {
        updateTabTitle(selectedTab.id, title);
      }

      // Update navigation state
      updateTabNavigation(
        selectedTab.id,
        webview.canGoBack(),
        webview.canGoForward()
      );
    };

    const handlePageTitleUpdated = (e: any) => {
      logger.debug("[SimpleBrowserView] Title updated", {
        tabId: selectedTab.id,
        title: e.title,
      });
      updateTabTitle(selectedTab.id, e.title);
    };

    const handlePageFaviconUpdated = (e: any) => {
      if (e.favicons && e.favicons.length > 0) {
        logger.debug("[SimpleBrowserView] Favicon updated", {
          tabId: selectedTab.id,
        });
        updateTabFavicon(selectedTab.id, e.favicons[0]);
      }
    };

    const handleDidNavigate = (e: any) => {
      logger.debug("[SimpleBrowserView] Navigation", {
        tabId: selectedTab.id,
        url: e.url,
      });
      updateTabUrl(selectedTab.id, e.url);
      if (typeof webview.canGoBack === "function") {
        updateTabNavigation(
          selectedTab.id,
          webview.canGoBack(),
          webview.canGoForward()
        );
      }
    };

    // Attach listeners
    webview.addEventListener("did-start-loading", handleLoadStart);
    webview.addEventListener("did-stop-loading", handleLoadStop);
    webview.addEventListener("page-title-updated", handlePageTitleUpdated);
    webview.addEventListener("page-favicon-updated", handlePageFaviconUpdated);
    webview.addEventListener("did-navigate", handleDidNavigate);
    webview.addEventListener("did-navigate-in-page", handleDidNavigate);

    return () => {
      webview.removeEventListener("did-start-loading", handleLoadStart);
      webview.removeEventListener("did-stop-loading", handleLoadStop);
      webview.removeEventListener("page-title-updated", handlePageTitleUpdated);
      webview.removeEventListener(
        "page-favicon-updated",
        handlePageFaviconUpdated
      );
      webview.removeEventListener("did-navigate", handleDidNavigate);
      webview.removeEventListener("did-navigate-in-page", handleDidNavigate);
    };
  }, [
    selectedTab,
    updateTabUrl,
    updateTabTitle,
    updateTabFavicon,
    updateTabLoading,
    updateTabNavigation,
  ]);

  const handleCreateTab = async () => {
    if (!effectiveWorktreeId) return;
    const projectId = currentProject?.id;
    const tabId = await createTab(effectiveWorktreeId, undefined, projectId);
    setSelectedTabId(tabId);
    // Focus address bar after a short delay to ensure DOM is ready
    setTimeout(() => {
      addressBarRef.current?.focus();
    }, 100);
  };

  const handleCloseTab = (tabId: string, e?: React.MouseEvent) => {
    e?.stopPropagation();
    closeTab(tabId);
    if (selectedTabId === tabId) {
      const remainingTabs = projectTabs.filter((t) => t.id !== tabId);
      if (remainingTabs.length > 0) {
        setSelectedTabId(remainingTabs[0].id);
        hasAutoSelected.current = true;
      } else {
        setSelectedTabId(null);
        hasAutoSelected.current = false;
      }
    }
  };

  const handleNavigate = (e: React.FormEvent) => {
    e.preventDefault();
    if (!selectedTab || !addressBarValue.trim()) return;

    let url = addressBarValue.trim();

    // Add protocol if missing
    if (!url.match(/^https?:\/\//i)) {
      // Check if it looks like a domain
      if (url.includes(".") && !url.includes(" ")) {
        url = `https://${url}`;
      } else {
        // Treat as search query
        url = `https://www.google.com/search?q=${encodeURIComponent(url)}`;
      }
    }

    const webview = webviewRefs.current.get(selectedTab.id) as any;
    if (webview && typeof webview.loadURL === "function") {
      webview.loadURL(url);
    }
    addressBarRef.current?.blur();
  };

  const handleGoBack = () => {
    if (selectedTab?.canGoBack) {
      const webview = webviewRefs.current.get(selectedTab.id) as any;
      if (typeof webview?.goBack === "function") webview.goBack();
    }
  };

  const handleGoForward = () => {
    if (selectedTab?.canGoForward) {
      const webview = webviewRefs.current.get(selectedTab.id) as any;
      if (typeof webview?.goForward === "function") webview.goForward();
    }
  };

  const handleReload = () => {
    if (selectedTab) {
      const webview = webviewRefs.current.get(selectedTab.id) as any;
      if (typeof webview?.reload === "function") webview.reload();
    }
  };

  const handleOpenDevTools = () => {
    if (selectedTab) {
      const webview = webviewRefs.current.get(selectedTab.id) as any;
      if (webview) {
        try {
          // Try to open DevTools directly on the webview
          if (typeof webview.openDevTools === "function") {
            webview.openDevTools();
          } else {
            // Fallback: try to access webContents
            const webContents = (webview as any).getWebContents?.();
            if (webContents && typeof webContents.openDevTools === "function") {
              webContents.openDevTools();
            } else {
              console.warn(
                "[SimpleBrowserView] Could not open DevTools - method not available"
              );
            }
          }
        } catch (error) {
          console.error("[SimpleBrowserView] Error opening DevTools", error);
        }
      }
    }
  };

  const isSecure = selectedTab?.url.startsWith("https://") || false;

  // Browser view with tabs and controls
  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-background">
      {/* Tab Bar */}
      <div className="flex items-center border-b border-border bg-card/30 flex-shrink-0 overflow-x-auto">
        <div className="flex items-center flex-1 min-w-0">
          {projectTabs.length === 0 && (
            <div className="px-3 py-1.5 text-xs text-muted-foreground">
              No browser tabs open
            </div>
          )}

          {projectTabs.map((tab) => {
            const isActive = selectedTabId === tab.id;
            return (
              <button
                key={tab.id}
                onClick={() => setSelectedTabId(tab.id)}
                className={cn(
                  "flex items-center gap-2 px-3 py-1.5 text-xs font-medium transition-all duration-200",
                  "border-r border-border flex-shrink-0 min-w-0 max-w-[200px]",
                  isActive
                    ? "bg-background border-b-2 border-b-primary"
                    : "bg-transparent hover:bg-muted/50"
                )}
              >
                {tab.isLoading ? (
                  <RotateCw className="w-3 h-3 animate-spin flex-shrink-0" />
                ) : (
                  <Globe className="w-3 h-3 flex-shrink-0" />
                )}
                <span className="truncate">{tab.title}</span>
                <X
                  className="w-3 h-3 flex-shrink-0 hover:text-destructive"
                  onClick={(e) => handleCloseTab(tab.id, e)}
                />
              </button>
            );
          })}
        </div>

        {/* New Tab Button */}
        <button
          onClick={handleCreateTab}
          className="px-3 py-1.5 hover:bg-accent transition-colors border-l border-border flex-shrink-0"
          title="New Tab"
        >
          <Plus className="w-3.5 h-3.5" />
        </button>
      </div>

      {/* Navigation Controls and Address Bar */}
      {selectedTab && (
        <div className="flex items-center gap-2 px-2 py-1.5 bg-muted/10 border-b border-border flex-shrink-0">
          {/* Navigation Buttons */}
          <button
            onClick={handleGoBack}
            disabled={!selectedTab.canGoBack}
            className={cn(
              "p-1.5 rounded transition-colors",
              selectedTab.canGoBack
                ? "hover:bg-accent text-foreground"
                : "text-muted-foreground/40 cursor-not-allowed"
            )}
            title="Go Back"
          >
            <ChevronLeft className="w-4 h-4" />
          </button>

          <button
            onClick={handleGoForward}
            disabled={!selectedTab.canGoForward}
            className={cn(
              "p-1.5 rounded transition-colors",
              selectedTab.canGoForward
                ? "hover:bg-accent text-foreground"
                : "text-muted-foreground/40 cursor-not-allowed"
            )}
            title="Go Forward"
          >
            <ChevronRight className="w-4 h-4" />
          </button>

          <button
            onClick={handleReload}
            className="p-1.5 rounded hover:bg-accent transition-colors"
            title="Reload"
          >
            <RotateCw
              className={cn("w-4 h-4", selectedTab.isLoading && "animate-spin")}
            />
          </button>

          {/* Address Bar */}
          <form onSubmit={handleNavigate} className="flex-1 flex items-center">
            <div className="flex-1 flex items-center gap-2 px-3 py-1.5 bg-background border rounded">
              {/* Security Icon */}
              {isSecure ? (
                <Lock className="w-3.5 h-3.5 text-green-600 flex-shrink-0" />
              ) : (
                <Search className="w-4 h-4 text-muted-foreground flex-shrink-0" />
              )}

              {/* URL Input */}
              <input
                ref={addressBarRef}
                type="text"
                value={addressBarValue}
                onChange={(e) => setAddressBarValue(e.target.value)}
                onFocus={() => {
                  setIsAddressBarFocused(true);
                  addressBarRef.current?.select();
                }}
                onBlur={() => {
                  setIsAddressBarFocused(false);
                  if (selectedTab) {
                    setAddressBarValue(selectedTab.url);
                  }
                }}
                placeholder="Search or enter address"
                className="flex-1 bg-transparent text-sm outline-none"
              />
            </div>
          </form>

          <button
            onClick={handleOpenDevTools}
            className="p-1.5 rounded hover:bg-accent transition-colors"
            title="Open DevTools"
          >
            <Code className="w-4 h-4" />
          </button>
        </div>
      )}

      {/* Browser Content Area */}
      <div
        className="flex-1 min-h-0 bg-background"
        style={{ display: "flex", flexDirection: "column" }}
      >
        {projectTabs.length === 0 ? (
          <div className="h-full flex items-center justify-center text-muted-foreground">
            <div className="text-center">
              <Globe className="w-12 h-12 mx-auto mb-2 opacity-50" />
              <p className="text-sm mb-4">No browser tab selected</p>
              <button
                onClick={handleCreateTab}
                className="px-4 py-2 bg-primary text-primary-foreground rounded hover:bg-primary/90"
              >
                Create New Tab
              </button>
            </div>
          </div>
        ) : (
          <>
            {projectTabs.map((tab) => {
              // Store initial URL for this tab, never update it
              if (!initialUrls.current.has(tab.id)) {
                // Clean Google URLs to remove problematic query params
                let cleanUrl = tab.url;
                if (cleanUrl.includes("google.com") && cleanUrl.includes("zx=")) {
                  cleanUrl = cleanUrl.split("?")[0];
                }
                initialUrls.current.set(tab.id, cleanUrl);
              }
              const initialUrl = initialUrls.current.get(tab.id)!;

              return (
                <webview
                  key={tab.id}
                  src={initialUrl}
                  ref={(el) => {
                    if (el) {
                      webviewRefs.current.set(tab.id, el);
                    } else {
                      webviewRefs.current.delete(tab.id);
                      initializedWebviews.current.delete(tab.id);
                      initialUrls.current.delete(tab.id);
                    }
                  }}
                  style={{
                    flex: 1,
                    width: "100%",
                    display: selectedTabId === tab.id ? "flex" : "none",
                    // Disable pointer events during resize to prevent webview from capturing mouse events
                    pointerEvents: isResizing ? "none" : "auto",
                  }}
                  partition={`persist:browser-${tab.worktreeId}`}
                  allowpopups={true}
                />
              );
            })}
          </>
        )}
      </div>
    </div>
  );
}
