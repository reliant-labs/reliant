import { useState } from "react";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { AlertTriangle, FolderX, Check, Settings, FolderGit2 } from "lucide-react";
import type { Worktree } from "../../store/worktreeStore";
import { usePreferences } from "../../hooks/settings-queries";
import { useViewerStore } from "../../store/viewerStore";

interface DeleteWorktreeModalProps {
  isOpen: boolean;
  onClose: () => void;
  worktree: Worktree | null;
  onConfirmDelete: (options?: { deleteGitBranch: boolean; deleteLocalDirectory: boolean }) => void;
  chatCount?: number; // Number of chats that will be affected
}

export function DeleteWorktreeModal({
  isOpen,
  onClose,
  worktree,
  onConfirmDelete,
  chatCount = 0,
}: DeleteWorktreeModalProps) {
  const { data: preferences } = usePreferences();
  const setSettingsMode = useViewerStore((state) => state.setSettingsMode);
  const [isDeleting, setIsDeleting] = useState(false);
  
  // Smart defaults based on whether it's first archive or permanent delete
  const isArchived = worktree?.deleted_at !== null && worktree?.deleted_at !== undefined;
  const [deleteGitBranch, setDeleteGitBranch] = useState(
    // When permanently deleting (already archived), default to true to clean up the branch
    // When archiving, use the user's preference
    isArchived ? true : (preferences?.worktree.defaultDeleteBranch ?? false)
  );
  const [deleteLocalDirectory, setDeleteLocalDirectory] = useState(
    isArchived ? true : (preferences?.worktree.defaultDeleteDirectory ?? true)
  );

  if (!worktree) return null;

  // Cannot delete main worktree
  if (worktree.is_main) {
    return (
      <Modal
        isOpen={isOpen}
        onClose={onClose}
        title="Cannot Delete Main Worktree"
        size="sm"
      >
        <div className="space-y-4">
          <div className="text-center space-y-2">
            <div className="flex justify-center mb-3">
              <AlertTriangle className="w-12 h-12 text-amber-500" />
            </div>
            <h3 className="font-semibold text-lg text-foreground">
              Main worktree is protected
            </h3>
            <p className="text-sm text-muted-foreground">
              The main project worktree <strong className="font-mono">{worktree.name}</strong> cannot be deleted or archived.
              It represents the default branch of your project.
            </p>
          </div>
          <div className="flex gap-3 justify-end">
            <Button variant="outline" onClick={onClose}>
              Close
            </Button>
          </div>
        </div>
      </Modal>
    );
  }

  const handleConfirm = async () => {
    setIsDeleting(true);
    try {
      await onConfirmDelete({
        deleteGitBranch,
        deleteLocalDirectory,
      });
      onClose();
      // Reset checkboxes to preference defaults
      setDeleteGitBranch(preferences?.worktree.defaultDeleteBranch ?? false);
      setDeleteLocalDirectory(preferences?.worktree.defaultDeleteDirectory ?? true);
    } catch (error) {
      console.error("Failed to delete worktree:", error);
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={isArchived ? "Delete Archived Worktree?" : "Archive Worktree?"}
      size="sm"
    >
      <div className="space-y-4">
        {/* Message */}
        <div className="text-center space-y-2">
          <h3 className="font-semibold text-lg text-foreground">
            {isArchived
              ? "Delete this worktree permanently?"
              : "Archive this worktree?"}
          </h3>
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">
              {isArchived ? (
                <>
                  This will <strong className="text-destructive">permanently delete</strong>{" "}
                  the worktree record <strong className="font-mono">{worktree.name}</strong> from the database.
                  {chatCount > 0 && (
                    <>
                      <br />
                      <br />
                      <strong>{chatCount} archived chat{chatCount > 1 ? "s" : ""}</strong> will remain in the database with their reference to this worktree.
                    </>
                  )}
                  <br />
                  <br />
                  <span className="text-destructive font-medium">This action cannot be undone.</span>
                </>
              ) : (
                <>
                  <strong className="font-mono">{worktree.name}</strong> will be archived
                  {chatCount > 0 && (
                    <>
                      {" "}along with <strong>{chatCount} associated chat{chatCount > 1 ? "s" : ""}</strong>
                    </>
                  )}.
                </>
              )}
            </p>
            {!isArchived && (
              <div className="flex items-center justify-center gap-2 text-xs bg-muted/30 py-2 px-3 rounded-md">
                <FolderGit2 className="w-3 h-3 text-muted-foreground" />
                <span className="text-muted-foreground">Branch: <span className="font-mono text-foreground">{worktree.branch}</span></span>
              </div>
            )}
          </div>
        </div>

        {/* Cleanup Options for First-Time Archive - shown when archiving */}
        {!isArchived && (
          <div className="space-y-3 border-t border-border pt-4">
            <div className="flex items-center justify-between">
              <p className="text-sm font-medium text-foreground">Cleanup Options:</p>
              <button
                type="button"
                onClick={() => {
                  onClose(); // Close the modal first
                  setTimeout(() => {
                    setSettingsMode(true, "workspaces");
                  }, 100); // Small delay to ensure modal is closed
                }}
                className="text-xs text-primary hover:underline flex items-center gap-1"
                title="Change default behavior in Settings > Workspaces"
              >
                <Settings className="w-3 h-3" />
                Change defaults
              </button>
            </div>
            
            {/* Delete Local Directory */}
            <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-2 rounded-lg transition-colors">
              <div className="relative flex items-center justify-center mt-0.5">
                <input
                  type="checkbox"
                  checked={deleteLocalDirectory}
                  onChange={(e) => setDeleteLocalDirectory(e.target.checked)}
                  className="sr-only"
                />
                <div className={`w-5 h-5 rounded border-2 transition-all flex items-center justify-center ${
                  deleteLocalDirectory 
                    ? 'border-primary bg-primary' 
                    : 'border-border bg-background'
                }`}>
                  {deleteLocalDirectory && <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />}
                </div>
              </div>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <FolderX className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm font-medium text-foreground">Delete local worktree directory</span>
                </div>
                <p className="text-xs text-muted-foreground mt-1">
                  Removes the working directory from your filesystem ({worktree.path})
                </p>
              </div>
            </label>

            {/* Delete Git Branch */}
            <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-2 rounded-lg transition-colors">
              <div className="relative flex items-center justify-center mt-0.5">
                <input
                  type="checkbox"
                  checked={deleteGitBranch}
                  onChange={(e) => setDeleteGitBranch(e.target.checked)}
                  className="sr-only"
                />
                <div className={`w-5 h-5 rounded border-2 transition-all flex items-center justify-center ${
                  deleteGitBranch 
                    ? 'border-primary bg-primary' 
                    : 'border-border bg-background'
                }`}>
                  {deleteGitBranch && <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />}
                </div>
              </div>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <FolderGit2 className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm font-medium text-foreground">Delete git branch</span>
                </div>
                <p className="text-xs text-muted-foreground mt-1">
                  Permanently deletes the branch <span className="font-mono">{worktree.branch}</span> from git repository
                </p>
                {deleteGitBranch && (
                  <p className="text-xs text-destructive font-medium mt-1">
                    ⚠️ This will delete the branch completely - make sure it's merged or you have backups!
                  </p>
                )}
              </div>
            </label>

            <div className="bg-muted/50 border border-border/50 rounded-lg p-3">
              <div className="flex gap-2 text-xs text-muted-foreground">
                <AlertTriangle className="w-4 h-4 flex-shrink-0 mt-0.5" />
                <div>
                  <p className="font-medium text-foreground mb-1">What happens when archiving?</p>
                  <ul className="space-y-1">
                    <li>• Worktree is archived (marked as archived in database)</li>
                    <li>• All associated chats are automatically archived</li>
                    <li>• Optional cleanup based on your selections above</li>
                    <li>• You can view archived worktrees from the archived view</li>
                    <li>• Delete again from archived view to permanently remove from database</li>
                  </ul>
                </div>
              </div>
            </div>
          </div>
        )}

        {isArchived && chatCount > 0 && (
          <div className="bg-primary/10 border border-primary/30 rounded-lg p-3">
            <div className="flex gap-2 text-xs">
              <AlertTriangle className="w-4 h-4 flex-shrink-0 mt-0.5 text-primary" />
              <div className="text-foreground">
                <p className="font-medium mb-1">💾 Note</p>
                <p>
                  The worktree record stays in the database so {chatCount} archived chat{chatCount > 1 ? "s" : ""} can keep their reference.
                  Only local files are removed based on your selection below.
                </p>
              </div>
            </div>
          </div>
        )}

        {/* Deletion Options for Permanent Delete */}
        {isArchived && (
          <div className="space-y-3 border-t border-border pt-4">
            <p className="text-sm font-medium text-foreground">Cleanup Options:</p>
            
            {/* Delete Local Directory */}
            <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-2 rounded-lg transition-colors">
              <div className="relative flex items-center justify-center mt-0.5">
                <input
                  type="checkbox"
                  checked={deleteLocalDirectory}
                  onChange={(e) => setDeleteLocalDirectory(e.target.checked)}
                  className="sr-only"
                />
                <div className={`w-5 h-5 rounded border-2 transition-all flex items-center justify-center ${
                  deleteLocalDirectory 
                    ? 'border-primary bg-primary' 
                    : 'border-border bg-background'
                }`}>
                  {deleteLocalDirectory && <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />}
                </div>
              </div>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <FolderX className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm font-medium text-foreground">Delete local worktree directory</span>
                </div>
                <p className="text-xs text-muted-foreground mt-1">
                  Removes the working directory from your filesystem ({worktree.path})
                </p>
              </div>
            </label>

            {/* Delete Git Branch */}
            <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-2 rounded-lg transition-colors">
              <div className="relative flex items-center justify-center mt-0.5">
                <input
                  type="checkbox"
                  checked={deleteGitBranch}
                  onChange={(e) => setDeleteGitBranch(e.target.checked)}
                  className="sr-only"
                />
                <div className={`w-5 h-5 rounded border-2 transition-all flex items-center justify-center ${
                  deleteGitBranch 
                    ? 'border-primary bg-primary' 
                    : 'border-border bg-background'
                }`}>
                  {deleteGitBranch && <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />}
                </div>
              </div>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <FolderGit2 className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm font-medium text-foreground">Delete git branch</span>
                </div>
                <p className="text-xs text-muted-foreground mt-1">
                  Permanently deletes the branch <span className="font-mono">{worktree.branch}</span> from git repository
                </p>
                {deleteGitBranch && (
                  <p className="text-xs text-destructive font-medium mt-1">
                    ⚠️ This will delete the branch completely - make sure it's merged or you have backups!
                  </p>
                )}
              </div>
            </label>
          </div>
        )}

        {/* Action Buttons */}
        <div className="flex gap-3 pt-2">
          <Button
            variant="outline"
            onClick={onClose}
            disabled={isDeleting}
            className="flex-1"
          >
            Cancel
          </Button>
          <Button
            variant={isArchived ? "destructive" : "default"}
            onClick={handleConfirm}
            disabled={isDeleting}
            className={`flex-1 ${
              !isArchived
                ? "bg-amber-500/90 hover:bg-amber-500 text-white border-amber-600/50"
                : ""
            }`}
          >
            {isDeleting ? (
              "Processing..."
            ) : isArchived ? (
              "Delete"
            ) : (
              "Archive Worktree"
            )}
          </Button>
        </div>
      </div>
    </Modal>
  );
}