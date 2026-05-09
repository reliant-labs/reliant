import { useEffect, useState, useMemo } from "react";
import { ChevronDown, ChevronRight, Copy, FileText, Settings2 } from "lucide-react";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useChatStore } from "../../store/chatStore";
import { useBranchChat } from "../../hooks/message-queries";
import { usePreferences, useUpdateWorktreePreferences } from "../../hooks/settings-queries";
import { worktreeGrpc } from "../../api/worktree-grpc";
import { logger } from "../../lib/logger";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { toast } from "../../lib/toast-manager";

interface BranchToWorktreeModalProps {
  isOpen: boolean;
  onClose: () => void;
  chatId: string;
  messageId: string;
  projectId: string;
  sourceWorktreeId?: string; // The worktree ID of the chat we're branching from
}

export function BranchToWorktreeModal({
  isOpen,
  onClose,
  chatId,
  messageId,
  projectId,
  sourceWorktreeId,
}: BranchToWorktreeModalProps) {
  const branchChat = useBranchChat();
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const { data: preferences } = usePreferences();
  const branchCopyUncommittedFilesDefault = preferences?.worktree.branchCopyUncommittedFilesDefault ?? false;
  const updateWorktreePrefs = useUpdateWorktreePreferences();
  
  // Track the current branch of the source worktree (from git status)
  const [currentBranch, setCurrentBranch] = useState<string | undefined>();
  
  // Track modified/untracked files that need to be copied to the new worktree
  // Git worktrees only share committed changes, so we need to explicitly copy
  // any uncommitted changes (modified + untracked files) to the new worktree
  const [changedFiles, setChangedFiles] = useState<string[]>([]);
  
  // Toggle for copying uncommitted files - defaults to user's setting
  const [copyUncommittedFiles, setCopyUncommittedFiles] = useState(branchCopyUncommittedFilesDefault);
  
  // Track whether file list is expanded
  const [isFileListExpanded, setIsFileListExpanded] = useState(false);
  
  // Check if current setting differs from saved default
  const isDifferentFromDefault = copyUncommittedFiles !== branchCopyUncommittedFilesDefault;
  
  // Handle setting current choice as new default
  const handleSetAsDefault = async () => {
    try {
      await updateWorktreePrefs.mutateAsync({
        branchCopyUncommittedFilesDefault: copyUncommittedFiles,
      });
    } catch (error) {
      logger.error("Failed to update default preference:", error);
    }
  };
  
  // Reset copyUncommittedFiles when modal opens based on user's current preference
  useEffect(() => {
    if (isOpen) {
      setCopyUncommittedFiles(branchCopyUncommittedFilesDefault);
      setIsFileListExpanded(false);
    }
  }, [isOpen, branchCopyUncommittedFilesDefault]);
  
  // Get the source worktree from the chat's worktree_id, or fall back to main
  const sourceWorktree = useMemo(() => {
    // If sourceWorktreeId is provided (from the chat), find that worktree
    if (sourceWorktreeId) {
      const worktree = worktrees.find(w => w.id === sourceWorktreeId);
      if (worktree) {
        return worktree;
      }
    }
    // Otherwise, fallback to the main worktree for this project
    return worktrees.find(w => w.is_main && w.project_id === projectId) || null;
  }, [sourceWorktreeId, worktrees, projectId]);
  
  // Fetch current branch and changed files when modal opens
  useEffect(() => {
    if (isOpen && sourceWorktree?.id) {
      worktreeGrpc.getGitStatus(sourceWorktree.id)
        .then((status) => {
          logger.info("Got git status for new worktree:", { 
            currentBranch: status.current_branch,
            worktreeId: sourceWorktree.id,
            worktreeName: sourceWorktree.name,
            modifiedFiles: status.modified_files,
            untrackedFiles: status.untracked_files,
            stagedFiles: status.staged_files,
          });
          setCurrentBranch(status.current_branch);
          
          // Collect all uncommitted files (modified, staged, and untracked)
          // These need to be copied since git worktrees only share committed changes
          const allChangedFiles = [
            ...(status.modified_files || []),
            ...(status.staged_files || []),
            ...(status.untracked_files || []),
          ];
          // Remove duplicates
          const uniqueFiles = [...new Set(allChangedFiles)];
          setChangedFiles(uniqueFiles);
        })
        .catch((error) => {
          logger.warn("Failed to fetch git status:", error);
          setCurrentBranch(undefined);
          setChangedFiles([]);
        });
    } else {
      setCurrentBranch(undefined);
      setChangedFiles([]);
    }
  }, [isOpen, sourceWorktree?.id, sourceWorktree?.name]);

  const handleWorktreeCreated = async (worktreeId: string) => {
    try {
      logger.info("Worktree created, branching chat:", { worktreeId, chatId, messageId });
      
      // Branch the chat to the new worktree with workspace context
      const { chat: newChat } = await branchChat.mutateAsync({
        chatId,
        messageId,
        worktreeId,
      });
      
      // Select the new worktree
      const newWorktree = worktrees.find(w => w.id === worktreeId);
      if (newWorktree) {
        await switchWorktreeContext(projectId, newWorktree);
      }

      // Navigate to the branched chat
      useChatStore.getState().selectChat(newChat);
      
      onClose();
    } catch (error) {
      logger.error("Failed to branch chat to worktree:", error);
      const errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      toast.error(`Failed to branch chat to workspace: ${errorMessage}`, { duration: 5000 });
    }
  };

  // Only pass changed files if toggle is on
  const filesToCopy = copyUncommittedFiles ? changedFiles : [];

  // Git worktrees only share committed changes, so we need to explicitly copy:
  // 1. gitignored files (.env, .env.local) - handled by CreateWorktreeModal defaults
  // 2. modified/untracked files from source worktree - passed via additionalCopyFiles (if enabled)
  return (
    <CreateWorktreeModal
      isOpen={isOpen}
      onClose={onClose}
      onWorktreeCreated={handleWorktreeCreated}
      projectId={projectId}
      title="Branch to New Workspace"
      sourceWorktreeId={sourceWorktree?.id}
      sourceWorktreeBranch={currentBranch}
      additionalCopyFiles={filesToCopy}
      lockBaseBranch={true}
      extraContent={
        changedFiles.length > 0 && (
          <div className="space-y-3 pt-2 border-t border-border/50">
            {/* Copy uncommitted files toggle */}
            <label className="flex items-start gap-3 cursor-pointer group">
              <div className="relative flex items-center justify-center mt-0.5">
                <input
                  type="checkbox"
                  checked={copyUncommittedFiles}
                  onChange={(e) => setCopyUncommittedFiles(e.target.checked)}
                  className="sr-only"
                />
                <div
                  className={cn(
                    "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                    copyUncommittedFiles
                      ? "border-foreground bg-background"
                      : "border-border bg-background"
                  )}
                >
                  {copyUncommittedFiles && (
                    <svg className="w-3 h-3 text-foreground" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
                    </svg>
                  )}
                </div>
              </div>
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <Copy className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm font-medium text-foreground">
                    Copy uncommitted files
                  </span>
                  <span className="text-xs text-muted-foreground">
                    ({changedFiles.length} file{changedFiles.length !== 1 ? 's' : ''})
                  </span>
                </div>
                <p className="text-xs text-muted-foreground mt-1">
                  Include modified and untracked files from the source workspace. Directories are copied recursively.
                </p>
              </div>
              {/* Set as default button */}
              {isDifferentFromDefault && (
                <Tooltip content="Save this as your default setting">
                  <button
                    type="button"
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      handleSetAsDefault();
                    }}
                    className="flex items-center gap-1 px-2 py-1 text-xs text-muted-foreground hover:text-foreground hover:bg-muted rounded transition-colors"
                  >
                    <Settings2 className="w-3 h-3" />
                    <span>Set as default</span>
                  </button>
                </Tooltip>
              )}
            </label>

            {/* Expandable file list */}
            {copyUncommittedFiles && changedFiles.length > 0 && (
              <div className="ml-8">
                <button
                  type="button"
                  onClick={() => setIsFileListExpanded(!isFileListExpanded)}
                  className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
                >
                  {isFileListExpanded ? (
                    <ChevronDown className="w-3.5 h-3.5" />
                  ) : (
                    <ChevronRight className="w-3.5 h-3.5" />
                  )}
                  <span>{isFileListExpanded ? 'Hide' : 'Show'} files to copy</span>
                </button>
                
                {isFileListExpanded && (
                  <div className="mt-2 max-h-32 overflow-y-auto rounded-md border border-border/50 bg-muted/30">
                    <ul className="py-1">
                      {changedFiles.map((file, index) => (
                        <li
                          key={index}
                          className="flex items-center gap-2 px-2 py-1 text-xs font-mono text-muted-foreground hover:bg-muted/50"
                        >
                          <FileText className="w-3 h-3 flex-shrink-0" />
                          <span className="truncate">{file}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            )}
          </div>
        )
      }
    />
  );
}