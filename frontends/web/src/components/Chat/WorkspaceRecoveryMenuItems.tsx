/**
 * WorkspaceRecoveryMenuItems - the chat menu's way out of a broken workspace.
 *
 * A chat's workspace directory can disappear from disk (deleted, cleaned up,
 * a machine whose volume detached), and every tool the chat runs then has
 * nowhere to run. The chat recovers on its own when it can, but the user gets
 * the same two moves explicitly:
 * - Recreate workspace: rebuild the directory from its branch, in place.
 * - Move to main checkout: run the chat in the project's main checkout instead.
 */

import { FolderGit2, RotateCcw } from "lucide-react";
import type { Worktree } from "../../store/worktreeStore";
import { useMoveChatToMainCheckout, useRecreateWorkspace } from "../../hooks/workspace-recovery";
import { toast } from "../../lib/toast-manager";

interface WorkspaceRecoveryMenuItemsProps {
  chatId: string;
  /** The chat's workspace. Nothing renders for the main checkout. */
  worktree: Worktree;
  /** The project's main worktree, when known. */
  mainWorktree?: Worktree;
  /** Called once an item is chosen, to close the menu. */
  onSelect?: () => void;
}

const itemClass = "w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2 disabled:opacity-50";

export function WorkspaceRecoveryMenuItems({ chatId, worktree, mainWorktree, onSelect }: WorkspaceRecoveryMenuItemsProps) {
  const recreate = useRecreateWorkspace();
  const moveToMain = useMoveChatToMainCheckout();

  if (worktree.is_main || worktree.deleted_at) {
    return null;
  }
  const workspaceName = worktree.branch || worktree.name;

  const handleRecreate = async () => {
    onSelect?.();
    try {
      const result = await recreate.mutateAsync(worktree.id);
      toast.success(result.message || `Workspace ${workspaceName} recreated`, { duration: 6000 });
    } catch (err) {
      void toast.error(err, { duration: 10000 });
    }
  };

  const handleMoveToMain = async () => {
    onSelect?.();
    const where = mainWorktree?.path ? ` (${mainWorktree.path})` : "";
    const confirmed = confirm(
      `Move this chat to the project's main checkout${where}?\n\n` +
        `Its tools will run there — a checkout other chats share — instead of in workspace ${workspaceName}. ` +
        `The workspace itself is kept.`
    );
    if (!confirmed) return;
    try {
      await moveToMain.mutateAsync({ chatId, mainWorktreeId: mainWorktree?.id });
      toast.success("Chat moved to the main checkout");
    } catch (err) {
      void toast.error(err, { duration: 10000 });
    }
  };

  return (
    <>
      <button onClick={handleRecreate} disabled={recreate.isPending} className={itemClass}>
        <RotateCcw className="h-4 w-4" />
        Recreate workspace
      </button>
      <button onClick={handleMoveToMain} disabled={moveToMain.isPending} className={itemClass}>
        <FolderGit2 className="h-4 w-4" />
        Move to main checkout
      </button>
    </>
  );
}
