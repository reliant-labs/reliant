import { useMemo, useState } from "react";
import { Archive, Loader2, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import EmptyState from "../forge-ui/empty_state";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { cleanupSummary, sortArchivedWorkspaces } from "./workspaceStatus";
import DataTable, { type DataTableColumn } from "../forge-ui/data_table";
import { workspaceColumn, workspaceTable } from "./workspaceStyles";
import { BranchCell, IconAction, TimeCell, WorkspaceNameCell } from "./WorkspaceTableParts";
import { deleteWorktree, unarchiveWorktree } from "../../hooks/worktree-actions";
import { useArchivedWorktreeList, type Worktree } from "../../hooks/worktree-queries";

type WorkspaceRow = { id: string; worktree: Worktree };

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
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const archived = useArchivedWorktreeList(currentProject?.id);
  const { worktrees, hasMore, loadMore: loadMoreArchived } = archived;
  const isLoading = archived.isFetching;
  const [restoringId, setRestoringId] = useState<string | null>(null);
  const [worktreeToDelete, setWorktreeToDelete] = useState<Worktree | null>(null);

  const archivedWorktrees = useMemo(
    () => sortArchivedWorkspaces(worktrees),
    [worktrees],
  );

  const handleRestore = async (id: string) => {
    setRestoringId(id);
    try {
      await unarchiveWorktree(id);
    } catch (error) {
      // unarchiveWorktree has already toasted the failure.
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

  const columns: DataTableColumn<WorkspaceRow>[] = [
    {
      key: "name",
      header: "Workspace",
      className: "max-w-0 w-full @xl:w-auto @xl:max-w-xs",
      render: (_, { worktree }) => <WorkspaceNameCell worktree={worktree} />,
    },
    {
      key: "branch",
      header: "Branch",
      className: cn(workspaceColumn.branch, "max-w-[14rem]"),
      headerClassName: workspaceColumn.branch,
      render: (_, { worktree }) => <BranchCell worktree={worktree} />,
    },
    {
      key: "archived",
      header: "Archived",
      className: workspaceColumn.time,
      headerClassName: workspaceColumn.time,
      render: (_, { worktree }) => <TimeCell value={worktree.deleted_at} empty="Unknown" />,
    },
    {
      key: "onDisk",
      header: "On disk",
      className: workspaceColumn.cleanup,
      headerClassName: workspaceColumn.cleanup,
      render: (_, { worktree }) => (
        <span className="text-xs text-muted-foreground">{cleanupSummary(worktree)}</span>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      className: "text-right",
      render: (_, { worktree }) => {
        const isRestoring = restoringId === worktree.id;
        const isDeleting = deletingId === worktree.id;
        return (
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
        );
      },
    },
  ];

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
          {archivedWorktrees.length === 1 && !hasMore
            ? "1 archived workspace"
            : `${archivedWorktrees.length}${hasMore ? "+" : ""} archived workspaces`}
        </p>
        <IconAction
          label="Refresh archived workspaces"
          icon={<RefreshCw className={cn("h-3.5 w-3.5", isLoading && "animate-spin")} />}
          onClick={() => void archived.refetch()}
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
        <div className={workspaceTable.frame}>
          <DataTable<WorkspaceRow>
            flush
            ariaLabel={`Archived workspaces in ${currentProject.name}`}
            columns={columns}
            data={archivedWorktrees.map((worktree) => ({ id: worktree.id, worktree }))}
            getRowKey={(row) => row.id}
            getRowProps={({ worktree }) => ({
              "data-testid": `archived-row-${worktree.id}`,
              className: cn(
                (restoringId === worktree.id || deletingId === worktree.id) && "opacity-60",
              ),
            })}
          />
          {hasMore && (
            <div className="flex justify-center border-t border-border p-2">
              <button
                type="button"
                className="text-xs text-primary hover:underline disabled:opacity-60"
                disabled={isLoading}
                onClick={loadMoreArchived}
                data-testid="load-more-archived"
              >
                {isLoading ? "Loading…" : "Load more archived workspaces"}
              </button>
            </div>
          )}
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
