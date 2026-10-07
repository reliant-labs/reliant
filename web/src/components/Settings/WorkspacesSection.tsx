import { useEffect, useMemo, useState } from "react";
import { FolderGit2, Monitor } from "lucide-react";
import { cn } from "../../lib/utils";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useSettingsClose } from "../../hooks/useSettingsClose";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { WorktreesPanel } from "../Worktrees/WorktreesPanel";
import { ArchivedWorktreesPanel } from "../Worktrees/ArchivedWorktreesPanel";
import { WorktreeDetailView } from "../Worktrees/WorktreeDetailView";
import { workspaceChip } from "../Worktrees/workspaceStyles";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { WorktreeSettings } from "./WorktreeSettings";
import { machineDisplayName } from "@/lib/machineName";

type WorkspacesTab = "active" | "archived" | "settings";

/**
 * Settings → Workspaces. A workspace is a git worktree of the current project
 * that a branched chat works in; this page lists them (active and archived),
 * drills into one, and holds the cleanup defaults applied when archiving.
 *
 * Selecting a workspace here opens its detail IN the page — it does not switch
 * the app into it. "Open workspace" does that, and then leaves Settings.
 */
export function WorkspacesSection() {
  const [activeTab, setActiveTab] = useState<WorkspacesTab>("active");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { activeDaemon } = useDaemonStatus();
  const closeSettings = useSettingsClose();
  const currentProject = useProjectStore((state) => state.currentProject);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);

  useEffect(() => {
    if (currentProject?.id) {
      loadWorktrees(currentProject.id, { includeArchived: true });
    }
  }, [currentProject?.id, loadWorktrees]);

  // A selection belongs to a project, and to a workspace that still exists.
  // Archiving the open workspace from its own detail view lands back on the
  // list rather than on a detail page for a row that moved tabs.
  const selected = useMemo(
    () => worktrees.find((worktree) => worktree.id === selectedId && !worktree.deleted_at) ?? null,
    [worktrees, selectedId],
  );

  const { activeCount, archivedCount } = useMemo(() => {
    let active = 0;
    let archived = 0;
    for (const worktree of worktrees) {
      if (worktree.deleted_at) archived += 1;
      else active += 1;
    }
    return { activeCount: active, archivedCount: archived };
  }, [worktrees]);

  const tabs: Array<{ id: WorkspacesTab; label: string; count?: number }> = [
    { id: "active", label: "Active", count: activeCount },
    { id: "archived", label: "Archived", count: archivedCount },
    { id: "settings", label: "Cleanup settings" },
  ];

  const selectTab = (tab: WorkspacesTab) => {
    setActiveTab(tab);
    setSelectedId(null);
  };

  return (
    <div className="h-full overflow-y-auto bg-background" data-testid="workspaces-section">
      <div className="mx-auto flex w-full max-w-6xl flex-col px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
        <SettingsPageHeader
          title="Workspaces"
          description="Each branched chat gets its own git worktree so agents can work in parallel without touching your main checkout. Review, open, archive or clean them up here."
          meta={
            <>
              <span className={workspaceChip} title="Workspaces are listed for the open project">
                <FolderGit2 className="h-3 w-3" aria-hidden="true" />
                <span className="sr-only">Project:</span>
                {currentProject?.name ?? "No project open"}
              </span>
              {activeDaemon && (
                <span className={workspaceChip} title="The machine these worktrees live on">
                  <Monitor className="h-3 w-3" aria-hidden="true" />
                  <span className="sr-only">Machine:</span>
                  {machineDisplayName(activeDaemon)}
                </span>
              )}
            </>
          }
          className="mb-4"
        />

        {selected ? (
          <WorktreeDetailView
            worktreeId={selected.id}
            onBack={() => setSelectedId(null)}
            onOpened={closeSettings}
            embedded
          />
        ) : (
          <>
            <div
              role="tablist"
              aria-label="Workspace views"
              className="mb-4 flex gap-1 border-b border-border"
            >
              {tabs.map((tab) => {
                const isActive = activeTab === tab.id;
                return (
                  <button
                    key={tab.id}
                    type="button"
                    role="tab"
                    id={`workspaces-tab-${tab.id}`}
                    aria-selected={isActive}
                    aria-controls={`workspaces-panel-${tab.id}`}
                    data-testid={`workspaces-tab-${tab.id}`}
                    onClick={() => selectTab(tab.id)}
                    className={cn(
                      "-mb-px inline-flex items-center gap-1.5 rounded-sm border-b-2 px-3 py-2 text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      isActive
                        ? "border-primary font-medium text-foreground"
                        : "border-transparent text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {tab.label}
                    {typeof tab.count === "number" && (
                      <span className="text-xs tabular-nums text-muted-foreground">{tab.count}</span>
                    )}
                  </button>
                );
              })}
            </div>

            <div
              role="tabpanel"
              id={`workspaces-panel-${activeTab}`}
              aria-labelledby={`workspaces-tab-${activeTab}`}
            >
              {activeTab === "active" && (
                <WorktreesPanel
                  paddingClass=""
                  daemonId={activeDaemon?.daemonId}
                  includeArchivedOnLoad
                  onSelect={(worktree) => setSelectedId(worktree.id)}
                  selectedId={null}
                  onOpened={closeSettings}
                />
              )}
              {activeTab === "archived" && <ArchivedWorktreesPanel paddingClass="" />}
              {activeTab === "settings" && (
                <div className="max-w-3xl">
                  <WorktreeSettings />
                </div>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
