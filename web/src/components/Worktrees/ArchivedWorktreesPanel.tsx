import { useEffect, useMemo, useState } from "react";
import { Archive, Loader2, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import EmptyState from "../forge-ui/empty_state";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { cleanupSummary, sortArchivedWorkspaces } from "./workspaceStatus";
import { workspaceColumn, workspaceTable } from "./workspaceStyles";
import { BranchCell, IconAction, TimeCell, WorkspaceNameCell } from "./WorkspaceTableParts";

interface ArchivedWorktreesPanelProps {
  /** Padding around the panel. The viewer tab relies on the default. */
  paddingClass?: string;
}

/**
 * Archived workspaces: put away, restorable, and deletable for good. Archiving
 * keeps the record (and, unless cleanup removed them, the files and branch),
 * so the "Cleanup" column says what is actually left on disk.
 */
export function ArchivedWorktreesPanel({ paddingClass = "p-3" }: ArchivedWorktreesPanelProps) {
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const deleteWorktree = useWorktreeStore((state) => state.deleteWorktree);
  const unarchiveWorktree = useWorktreeStore((state) => state.unarchiveWorktree);
  const isLoading = useWorktreeStore((state) => state.isLoading);
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const [restoringId, setRestoringId] = useState<string | null>(null);
  const [worktreeToDelete, setWorktreeToDelete] = useState<Worktree | null>(null);

  const archivedWorktrees = useMemo(
    () => sortArchivedWorkspaces(worktrees.filter((worktree) => worktree.deleted_at)),
    [worktrees],
  );

  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id, { includeArchived: true });
    }
  }, [currentProject, loadWorktrees]);

  const handleRestore = async (id: string) => {
    setRestoringId(id);
    try {
      await unarchiveWorktree(id);
    } catch (error) {
      // The store has already toasted the failure.
      console.error("Failed to restore workspace:", error);
    } finally {
      setRestoringId(null);
    }
  };

  const handleConfirmDelete = async (options?: { deleteGitBranch: boolean }) => {
    if (worktreeToDelete) {
      await deleteWorktree(worktreeToDelete.id, options);
      setWorktreeToDelete(null);
    }
  };

  const chatCount = worktreeToDelete
    ? chats.filter((chat) => chat.worktreeId === worktreeToDelete.id).length
    : 0;

  if (!currentProject) {
    return (
      <div className={cn("forge-ui", paddingClass)}>
        <EmptyState
          icon={<Archive className="h-6 w-6" />}
          title="No project open"
          description="Open a project to see the workspaces you've archived in it."
        />
      </div>
    );
  }

  return (
    <div className={cn("forge-ui flex flex-col gap-3", paddingClass)} data-testid="archived-workspaces">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          {archivedWorktrees.length === 1
            ? "1 archived workspace"
            : `${archivedWorktrees.length} archived workspaces`}
        </p>
        <IconAction
          label="Refresh archived workspaces"
          icon={<RefreshCw className={cn("h-3.5 w-3.5", isLoading && "animate-spin")} />}
          onClick={() => void loadWorktrees(currentProject.id, { includeArchived: true })}
          disabled={isLoading}
        />
      </div>

      {isLoading && archivedWorktrees.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <SkeletonLoader variant="table-row" count={2} />
        </div>
      ) : archivedWorktrees.length === 0 ? (
        <EmptyState
          icon={<Archive className="h-6 w-6" />}
          title="Nothing archived"
          description="Archiving a workspace puts it and its chats away without losing them. Archived workspaces land here, where you can restore them or delete them for good."
        />
      ) : (
        <div className={workspaceTable.wrapper}>
          <table className={workspaceTable.table}>
            <caption className="sr-only">
              Archived workspaces in {currentProject.name}: branch, when each was archived, and what
              cleanup removed.
            </caption>
            <thead>
              <tr className={workspaceTable.headRow}>
                <th scope="col" className={workspaceTable.headCell}>
                  Workspace
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, workspaceColumn.branch)}>
                  Branch
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, workspaceColumn.time)}>
                  Archived
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, workspaceColumn.cleanup)}>
                  On disk
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, "text-right")}>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {archivedWorktrees.map((worktree) => {
                const isRestoring = restoringId === worktree.id;
                const isDeleting = deletingId === worktree.id;
                return (
                  <tr
                    key={worktree.id}
                    data-testid={`archived-row-${worktree.id}`}
                    className={cn(workspaceTable.row, (isRestoring || isDeleting) && "opacity-60")}
                  >
                    <td className={cn(workspaceTable.cell, "max-w-0 w-full @xl:w-auto @xl:max-w-xs")}>
                      <WorkspaceNameCell worktree={worktree} />
                    </td>
                    <td className={cn(workspaceTable.cell, workspaceColumn.branch, "max-w-[14rem]")}>
                      <BranchCell worktree={worktree} />
                    </td>
                    <td className={cn(workspaceTable.cell, workspaceColumn.time)}>
                      <TimeCell value={worktree.deleted_at} empty="Unknown" />
                    </td>
                    <td className={cn(workspaceTable.cell, workspaceColumn.cleanup)}>
                      <span className="whitespace-nowrap text-xs text-muted-foreground">
                        {cleanupSummary(worktree)}
                      </span>
                    </td>
                    <td className={cn(workspaceTable.cell, "whitespace-nowrap text-right")}>
                      <div className="inline-flex items-center gap-0.5">
                        <IconAction
                          label={`Restore ${worktree.name}`}
                          hint="Restore workspace and its chats"
                          icon={
                            isRestoring ? (
                              <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            ) : (
                              <RotateCcw className="h-3.5 w-3.5" />
                            )
                          }
                          onClick={() => void handleRestore(worktree.id)}
                          disabled={isRestoring || isDeleting}
                          testId={`restore-workspace-${worktree.id}`}
                        />
                        <IconAction
                          label={`Delete ${worktree.name} permanently`}
                          hint="Delete permanently"
                          icon={
                            isDeleting ? (
                              <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            ) : (
                              <Trash2 className="h-3.5 w-3.5" />
                            )
                          }
                          onClick={() => setWorktreeToDelete(worktree)}
                          disabled={isRestoring || isDeleting}
                          danger
                          testId={`delete-workspace-${worktree.id}`}
                        />
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <DeleteWorktreeModal
        key={worktreeToDelete?.id ?? "none"}
        isOpen={worktreeToDelete !== null}
        onClose={() => setWorktreeToDelete(null)}
        worktree={worktreeToDelete}
        onConfirmDelete={handleConfirmDelete}
        chatCount={chatCount}
      />
    </div>
  );
}
