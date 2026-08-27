import { useState, useEffect } from "react";
import { Archive, RefreshCw, Loader2, RotateCcw, Calendar, FolderGit2, Trash2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { Button } from "../ui/Button";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { workspaceButton } from "./workspaceStyles";
import { format } from "date-fns";

interface ArchivedWorktreesPanelProps {
  paddingClass?: string;
}

export function ArchivedWorktreesPanel({ paddingClass = "" }: ArchivedWorktreesPanelProps) {
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const deleteWorktree = useWorktreeStore((state) => state.deleteWorktree);
  const unarchiveWorktree = useWorktreeStore((state) => state.unarchiveWorktree);
  const isLoading = useWorktreeStore((state) => state.isLoading);
  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const [unarchivingId, setUnarchivingId] = useState<string | null>(null);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [worktreeToDelete, setWorktreeToDelete] = useState<Worktree | null>(null);

  const archivedWorktrees = worktrees.filter((worktree) => worktree.deleted_at);

  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id, { includeArchived: true });
    }
  }, [currentProject, loadWorktrees]);

  const handleUnarchive = async (id: string) => {
    setUnarchivingId(id);
    try {
      await unarchiveWorktree(id);
    } catch (error) {
      console.error("Failed to unarchive:", error);
    } finally {
      setUnarchivingId(null);
    }
  };

  const handleCleanup = (worktree: Worktree) => {
    setWorktreeToDelete(worktree);
    setDeleteModalOpen(true);
  };

  const handleConfirmDelete = async (options?: { deleteGitBranch: boolean; deleteLocalDirectory: boolean }) => {
    if (worktreeToDelete) {
      await deleteWorktree(worktreeToDelete.id, options);
      setDeleteModalOpen(false);
      setWorktreeToDelete(null);
    }
  };

  const chatCount = worktreeToDelete
    ? chats.filter((chat) => chat.worktreeId === worktreeToDelete.id).length
    : 0;

  const formatArchivedDate = (dateString?: string | null) => {
    if (!dateString) return null;
    try {
      const date = new Date(dateString);
      if (isNaN(date.getTime())) return null;
      return format(date, "MMM d, yyyy 'at' h:mm a");
    } catch {
      return null;
    }
  };

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center p-6 text-center">
        <Archive className="mb-3 h-10 w-10 text-muted-foreground/40" />
        <p className="text-sm font-medium text-foreground">No project selected</p>
        <p className="mt-1 text-xs text-muted-foreground">
          Select a project to view archived workspaces.
        </p>
      </div>
    );
  }

  return (
    <div className={cn("flex h-full flex-col", paddingClass)}>
      <div className="flex-shrink-0 border-b border-border/60 px-4 py-4">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-foreground">Archived workspaces</h2>
              <span className="rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
                {archivedWorktrees.length}
              </span>
            </div>
            <p className="mt-1 truncate text-xs text-muted-foreground">
              Restore old workspaces or clean up remaining files.
            </p>
          </div>
          <Button
            onClick={() => loadWorktrees(currentProject.id, { includeArchived: true })}
            leftIcon={<RefreshCw className={cn("h-3 w-3", isLoading && "animate-spin")} />}
            variant="outline"
            size="xs"
            disabled={isLoading}
            className={workspaceButton.subtle}
          >
            Refresh
          </Button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {isLoading && archivedWorktrees.length === 0 ? (
          <div className="flex h-32 items-center justify-center">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        ) : archivedWorktrees.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center rounded-xl border border-dashed border-border/70 p-6 text-center">
            <Archive className="mb-3 h-10 w-10 text-muted-foreground/35" />
            <p className="text-sm font-medium text-foreground">Nothing archived</p>
            <p className="mt-1 max-w-48 text-xs text-muted-foreground">
              Archived workspaces will appear here when you put them away.
            </p>
          </div>
        ) : (
          <div className="space-y-2">
            {archivedWorktrees.map((worktree) => {
              const archivedDate = formatArchivedDate(worktree.deleted_at);
              const isRestoring = unarchivingId === worktree.id;

              return (
                <div
                  key={worktree.id}
                  className="rounded-xl border border-border/60 bg-background p-3 transition-colors hover:border-primary/30 hover:bg-muted/30"
                >
                  <div className="flex items-start gap-3">
                    <div className="mt-0.5 rounded-lg bg-muted p-2 text-muted-foreground">
                      <FolderGit2 className="h-4 w-4" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <h3 className="truncate text-sm font-medium text-foreground">
                          {worktree.name}
                        </h3>
                        {worktree.is_main && (
                          <span className="rounded-full bg-muted px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
                            Main
                          </span>
                        )}
                      </div>
                      <p className="mt-1 truncate text-xs text-muted-foreground">
                        {worktree.branch}
                      </p>
                      {archivedDate && (
                        <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
                          <Calendar className="h-3 w-3" />
                          Archived {archivedDate}
                        </p>
                      )}
                    </div>
                  </div>

                  <div className="mt-3 grid grid-cols-2 gap-2 border-t border-border/60 pt-3">
                    <Button
                      onClick={() => handleUnarchive(worktree.id)}
                      leftIcon={
                        isRestoring ? (
                          <Loader2 className="h-3 w-3 animate-spin" />
                        ) : (
                          <RotateCcw className="h-3 w-3" />
                        )
                      }
                      variant="secondary"
                      size="xs"
                      disabled={isRestoring}
                      className={workspaceButton.secondary}
                    >
                      {isRestoring ? "Restoring…" : "Restore"}
                    </Button>
                    <Button
                      onClick={() => handleCleanup(worktree)}
                      leftIcon={<Trash2 className="h-3 w-3" />}
                      variant="destructive"
                      size="xs"
                      className={workspaceButton.destructive}
                    >
                      Delete
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      <DeleteWorktreeModal
        isOpen={deleteModalOpen}
        onClose={() => {
          setDeleteModalOpen(false);
          setWorktreeToDelete(null);
        }}
        worktree={worktreeToDelete}
        onConfirmDelete={handleConfirmDelete}
        chatCount={chatCount}
      />
    </div>
  );
}