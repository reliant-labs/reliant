import { useState, useEffect, useMemo } from "react";
import { Archive, RefreshCw, Loader2, RotateCcw, Calendar, FolderGit2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatStore } from "../../store/chatStore";
import { Button } from "../ui/Button";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
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
  const chatsMap = useChatStore((state) => state.chats);
  const chats = useMemo(() => Array.from(chatsMap.values()), [chatsMap]);
  const [unarchivingId, setUnarchivingId] = useState<string | null>(null);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [worktreeToDelete, setWorktreeToDelete] = useState<Worktree | null>(null);

  // Filter for archived worktrees (those with deleted_at set)
  const archivedWorktrees = worktrees.filter(w => w.deleted_at !== null && w.deleted_at !== undefined);

  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id);
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

  // Calculate chat count for the worktree being deleted
  const chatCount = worktreeToDelete
    ? chats.filter(chat => chat.worktreeId === worktreeToDelete.id).length
    : 0;

  if (!currentProject) {
    return (
      <div className="flex flex-col h-full items-center justify-center p-4 text-center">
        <Archive className="w-8 h-8 text-muted-foreground opacity-50 mb-2" />
        <p className="text-sm font-mono text-muted-foreground">
          Select a project to view archived workspaces
        </p>
      </div>
    );
  }

  return (
    <div className={cn("flex flex-col h-full bg-background", paddingClass)}>
      {/* Header */}
      <div className="flex items-center justify-between p-4 border-b border-border">
        <div className="flex items-center gap-2">
          <Archive className="w-4 h-4 text-muted-foreground" />
          <h2 className="text-sm font-semibold text-foreground">
            Archived Workspaces
          </h2>
          {archivedWorktrees.length > 0 && (
            <span className="text-xs text-muted-foreground bg-muted px-2 py-0.5 rounded-full">
              {archivedWorktrees.length}
            </span>
          )}
        </div>
        <Button
          onClick={() => currentProject && loadWorktrees(currentProject.id)}
          leftIcon={<RefreshCw className={cn("w-3 h-3", isLoading && "animate-spin")} />}
          variant="ghost"
          size="xs"
          disabled={isLoading}
        >
          Refresh
        </Button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center h-32">
            <Loader2 className="w-6 h-6 animate-spin text-muted-foreground" />
          </div>
        ) : archivedWorktrees.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-center p-8">
            <Archive className="w-12 h-12 text-muted-foreground opacity-30 mb-3" />
            <p className="text-sm font-medium text-foreground mb-1">
              No archived workspaces
            </p>
            <p className="text-xs text-muted-foreground">
              Archived workspaces will appear here
            </p>
          </div>
        ) : (
          <div className="p-3 space-y-3">
            {archivedWorktrees.map((worktree) => (
              <div
                key={worktree.id}
                className="rounded-lg p-3 border border-border bg-card/50 hover:bg-card hover:border-primary/30 transition-all"
              >
                {/* Workspace Info */}
                <div className="flex items-start justify-between mb-3">
                  <div className="flex-1">
                    <div className="flex items-center gap-2 mb-1">
                      <FolderGit2 className="w-4 h-4 text-muted-foreground" />
                      <h3 className="font-semibold text-sm text-foreground">
                        {worktree.name}
                      </h3>
                    </div>
                    <p className="text-xs text-muted-foreground font-mono">
                      {worktree.branch}
                    </p>
                  </div>
                </div>

                {/* Archived Date */}
                {worktree.deleted_at && (() => {
                  try {
                    const date = new Date(worktree.deleted_at);
                    if (isNaN(date.getTime())) {
                      return null; // Invalid date, don't render
                    }
                    return (
                      <div className="flex items-center gap-2 text-xs text-muted-foreground mb-3">
                        <Calendar className="w-3 h-3" />
                        <span>
                          Archived {format(date, "MMM d, yyyy 'at' h:mm a")}
                        </span>
                      </div>
                    );
                  } catch (error) {
                    return null; // Error formatting date, don't render
                  }
                })()}

                {/* Actions */}
                <div className="flex gap-2 pt-3 border-t border-border">
                  <Button
                    onClick={() => handleUnarchive(worktree.id)}
                    leftIcon={
                      unarchivingId === worktree.id ? (
                        <Loader2 className="w-3 h-3 animate-spin" />
                      ) : (
                        <RotateCcw className="w-3 h-3" />
                      )
                    }
                    variant="default"
                    size="xs"
                    disabled={unarchivingId === worktree.id}
                    className="flex-1"
                  >
                    Restore
                  </Button>
                  <Button
                    onClick={() => handleCleanup(worktree)}
                    variant="destructive"
                    size="xs"
                    className="flex-1"
                  >
                    Delete
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Delete Modal */}
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
