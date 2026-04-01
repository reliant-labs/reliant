import { Plus } from "lucide-react";
import { BrowserTab } from "./BrowserTab";
import { useBrowserStore } from "../../store/browserStore";
import { Tooltip } from "../ui/Tooltip";

interface BrowserTabBarProps {
  worktreeId: string; // Required: workspace for new tabs
}

export function BrowserTabBar({ worktreeId }: BrowserTabBarProps) {
  const tabs = useBrowserStore((state) => state.tabs);
  const activeTabId = useBrowserStore((state) => state.activeTabId);
  const createTab = useBrowserStore((state) => state.createTab);
  const closeTab = useBrowserStore((state) => state.closeTab);
  const setActiveTab = useBrowserStore((state) => state.setActiveTab);
  
  // Filter tabs to show only this workspace's tabs
  const worktreeTabs = tabs.filter(t => t.worktreeId === worktreeId);

  const handleCreateTab = async () => {
    await createTab(worktreeId);
  };

  const handleCloseTab = (e: React.MouseEvent, tabId: string) => {
    e.stopPropagation();
    closeTab(tabId);
  };

  return (
    <div className="flex items-center bg-muted/30 border-b border-border overflow-x-auto overflow-y-hidden">
      {/* Tabs */}
      <div className="flex flex-1 overflow-x-auto overflow-y-hidden scrollbar-hide">
        {worktreeTabs.map((tab) => (
          <BrowserTab
            key={tab.id}
            id={tab.id}
            title={tab.title}
            favicon={tab.favicon}
            isActive={tab.id === activeTabId}
            isLoading={tab.isLoading}
            onSelect={() => setActiveTab(tab.id)}
            onClose={(e) => handleCloseTab(e, tab.id)}
          />
        ))}
      </div>

      {/* New tab button */}
      <div className="flex-shrink-0 border-l border-border">
        <Tooltip content="New Tab (⌘T)" placement="bottom" delay={300}>
          <button
            onClick={handleCreateTab}
            className="p-2 hover:bg-accent transition-colors"
            aria-label="New Tab"
          >
            <Plus className="w-4 h-4" />
          </button>
        </Tooltip>
      </div>
    </div>
  );
}
