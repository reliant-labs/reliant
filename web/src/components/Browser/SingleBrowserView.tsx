import { useState, useEffect, useRef } from "react";
import { ChevronLeft, ChevronRight, RotateCw, Search, Lock, Code } from "lucide-react";
import { cn } from "../../lib/utils";
import { useBrowserStore } from "../../store/browserStore";
import { useViewerStore } from "../../store/viewerStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { logger } from "../../lib/logger";

interface SingleBrowserViewProps {
  tabId: string;
  viewerId?: string; // Optional viewer ID to update title
}

/**
 * SingleBrowserView - Shows a single browser tab full screen
 * Used within the viewer panel tab system
 */
export function SingleBrowserView({ tabId, viewerId }: SingleBrowserViewProps) {
  const [addressBarValue, setAddressBarValue] = useState("");
  const [isAddressBarFocused, setIsAddressBarFocused] = useState(false);
  const addressBarRef = useRef<HTMLInputElement>(null);
  const webviewRef = useRef<HTMLElement | null>(null);
  const initializedRef = useRef(false);
  const initialUrlRef = useRef<string>("");

  // Get this specific browser tab
  const tabs = useBrowserStore((state) => state.tabs);
  const updateTabUrl = useBrowserStore((state) => state.updateTabUrl);
  const updateTabTitle = useBrowserStore((state) => state.updateTabTitle);
  const updateTabFavicon = useBrowserStore((state) => state.updateTabFavicon);
  const updateViewerTitle = useViewerStore((state) => state.updateViewerTitle);
  const updateTabLoading = useBrowserStore((state) => state.updateTabLoading);
  const updateTabNavigation = useBrowserStore((state) => state.updateTabNavigation);
  const createTab = useBrowserStore((state) => state.createTab);
  const openBrowserViewer = useViewerStore((state) => state.openBrowserViewer);
  
  // Subscribe to global resize state to disable pointer events during resize
  const isResizing = useSidebarStore((state) => state.isResizing);
  
  // Get current project/worktree context for opening new tabs
  const currentProject = useProjectStore((state) => state.currentProject);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);

  const tab = tabs.find((t) => t.id === tabId);

  // Reset initialUrlRef when tabId changes (component being reused for different tab)
  useEffect(() => {
    initialUrlRef.current = "";
    initializedRef.current = false;
  }, [tabId]);

  // Update address bar when tab URL changes
  useEffect(() => {
    if (tab && !isAddressBarFocused) {
      setAddressBarValue(tab.url);
    }
  }, [tab, isAddressBarFocused]);

  // Set up webview event listeners
  useEffect(() => {
    if (!tab || !webviewRef.current) return;

    const webview = webviewRef.current as any;

    const handleLoadStart = () => {
      logger.debug("[SingleBrowserView] Load started", { tabId });
      updateTabLoading(tabId, true);
    };

    const handleLoadStop = () => {
      logger.debug("[SingleBrowserView] Load stopped", { tabId });
      updateTabLoading(tabId, false);

      // Guard: Electron <webview> APIs are absent in plain-browser (web) builds.
      if (typeof webview.getURL !== "function") return;

      // Update URL
      const url = webview.getURL();
      updateTabUrl(tabId, url);

      // Update title
      const title = webview.getTitle();
      if (title) {
        updateTabTitle(tabId, title);
        // Also update viewer title if viewerId provided
        if (viewerId) {
          updateViewerTitle(viewerId, title);
        }
      }

      // Update navigation state
      updateTabNavigation(tabId, webview.canGoBack(), webview.canGoForward());
    };

    const handlePageTitleUpdated = (e: any) => {
      logger.debug("[SingleBrowserView] Title updated", { tabId, title: e.title });
      updateTabTitle(tabId, e.title);
      // Also update viewer title if viewerId provided
      if (viewerId) {
        updateViewerTitle(viewerId, e.title);
      }
    };

    const handlePageFaviconUpdated = (e: any) => {
      if (e.favicons && e.favicons.length > 0) {
        logger.debug("[SingleBrowserView] Favicon updated", { tabId });
        updateTabFavicon(tabId, e.favicons[0]);
      }
    };

    const handleDidNavigate = (e: any) => {
      logger.debug("[SingleBrowserView] Navigation", { tabId, url: e.url });
      updateTabUrl(tabId, e.url);
      if (typeof webview.canGoBack === "function") {
        updateTabNavigation(tabId, webview.canGoBack(), webview.canGoForward());
      }
    };

    // Inject JavaScript to intercept target="_blank" links and window.open calls
    // This is needed because webview's new-window event doesn't fire reliably in modern Electron
    const handleDomReady = () => {
      const injectedScript = `
        (function() {
          if (window.__reliantClickInterceptorInstalled) return;
          window.__reliantClickInterceptorInstalled = true;
          
          // Intercept clicks on links with target="_blank" or ctrl/cmd+click
          document.addEventListener('click', function(e) {
            const link = e.target.closest('a');
            if (link && (link.target === '_blank' || (e.metaKey || e.ctrlKey))) {
              const href = link.href;
              if (href && href.startsWith('http')) {
                e.preventDefault();
                e.stopPropagation();
              }
            }
          }, true);
          
          // Intercept window.open calls
          const originalWindowOpen = window.open;
          window.open = function(url, target, features) {
            if (url && url.startsWith('http')) {
              return null;
            }
            return originalWindowOpen.call(this, url, target, features);
          };
        })();
      `;
      
      if (typeof webview.executeJavaScript !== "function") return;
      webview.executeJavaScript(injectedScript).catch((err: Error) => {
        logger.error("[SingleBrowserView] Failed to inject click interceptor", { error: err.message });
      });
    };
    
    // Listen for console messages to catch intercepted URLs and open in new tabs
    const handleConsoleMessage = async (e: any) => {
      const message = e.message;
      if (message && message.includes('[Reliant] Intercepted')) {
        const urlMatch = message.match(/https?:\/\/[^\s,]+/);
        if (urlMatch) {
          const url = urlMatch[0];
          
          // Open in a new tab within Reliant
          if (currentProject?.id && currentWorktree?.id) {
            try {
              const browserTabId = await createTab(currentWorktree.id, url, currentProject.id);
              openBrowserViewer(currentProject.id, currentWorktree.id, browserTabId);
            } catch (err) {
              // Fallback: navigate current tab
              if (typeof webview.loadURL === "function") webview.loadURL(url);
            }
          } else {
            // No context for new tab, navigate current tab
            if (typeof webview.loadURL === "function") webview.loadURL(url);
          }
        }
      }
    };

    // Attach listeners
    webview.addEventListener("did-start-loading", handleLoadStart);
    webview.addEventListener("did-stop-loading", handleLoadStop);
    webview.addEventListener("page-title-updated", handlePageTitleUpdated);
    webview.addEventListener("page-favicon-updated", handlePageFaviconUpdated);
    webview.addEventListener("did-navigate", handleDidNavigate);
    webview.addEventListener("did-navigate-in-page", handleDidNavigate);
    webview.addEventListener("dom-ready", handleDomReady);
    webview.addEventListener("console-message", handleConsoleMessage);

    return () => {
      webview.removeEventListener("did-start-loading", handleLoadStart);
      webview.removeEventListener("did-stop-loading", handleLoadStop);
      webview.removeEventListener("page-title-updated", handlePageTitleUpdated);
      webview.removeEventListener("page-favicon-updated", handlePageFaviconUpdated);
      webview.removeEventListener("did-navigate", handleDidNavigate);
      webview.removeEventListener("did-navigate-in-page", handleDidNavigate);
      webview.removeEventListener("dom-ready", handleDomReady);
      webview.removeEventListener("console-message", handleConsoleMessage);
    };
  }, [tab, tabId, updateTabUrl, updateTabTitle, updateTabFavicon, updateTabLoading, updateTabNavigation, currentProject, currentWorktree, createTab, openBrowserViewer, updateViewerTitle, viewerId]);

  const handleNavigate = (e: React.FormEvent) => {
    e.preventDefault();
    if (!tab || !addressBarValue.trim()) return;

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

    const webview = webviewRef.current as any;
    if (webview && typeof webview.loadURL === "function") {
      webview.loadURL(url);
    }
    addressBarRef.current?.blur();
  };

  const handleGoBack = () => {
    if (tab?.canGoBack) {
      const webview = webviewRef.current as any;
      if (typeof webview?.goBack === "function") webview.goBack();
    }
  };

  const handleGoForward = () => {
    if (tab?.canGoForward) {
      const webview = webviewRef.current as any;
      if (typeof webview?.goForward === "function") webview.goForward();
    }
  };

  const handleReload = () => {
    if (tab) {
      const webview = webviewRef.current as any;
      if (typeof webview?.reload === "function") webview.reload();
    }
  };

  const handleOpenDevTools = () => {
    if (tab) {
      const webview = webviewRef.current as any;
      if (webview) {
        try {
          if (typeof webview.openDevTools === "function") {
            webview.openDevTools();
          } else {
            const webContents = (webview as any).getWebContents?.();
            if (webContents && typeof webContents.openDevTools === "function") {
              webContents.openDevTools();
            }
          }
        } catch (error) {
          console.error("[SingleBrowserView] Error opening DevTools", error);
        }
      }
    }
  };

  if (!tab) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted-foreground">
        <p>Browser tab not found</p>
      </div>
    );
  }

  const isSecure = tab.url.startsWith("https://");

  // Store initial URL on first render
  if (!initialUrlRef.current && tab.url) {
    let cleanUrl = tab.url;
    if (cleanUrl.includes("google.com") && cleanUrl.includes("zx=")) {
      cleanUrl = cleanUrl.split("?")[0];
    }
    initialUrlRef.current = cleanUrl;
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-background h-full">
      {/* Navigation Controls and Address Bar */}
      <div className="flex items-center gap-2 px-2 py-1.5 bg-muted/10 border-b border-border flex-shrink-0">
        {/* Navigation Buttons */}
        <button
          onClick={handleGoBack}
          disabled={!tab.canGoBack}
          className={cn(
            "p-1.5 rounded transition-colors",
            tab.canGoBack
              ? "hover:bg-accent text-foreground"
              : "text-muted-foreground/40 cursor-not-allowed"
          )}
          title="Go Back"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>

        <button
          onClick={handleGoForward}
          disabled={!tab.canGoForward}
          className={cn(
            "p-1.5 rounded transition-colors",
            tab.canGoForward
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
          <RotateCw className={cn("w-4 h-4", tab.isLoading && "animate-spin")} />
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
                if (tab) {
                  setAddressBarValue(tab.url);
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

      {/* Browser Content Area - Full Screen */}
      <div className="flex-1 min-h-0 bg-background">
        <webview
          key={tabId} // Force remount when tab changes
          src={initialUrlRef.current || tab.url}
          ref={(el) => {
            if (el) {
              webviewRef.current = el;
            }
          }}
          style={{
            width: "100%",
            height: "100%",
            display: "flex",
            // Disable pointer events during resize to prevent webview from capturing mouse events
            pointerEvents: isResizing ? "none" : "auto",
          }}
          partition={`persist:browser-${tab.worktreeId}`}
          allowpopups={true}
        />
      </div>
    </div>
  );
}
