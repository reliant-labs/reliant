import { useEffect, useMemo, useState } from "react";
import { Archive, FolderGit2, Settings } from "lucide-react";
import { cn } from "../../lib/utils";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { WorktreesPanel } from "../Worktrees/WorktreesPanel";
import { ArchivedWorktreesPanel } from "../Worktrees/ArchivedWorktreesPanel";
import { WorktreeDetailView } from "../Worktrees/WorktreeDetailView";
import { WorktreeSettings } from "./WorktreeSettings";

type WorkspacesTab = "active" | "archived" | "settings";

export function WorkspacesSection() {
  const [activeTab, setActiveTab] = useState<WorkspacesTab>("active");
  const { activeDaemon } = useDaemonStatus();
  const currentProject = useProjectStore((state) => state.currentProject);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const isLoading = useWorktreeStore((state) => state.isLoading);

  useEffect(() => {
    if (currentProject?.id) {
      loadWorktrees(currentProject.id, { includeArchived: true });
    }
  }, [currentProject?.id, loadWorktrees]);

  const { activeCount, archivedCount } = useMemo(() => {
    return worktrees.reduce(
      (counts, worktree) => {
        if (worktree.deleted_at) {
          counts.archivedCount += 1;
        } else {
          counts.activeCount += 1;
        }
        return counts;
      },
      { activeCount: 0, archivedCount: 0 }
    );
  }, [worktrees]);

  const tabs: Array<{
    id: WorkspacesTab;
    label: string;
    description: string;
    icon: React.ReactNode;
    count?: number;
  }> = [
    {
      id: "active",
      label: "Active",
      description: "Current workspaces",
      icon: <FolderGit2 className="h-4 w-4" />,
      count: activeCount,
    },
    {
      id: "archived",
      label: "Archived",
      description: "Stored for later",
      icon: <Archive className="h-4 w-4" />,
      count: archivedCount,
    },
    {
      id: "settings",
      label: "Preferences",
      description: "Defaults and cleanup",
      icon: <Settings className="h-4 w-4" />,
    },
  ];

  return (
    <div className="flex h-full flex-col bg-background">
      <div className="flex-shrink-0 border-b border-border/60 bg-card/30 px-6 py-5">
        <div className="flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
          <div className="min-w-0">
            <div className="mb-2 flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
              <FolderGit2 className="h-3.5 w-3.5" />
              Workspace Management
            </div>
            <h1 className="text-2xl font-semibold tracking-tight text-foreground">
              Workspaces
            </h1>
            <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
              Manage project workspaces, review their state, and tune safe cleanup defaults.
            </p>
            <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span className="rounded-full border border-border bg-background px-2.5 py-1">
                {currentProject?.name ?? "No project selected"}
              </span>
              {activeDaemon?.hostname && (
                <span className="rounded-full border border-border bg-background px-2.5 py-1">
                  Machine: {activeDaemon.hostname}
                </span>
              )}
              {isLoading && <span>Refreshing…</span>}
            </div>
          </div>

          <div className="grid w-full gap-2 rounded-xl border border-border/60 bg-background/80 p-1 sm:w-auto sm:grid-cols-3">
            {tabs.map((tab) => (
              <button
                key={tab.id}
                type="button"
                aria-pressed={activeTab === tab.id}
                onClick={() => setActiveTab(tab.id)}
                className={cn(
                  "flex min-w-0 items-center gap-3 rounded-lg px-3 py-2 text-left transition-colors focus:outline-none focus:ring-2 focus:ring-primary/50",
                  activeTab === tab.id
                    ? "bg-primary text-primary-foreground shadow-sm"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground"
                )}
              >
                <span className="flex-shrink-0">{tab.icon}</span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{tab.label}</span>
                  <span
                    className={cn(
                      "block truncate text-xs",
                      activeTab === tab.id
                        ? "text-primary-foreground/75"
                        : "text-muted-foreground"
                    )}
                  >
                    {tab.description}
                  </span>
                </span>
                {typeof tab.count === "number" && (
                  <span
                    className={cn(
                      "rounded-full px-2 py-0.5 text-xs font-medium",
                      activeTab === tab.id
                        ? "bg-primary-foreground/15 text-primary-foreground"
                        : "bg-muted text-muted-foreground"
                    )}
                  >
                    {tab.count}
                  </span>
                )}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-hidden">
        {activeTab === "settings" ? (
          <div className="h-full overflow-auto px-6 py-6">
            <div className="mx-auto max-w-3xl">
              <WorktreeSettings />
            </div>
          </div>
        ) : (
          <div className="flex h-full min-h-0">
            <div className="w-80 flex-shrink-0 overflow-hidden border-r border-border/60 bg-card/20">
              {activeTab === "active" ? (
                <WorktreesPanel daemonId={activeDaemon?.daemonId} includeArchivedOnLoad />
              ) : (
                <ArchivedWorktreesPanel />
              )}
            </div>
            <div className="min-w-0 flex-1 overflow-hidden">
              <WorktreeDetailView />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}