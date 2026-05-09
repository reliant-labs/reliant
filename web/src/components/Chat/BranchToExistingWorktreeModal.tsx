import { useState, useMemo } from "react";
import { AlertTriangle, FolderSync, FolderGit2 } from "lucide-react";
import { Modal } from "../ui/Modal";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useChatStore } from "../../store/chatStore";
import { useBranchChat } from "../../hooks/message-queries";
import { logger } from "../../lib/logger";
import { cn } from "../../lib/utils";

interface BranchToExistingWorktreeModalProps {
  isOpen: boolean;
  onClose: () => void;
  chatId: string;
  messageId: string;
  projectId: string;
  sourceWorktreeId?: string; // The worktree ID of the chat we're branching from
}

export function BranchToExistingWorktreeModal({
  isOpen,
  onClose,
  chatId,
  messageId,
  projectId,
  sourceWorktreeId,
}: BranchToExistingWorktreeModalProps) {
  const branchChat = useBranchChat();
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  
  const [selectedWorktreeId, setSelectedWorktreeId] = useState<string | null>(null);
  const [isBranching, setIsBranching] = useState(false);
  const [error, setError] = useState<string | null>(null);
  
  // Get available worktrees (exclude the source worktree and archived ones)
  const availableWorktrees = useMemo(() => {
    return worktrees.filter(w => 
      w.project_id === projectId && 
      w.id !== sourceWorktreeId &&
      !w.deleted_at
    );
  }, [worktrees, projectId, sourceWorktreeId]);
  
  // Group worktrees: main first, then by most recent activity
  const sortedWorktrees = useMemo(() => {
    return [...availableWorktrees].sort((a, b) => {
      // Main worktree first
      if (a.is_main && !b.is_main) return -1;
      if (!a.is_main && b.is_main) return 1;
      // Then by last_active (most recent first)
      return new Date(b.last_active).getTime() - new Date(a.last_active).getTime();
    });
  }, [availableWorktrees]);

  const handleBranch = async () => {
    if (!selectedWorktreeId) {
      setError("Please select a workspace");
      return;
    }

    setIsBranching(true);
    setError(null);

    try {
      logger.info("Branching chat to existing worktree:", { 
        chatId, 
        messageId, 
        worktreeId: selectedWorktreeId 
      });
      
      // Branch the chat to the selected worktree
      const { chat: newChat } = await branchChat.mutateAsync({
        chatId,
        messageId,
        worktreeId: selectedWorktreeId,
      });
      
      // Switch to the workspace
      const targetWorktree = worktrees.find(w => w.id === selectedWorktreeId);
      if (targetWorktree) {
        await switchWorktreeContext(projectId, targetWorktree);
      }

      // Navigate to the branched chat
      useChatStore.getState().selectChat(newChat);
      
      onClose();
    } catch (err) {
      logger.error("Failed to branch chat to existing worktree:", err);
      setError(err instanceof Error ? err.message : "Failed to branch chat");
    } finally {
      setIsBranching(false);
    }
  };

  const formatLastActive = (lastActive: string) => {
    const date = new Date(lastActive);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    if (diffMins < 1) return "Just now";
    if (diffMins < 60) return `${diffMins}m ago`;
    if (diffHours < 24) return `${diffHours}h ago`;
    if (diffDays < 7) return `${diffDays}d ago`;
    return date.toLocaleDateString();
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Branch to Existing Workspace"
      size="md"
    >
      <div className="space-y-6">
        {/* Header description */}
        <p className="text-sm text-muted-foreground">
          Create a new chat branch in an existing workspace. The chat context up to this message will be copied.
        </p>

        {error && (
          <div className="p-4 bg-destructive/10 border border-destructive/30 text-destructive rounded-lg text-sm">
            <div className="flex items-start gap-2">
              <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0 text-destructive" aria-hidden />
              <span className="flex-1">{error}</span>
            </div>
          </div>
        )}

        {/* Workspace list */}
        {sortedWorktrees.length > 0 ? (
          <div className="space-y-2">
            <label className="block text-sm font-semibold text-foreground">
              Select Workspace
            </label>
            <div className="max-h-64 overflow-y-auto rounded-lg border border-border">
              {sortedWorktrees.map((worktree) => (
                <WorktreeOption
                  key={worktree.id}
                  worktree={worktree}
                  isSelected={selectedWorktreeId === worktree.id}
                  onSelect={() => setSelectedWorktreeId(worktree.id)}
                  lastActiveText={formatLastActive(worktree.last_active)}
                />
              ))}
            </div>
          </div>
        ) : (
          <div className="text-center py-8 text-muted-foreground">
            <FolderGit2 className="w-12 h-12 mx-auto mb-3 opacity-50" />
            <p className="text-sm font-medium">No other workspaces available</p>
            <p className="text-xs mt-1">
              Create a new workspace first, or use "Branch to New Workspace" instead.
            </p>
          </div>
        )}

        {/* Actions */}
        <div className="flex gap-3 pt-4 border-t border-border">
          <button
            type="button"
            onClick={onClose}
            className="flex-1 px-5 py-3 bg-muted hover:bg-muted/80 border border-border rounded-lg text-sm font-medium transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
            disabled={isBranching}
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleBranch}
            disabled={isBranching || !selectedWorktreeId || sortedWorktrees.length === 0}
            className="flex-1 px-5 py-3 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2"
          >
            <FolderSync className="w-4 h-4" />
            {isBranching ? "Branching..." : "Branch Chat"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// Worktree option component
interface WorktreeOptionProps {
  worktree: Worktree;
  isSelected: boolean;
  onSelect: () => void;
  lastActiveText: string;
}

function WorktreeOption({ worktree, isSelected, onSelect, lastActiveText }: WorktreeOptionProps) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "w-full flex items-start gap-3 p-3 text-left transition-colors border-b border-border/50 last:border-b-0",
        isSelected
          ? "bg-primary/10 hover:bg-primary/15"
          : "hover:bg-muted/50"
      )}
    >
      <div className="relative flex items-center justify-center mt-0.5">
        <div
          className={cn(
            "w-5 h-5 rounded-full border-2 transition-all flex items-center justify-center",
            isSelected
              ? "border-primary bg-primary"
              : "border-border bg-background"
          )}
        >
          {isSelected && (
            <div className="w-2.5 h-2.5 rounded-full bg-white" />
          )}
        </div>
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <FolderGit2 className="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <span className="font-medium text-sm text-foreground truncate">
            {worktree.name}
          </span>
          {worktree.is_main && (
            <span className="text-xs px-1.5 py-0.5 bg-muted rounded text-muted-foreground">
              main
            </span>
          )}
        </div>
        <div className="flex items-center gap-2 mt-1 text-xs text-muted-foreground">
          <span className="font-mono truncate">{worktree.branch}</span>
          <span className="text-border">•</span>
          <span>{lastActiveText}</span>
        </div>
      </div>
    </button>
  );
}