import { useState, useEffect } from "react";
import { Globe, X, Plus, Star, Trash2 } from "lucide-react";
import { useBrowserStore, type BrowserTab } from "../../store/browserStore";
import { useBookmarksStore, type Bookmark } from "../../store/bookmarksStore";
import { useViewerStore } from "../../store/viewerStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { Tooltip } from "../ui/Tooltip";
import { toast } from "../../lib/toast-manager";
import { logger } from "../../lib/logger";
import {
  SidebarSection,
  SidebarEmptyState,
} from "../RightSidebar/shared";

interface BrowserSidebarContentProps {
  worktreeId?: string;
}

export function BrowserSidebarContent({ worktreeId }: BrowserSidebarContentProps) {
  const [bookmarksExpanded, setBookmarksExpanded] = useState(true);
  const [tabsExpanded, setTabsExpanded] = useState(true);

  // Browser tabs state
  const tabs = useBrowserStore((state) => state.tabs);
  const closeTab = useBrowserStore((state) => state.closeTab);
  const createTab = useBrowserStore((state) => state.createTab);
  const closeWorktreeTabs = useBrowserStore((state) => state.closeWorktreeTabs);
  
  // Bookmarks state
  const bookmarks = useBookmarksStore((state) => state.bookmarks);
  const loadBookmarks = useBookmarksStore((state) => state.loadBookmarks);
  const addBookmark = useBookmarksStore((state) => state.addBookmark);
  const removeBookmark = useBookmarksStore((state) => state.removeBookmark);
  
  // Viewer state
  const openBrowserViewer = useViewerStore((state) => state.openBrowserViewer);
  const viewers = useViewerStore((state) => state.viewers);
  const closeViewer = useViewerStore((state) => state.closeViewer);
  const currentProject = useProjectStore((state) => state.currentProject);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const worktrees = useWorktreeStore((state) => state.worktrees);

  // Filter tabs by worktree
  const worktreeTabs = worktreeId 
    ? tabs.filter(t => t.worktreeId === worktreeId)
    : tabs;

  // Load bookmarks on mount
  useEffect(() => {
    loadBookmarks().catch((error) => {
      logger.error("[BrowserSidebarContent] Failed to load bookmarks:", error);
    });
  }, [loadBookmarks]);

  const handleTabClick = async (tab: BrowserTab) => {
    if (!currentProject?.id || !worktreeId) return;
    
    // Switch context to this worktree so the viewer is visible
    const targetWorktree = worktrees.find(w => w.id === worktreeId);
    if (targetWorktree) {
      await switchWorktreeContext(currentProject.id, targetWorktree);
    }

    // Open the tab in the viewer panel
    await openBrowserViewer(currentProject.id, worktreeId, tab.id);
  };

  const handleCloseTab = (e: React.MouseEvent, tabId: string) => {
    e.stopPropagation();
    
    // Also close the corresponding viewer if it exists
    // Use skipBrowserTabClose since we're handling that ourselves
    const browserViewer = viewers.find(
      (v) => v.type === "browser" && (v as any).browserTabId === tabId
    );
    if (browserViewer) {
      closeViewer(browserViewer.id, { skipBrowserTabClose: true });
    }
    
    closeTab(tabId);
  };

  const handleBookmarkClick = async (bookmark: Bookmark) => {
    if (!currentProject?.id || !worktreeId) return;
    
    // Switch context to this worktree so the viewer is visible
    const targetWorktree = worktrees.find(w => w.id === worktreeId);
    if (targetWorktree) {
      await switchWorktreeContext(currentProject.id, targetWorktree);
    }

    // Create a new browser tab with this URL and open it
    const tabId = await createTab(worktreeId, bookmark.url, currentProject.id);
    await openBrowserViewer(currentProject.id, worktreeId, tabId);
  };

  const handleAddBookmarkFromTab = async (tab: BrowserTab) => {
    try {
      await addBookmark({
        title: tab.title || "Untitled",
        url: tab.url,
        favicon: tab.favicon,
      });
      toast.success("Bookmark added");
    } catch {
      toast.error("Failed to add bookmark");
    }
  };

  const handleRemoveBookmark = async (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    try {
      await removeBookmark(id);
      toast.success("Bookmark removed");
    } catch {
      toast.error("Failed to remove bookmark");
    }
  };

  const handleNewTab = async () => {
    if (!currentProject?.id || !worktreeId) return;
    
    // Switch context to this worktree so the viewer is visible
    const targetWorktree = worktrees.find(w => w.id === worktreeId);
    if (targetWorktree) {
      await switchWorktreeContext(currentProject.id, targetWorktree);
    }

    const tabId = await createTab(worktreeId, undefined, currentProject.id);
    await openBrowserViewer(currentProject.id, worktreeId, tabId);
  };

  const handleCloseAllTabs = () => {
    if (!worktreeId) return;
    
    // Close all browser viewers for these tabs first
    worktreeTabs.forEach(tab => {
      const browserViewer = viewers.find(
        (v) => v.type === "browser" && (v as any).browserTabId === tab.id
      );
      if (browserViewer) {
        closeViewer(browserViewer.id, { skipBrowserTabClose: true });
      }
    });
    
    // Then close all browser tabs for this worktree
    closeWorktreeTabs(worktreeId);
    toast.success("All tabs closed");
  };

  // Check if a URL is already bookmarked
  const isBookmarked = (url: string) => bookmarks.some(b => b.url === url);

  // Get hostname for display
  const getHostname = (url: string) => {
    try {
      return new URL(url).hostname;
    } catch {
      return url;
    }
  };

  return (
    <div className="flex flex-col h-full">
      <div className="flex-1 overflow-y-auto">
        {/* Open Tabs Section */}
        <SidebarSection
          title="Open Tabs"
          count={worktreeTabs.length > 0 ? worktreeTabs.length : undefined}
          isExpanded={tabsExpanded}
          onToggle={() => setTabsExpanded(!tabsExpanded)}
          actions={
            <>
              <Tooltip content="New tab" placement="left">
                <button
                  onClick={handleNewTab}
                  className="p-1 text-muted-foreground hover:text-foreground hover:bg-muted rounded transition-colors"
                >
                  <Plus className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
              {worktreeTabs.length > 1 && (
                <Tooltip content="Close all tabs" placement="left">
                  <button
                    onClick={handleCloseAllTabs}
                    className="p-1 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded transition-colors"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              )}
            </>
          }
        >
          {worktreeTabs.length === 0 ? (
            <SidebarEmptyState
              icon={Globe}
              title="No open tabs"
              description='Click "+" to start browsing'
              size="sm"
            />
          ) : (
            <div className="space-y-0.5 px-1">
              {worktreeTabs.map((tab) => (
                <div
                  key={tab.id}
                  onClick={() => handleTabClick(tab)}
                  className="group flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-muted/50 cursor-pointer transition-colors"
                >
                  {/* Favicon */}
                  <div className="w-4 h-4 flex-shrink-0">
                    {tab.favicon ? (
                      <img
                        src={tab.favicon}
                        alt=""
                        className="w-4 h-4 rounded"
                        onError={(e) => {
                          (e.target as HTMLImageElement).style.display = "none";
                        }}
                      />
                    ) : (
                      <Globe className="w-4 h-4 text-muted-foreground" />
                    )}
                  </div>

                  {/* Title and URL */}
                  <div className="flex-1 min-w-0">
                    <div className="text-sm truncate">
                      {tab.title || "New Tab"}
                    </div>
                    <div className="text-xs text-muted-foreground truncate">
                      {getHostname(tab.url)}
                    </div>
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                    {!isBookmarked(tab.url) && (
                      <Tooltip content="Add to bookmarks" placement="left">
                        <button
                          onClick={(e) => {
                            e.stopPropagation();
                            handleAddBookmarkFromTab(tab);
                          }}
                          className="p-1 rounded hover:bg-muted"
                        >
                          <Star className="w-3 h-3" />
                        </button>
                      </Tooltip>
                    )}
                    <Tooltip content="Close tab" placement="left">
                      <button
                        onClick={(e) => handleCloseTab(e, tab.id)}
                        className="p-1 rounded hover:bg-destructive/20 hover:text-destructive"
                      >
                        <X className="w-3 h-3" />
                      </button>
                    </Tooltip>
                  </div>
                </div>
              ))}
            </div>
          )}
        </SidebarSection>

        {/* Bookmarks Section */}
        <SidebarSection
          title="Bookmarks"
          icon={<Star className="w-3 h-3" />}
          count={bookmarks.length > 0 ? bookmarks.length : undefined}
          isExpanded={bookmarksExpanded}
          onToggle={() => setBookmarksExpanded(!bookmarksExpanded)}
        >
          {bookmarks.length === 0 ? (
            <SidebarEmptyState
              icon={Star}
              title="No bookmarks yet"
              description="Click the star icon on any tab to bookmark it"
              size="sm"
            />
          ) : (
            <div className="space-y-0.5 px-1">
              {bookmarks.map((bookmark) => (
                <div
                  key={bookmark.id}
                  onClick={() => handleBookmarkClick(bookmark)}
                  className="group flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-muted/50 cursor-pointer transition-colors"
                >
                  {/* Favicon */}
                  <div className="w-4 h-4 flex-shrink-0">
                    {bookmark.favicon ? (
                      <img
                        src={bookmark.favicon}
                        alt=""
                        className="w-4 h-4 rounded"
                        onError={(e) => {
                          (e.target as HTMLImageElement).style.display = "none";
                        }}
                      />
                    ) : (
                      <Star className="w-4 h-4 text-amber-500" />
                    )}
                  </div>

                  {/* Title and URL */}
                  <div className="flex-1 min-w-0">
                    <div className="text-sm truncate">
                      {bookmark.title}
                    </div>
                    <div className="text-xs text-muted-foreground truncate">
                      {getHostname(bookmark.url)}
                    </div>
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                    <Tooltip content="Remove bookmark" placement="left">
                      <button
                        onClick={(e) => handleRemoveBookmark(e, bookmark.id)}
                        className="p-1 rounded hover:bg-destructive/20 hover:text-destructive"
                      >
                        <Trash2 className="w-3 h-3" />
                      </button>
                    </Tooltip>
                  </div>
                </div>
              ))}
            </div>
          )}
        </SidebarSection>
      </div>
    </div>
  );
}
