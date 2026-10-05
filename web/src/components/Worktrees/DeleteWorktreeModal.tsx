import { useState, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import type { Worktree } from "../../store/worktreeStore";
import { usePreferences } from "../../hooks/settings-queries";

interface DeleteWorktreeModalProps {
  isOpen: boolean;
  onClose: () => void;
  worktree: Worktree | null;
  onConfirmDelete: (options?: { deleteGitBranch: boolean; deleteLocalDirectory: boolean }) => void;
  chatCount?: number; // Number of chats that will be affected
}

/**
 * Confirms archiving an active workspace, or permanently deleting an archived
 * one, with the on-disk cleanup choices for each.
 *
 * The defaults are read once, when the dialog mounts, from whether the
 * workspace is already archived. Callers that reuse one dialog for several
 * workspaces must key it by workspace id so each gets its own defaults.
 */
export function DeleteWorktreeModal({
  isOpen,
  onClose,
  worktree,
  onConfirmDelete,
  chatCount = 0,
}: DeleteWorktreeModalProps) {
  const { data: preferences } = usePreferences();
  const navigate = useNavigate();
  const [isDeleting, setIsDeleting] = useState(false);

  // Permanently deleting an archived workspace defaults to removing
  // everything; archiving uses the user's saved cleanup defaults.
  const isArchived = worktree?.deleted_at !== null && worktree?.deleted_at !== undefined;
  const [deleteGitBranch, setDeleteGitBranch] = useState(
    isArchived ? true : (preferences?.worktree.defaultDeleteBranch ?? false),
  );
  const [deleteLocalDirectory, setDeleteLocalDirectory] = useState(
    isArchived ? true : (preferences?.worktree.defaultDeleteDirectory ?? true),
  );

  if (!worktree) return null;

  if (worktree.is_main) {
    return (
      <Modal isOpen={isOpen} onClose={onClose} title="The main checkout can't be archived" size="sm">
        <div className="flex flex-col gap-4">
          <p className="text-pretty text-sm text-muted-foreground">
            <span className="font-mono text-foreground">{worktree.name}</span> is your project's own
            checkout, not a separate worktree, so there is nothing to archive or delete.
          </p>
          <div className="flex justify-end">
            <Button variant="outline" size="sm" onClick={onClose}>
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
      await onConfirmDelete({ deleteGitBranch, deleteLocalDirectory });
      onClose();
    } catch (error) {
      console.error("Failed to delete worktree:", error);
    } finally {
      setIsDeleting(false);
    }
  };

  const openCleanupSettings = () => {
    onClose();
    // Let the dialog unmount before the route changes underneath it.
    setTimeout(() => {
      navigate({ to: "/settings/$section", params: { section: "workspaces" } });
    }, 100);
  };

  const chatsPhrase = `${chatCount} chat${chatCount === 1 ? "" : "s"}`;

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={isArchived ? "Delete workspace permanently?" : "Archive workspace?"}
      size="sm"
    >
      <div className="flex flex-col gap-4">
        <p className="text-pretty text-sm text-muted-foreground">
          {isArchived ? (
            <>
              <span className="font-mono text-foreground">{worktree.name}</span> will be removed from
              Reliant.{" "}
              {chatCount > 0 && `Its ${chatsPhrase} stay in your chat archive. `}
              <span className="font-medium text-destructive">This can't be undone.</span>
            </>
          ) : (
            <>
              <span className="font-mono text-foreground">{worktree.name}</span>
              {chatCount > 0 ? ` and its ${chatsPhrase} will be archived.` : " will be archived."}{" "}
              You can restore it from the Archived tab.
            </>
          )}
        </p>

        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between gap-2">
            <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Also remove
            </h4>
            {!isArchived && (
              <button
                type="button"
                onClick={openCleanupSettings}
                className="rounded-sm text-xs text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                Change defaults
              </button>
            )}
          </div>
          <div className="divide-y divide-border/60 rounded-md border border-border/60 bg-background">
            <CleanupOption
              checked={deleteLocalDirectory}
              onChange={setDeleteLocalDirectory}
              title="Worktree directory"
              description={
                <>
                  Delete <span className="break-all font-mono">{worktree.path}</span> from disk.
                  Uncommitted changes in it are lost.
                </>
              }
            />
            <CleanupOption
              checked={deleteGitBranch}
              onChange={setDeleteGitBranch}
              title="Git branch"
              description={
                <>
                  Delete the branch <span className="font-mono">{worktree.branch}</span> from the
                  repository.
                </>
              }
              warning={
                deleteGitBranch
                  ? "Commits that aren't merged or pushed elsewhere will be gone."
                  : undefined
              }
            />
          </div>
        </div>

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <Button variant="outline" size="sm" onClick={onClose} disabled={isDeleting}>
            Cancel
          </Button>
          <Button
            variant={isArchived ? "destructive" : "primary"}
            size="sm"
            onClick={handleConfirm}
            loading={isDeleting}
          >
            {isArchived ? "Delete permanently" : "Archive workspace"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function CleanupOption({
  checked,
  onChange,
  title,
  description,
  warning,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  title: string;
  description: ReactNode;
  warning?: string;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-3 px-3 py-2.5 hover:bg-muted/50">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 h-4 w-4 flex-shrink-0 rounded border-border accent-primary"
      />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="text-sm font-medium text-foreground">{title}</span>
        <span className="text-pretty text-xs text-muted-foreground">{description}</span>
        {warning && <span className="text-xs font-medium text-destructive">{warning}</span>}
      </span>
    </label>
  );
}
