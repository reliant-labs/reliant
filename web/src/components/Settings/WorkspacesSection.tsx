import { useState } from "react";
import { cn } from "../../lib/utils";
import { Settings, FolderGit2 } from "lucide-react";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { WorktreesPanel } from "../Worktrees/WorktreesPanel";
import { ArchivedWorktreesPanel } from "../Worktrees/ArchivedWorktreesPanel";
import { WorktreeDetailView } from "../Worktrees/WorktreeDetailView";
import { WorktreeSettings } from "./WorktreeSettings";

type WorkspacesTab = "active" | "archived" | "settings";

/**
 * WorkspacesSection - Unified workspaces management for settings
 * 
 * Combines:
 * - Active workspaces list + detail view
 * - Archived workspaces list + detail view  
 * - Workspace archive/cleanup settings
 */
export function WorkspacesSection() {
  const [activeTab, setActiveTab] = useState<WorkspacesTab>("active");
  const { activeDaemon } = useDaemonStatus();

  const tabs: Array<{ id: WorkspacesTab; label: string; icon: React.ReactNode }> = [
    { id: "active", label: "Active", icon: <FolderGit2 className="w-4 h-4" /> },
    { id: "archived", label: "Archived", icon: <FolderGit2 className="w-4 h-4 opacity-50" /> },
    { id: "settings", label: "Settings", icon: <Settings className="w-4 h-4" /> },
  ];

  return (
    <div className="flex flex-col h-full">
      {/* Tab Header */}
      <div className="flex border-b border-border bg-muted/20 flex-shrink-0">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "flex items-center gap-2 px-4 py-3 text-sm font-medium transition-colors border-b-2",
              activeTab === tab.id
                ? "text-foreground border-primary bg-background"
                : "text-muted-foreground border-transparent hover:text-foreground hover:bg-muted/50"
            )}
          >
            {tab.icon}
            {tab.label}
          </button>
        ))}
      </div>

      {/* Tab Content */}
      <div className="flex-1 overflow-hidden">
        {activeTab === "settings" ? (
          /* Settings tab - scrollable content */
          <div className="h-full overflow-auto">
            <div className="max-w-4xl mx-auto p-6">
              <WorktreeSettings />
            </div>
          </div>
        ) : (
          /* Active/Archived tabs - sidebar + detail view layout */
          <div className="flex h-full">
            {/* Left Sidebar - Workspace List */}
            <div className="w-64 flex-shrink-0 border-r border-border overflow-hidden">
              {activeTab === "active" ? (
                <WorktreesPanel daemonName={activeDaemon?.hostname} />
              ) : (
                <ArchivedWorktreesPanel />
              )}
            </div>

            {/* Right Side - Detail View */}
            <div className="flex-1 overflow-auto">
              <WorktreeDetailView />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}