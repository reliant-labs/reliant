import { useMemo, useState } from "react";
import { FolderGit2, Monitor } from "lucide-react";
import { cn } from "../../lib/utils";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useSettingsClose } from "../../hooks/useSettingsClose";
import { useProjectStore } from "../../store/projectStore";
import { WorktreesPanel } from "../Worktrees/WorktreesPanel";
import { ArchivedWorktreesPanel } from "../Worktrees/ArchivedWorktreesPanel";
import { WorktreeDetailView } from "../Worktrees/WorktreeDetailView";
import { workspaceChip } from "../Worktrees/workspaceStyles";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { WorktreeSettings } from "./WorktreeSettings";
import { machineDisplayName } from "@/lib/machineName";
import { useWorktreeList, useArchivedWorktreeList } from "../../hooks/worktree-queries";

type BranchesTab = "active" | "archived" | "settings";

/**
 * Settings → Branches. A branch here is the git worktree of the current
 * project that a branched chat works in; this page lists them (active and
 * archived), drills into one, and holds the cleanup defaults applied when
 * archiving. It was called "Workspaces", which collided with the product's
 * other workspace (a cloud workspace); its id was `workspaces` and
 * /settings/workspaces redirects here.
 *
 * Selecting a branch here opens its detail IN the page — it does not switch
 * the app into it. "Open" does that, and then leaves Settings.
 */
export function BranchesSection() {
  const [activeTab, setActiveTab] = useState<BranchesTab>("active");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { activeDaemon } = useDaemonStatus();
  const closeSettings = useSettingsClose();
  const currentProject = useProjectStore((state) => state.currentProject);
  const { worktrees, hasMore: hasMoreActive } = useWorktreeList(currentProject?.id);
  // Read only: the Archived tab loads the archived list when it is opened.
  const archived = useArchivedWorktreeList(currentProject?.id, { enabled: false });
  const archivedLoaded = archived.hasLoaded;
  const archivedCount = archived.worktrees.length;
  const hasMoreArchived = archived.hasMore;

  // A selection belongs to a project, and to a workspace that still exists.
  // Archiving the open workspace from its own detail view lands back on the
  // list rather than on a detail page for a row that moved tabs.
  const selected = useMemo(
    () => worktrees.find((worktree) => worktree.id === selectedId && !worktree.deleted_at) ?? null,
    [worktrees, selectedId],
  );

  // Counts reflect what is loaded. Archived workspaces are a separate query the
  // Archived tab loads when opened, so until then there is no count to show.
  const activeCount = worktrees.length;
  const tabs: Array<{ id: BranchesTab; label: string; count?: number }> = [
    { id: "active", label: hasMoreActive ? "Active (more)" : "Active", count: activeCount },
    {
      id: "archived",
      label: hasMoreArchived ? "Archived (more)" : "Archived",
      count: archivedLoaded ? archivedCount : undefined,
    },
    { id: "settings", label: "Cleanup settings" },
  ];

  const selectTab = (tab: BranchesTab) => {
    setActiveTab(tab);
    setSelectedId(null);
  };

  return (
    <div className="flex flex-col" data-testid="branches-section">
        <SettingsPageHeader
          title="Branches"
          description="Each branched chat works in its own git worktree, so agents can work in parallel without touching your main checkout. Review, open, archive or clean them up here."
          meta={
            <>
              <span className={workspaceChip} title="Branches are listed for the open project">
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
              aria-label="Branch views"
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
  );
}
