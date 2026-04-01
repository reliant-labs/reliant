import { useState, useEffect } from "react";
import { GitPullRequest, Loader2, Check, ArrowUp, ArrowDown } from "lucide-react";
import { cn } from "../../lib/utils";
import * as gitApi from "../../api/git";
import { logger } from "../../lib/logger";
import { SidebarInput } from "../RightSidebar/shared";
import { Tooltip } from "../ui/Tooltip";

interface SourceControlPanelProps {
  worktreeId: string;
  stagedFilesCount: number;
  hasUnstagedChanges: boolean;
  ahead: number;
  behind: number;
  branch: string;
  defaultBranch?: string;
  onCommitSuccess?: () => void;
  onPushSuccess?: () => void;
  onOpenPRDialog?: () => void;
  prRefreshTrigger?: number; // Increment to trigger PR status refresh
  className?: string;
}

export function SourceControlPanel({
  worktreeId,
  stagedFilesCount,
  hasUnstagedChanges,
  ahead,
  behind,
  branch,
  defaultBranch,
  onCommitSuccess,
  onPushSuccess,
  onOpenPRDialog,
  prRefreshTrigger = 0,
  className,
}: SourceControlPanelProps) {
  const [commitMessage, setCommitMessage] = useState("");
  const [isCommitting, setIsCommitting] = useState(false);
  const [isSyncing, setIsSyncing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [existingPR, setExistingPR] = useState<gitApi.ExistingPRResponse | null>(null);
  const [_isCheckingPR, setIsCheckingPR] = useState(false);
  const [ghCliMissing, setGhCliMissing] = useState(false);

  // Determine which mode we're in:
  // - "commit" mode: there are staged files ready to commit
  // - "push" mode: no changes, but we have commits to push (ahead > 0)
  // - "pull" mode: no changes, but we have commits to pull (behind > 0)
  const hasAnyChanges = stagedFilesCount > 0 || hasUnstagedChanges;
  const showPushMode = !hasAnyChanges && ahead > 0;
  const showPullMode = !hasAnyChanges && behind > 0 && ahead === 0;
  const isDefaultBranch = !!defaultBranch && branch === defaultBranch;
  const prDisabled = isSyncing || isCommitting || ghCliMissing || isDefaultBranch;
  const prTooltip = ghCliMissing
    ? "GitHub CLI (gh) not installed. Install from cli.github.com"
    : isDefaultBranch
      ? "Cannot create PR from the default branch"
      : "Create pull request";

  // Check for existing PR when worktree changes or when triggered by parent
  useEffect(() => {
    const checkExistingPR = async () => {
      setIsCheckingPR(true);
      setGhCliMissing(false);
      try {
        const prInfo = await gitApi.getExistingPR(worktreeId);
        setExistingPR(prInfo);
        if (prInfo.exists) {
          logger.info("Found existing PR for worktree", { url: prInfo.url, state: prInfo.state });
        }
      } catch (err) {
        // Check if this is a gh CLI missing error
        const errorMessage = err instanceof Error ? err.message : String(err);
        if (errorMessage.includes("GitHub CLI (gh) is not installed")) {
          setGhCliMissing(true);
          logger.info("GitHub CLI not installed - PR features disabled");
        } else {
          // Don't treat other errors as fatal - just means we couldn't check
          logger.debug("Could not check for existing PR", err);
        }
        setExistingPR(null);
      } finally {
        setIsCheckingPR(false);
      }
    };

    checkExistingPR();
  }, [worktreeId, prRefreshTrigger]);

  const handleCommit = async () => {
    if (!commitMessage.trim()) {
      setError("Commit message is required");
      return;
    }

    if (stagedFilesCount === 0) {
      setError("No files staged for commit");
      return;
    }

    setIsCommitting(true);
    setError(null);

    try {
      await gitApi.commitChanges(worktreeId, commitMessage.trim());
      setCommitMessage("");
      onCommitSuccess?.();
      logger.info("Changes committed successfully");
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to commit changes";
      setError(errorMessage);
      logger.error("Failed to commit changes", err);
    } finally {
      setIsCommitting(false);
    }
  };

  const handlePush = async () => {
    setIsSyncing(true);
    setError(null);

    try {
      await gitApi.pushChanges(worktreeId);
      onPushSuccess?.();
      logger.info("Changes pushed successfully");
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to push changes";
      setError(errorMessage);
      logger.error("Failed to push changes", err);
    } finally {
      setIsSyncing(false);
    }
  };

  const handlePull = async () => {
    setIsSyncing(true);
    setError(null);

    try {
      await gitApi.pullChanges(worktreeId);
      onPushSuccess?.(); // Refresh the view
      logger.info("Changes pulled successfully");
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to pull changes";
      setError(errorMessage);
      logger.error("Failed to pull changes", err);
    } finally {
      setIsSyncing(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    // Cmd/Ctrl + Enter to commit (only in commit mode, not push or pull mode)
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !showPushMode && !showPullMode) {
      e.preventDefault();
      handleCommit();
    }
  };

  const canCommit = commitMessage.trim().length > 0 && stagedFilesCount > 0 && !isCommitting;
  const canPush = ahead > 0 && !isSyncing && !hasAnyChanges;
  const canPull = behind > 0 && !isSyncing && !hasAnyChanges && ahead === 0;

  // Determine button state and label
  const getButtonState = () => {
    if (showPullMode) {
      return {
        label: `Pull (${behind})`,
        icon: ArrowDown,
        onClick: handlePull,
        disabled: !canPull,
        loading: isSyncing,
        title: `Pull ${behind} commit${behind === 1 ? '' : 's'}`,
      };
    }
    if (showPushMode) {
      return {
        label: `Push (${ahead})`,
        icon: ArrowUp,
        onClick: handlePush,
        disabled: !canPush,
        loading: isSyncing,
        title: `Push ${ahead} commit${ahead === 1 ? '' : 's'}`,
      };
    }
    // Default to commit mode
    return {
      label: "Commit",
      icon: Check,
      onClick: handleCommit,
      disabled: !canCommit,
      loading: isCommitting,
      title: `Commit ${stagedFilesCount} ${stagedFilesCount === 1 ? 'file' : 'files'} (⌘+Enter)`,
    };
  };

  const buttonState = getButtonState();
  const ButtonIcon = buttonState.icon;

  return (
    <div className={cn("flex flex-col gap-2 p-2 bg-background/95", className)}>
      {/* Error Display */}
      {error && (
        <div className="text-xs text-destructive bg-destructive/10 px-2 py-1 rounded border border-destructive/20">
          {error}
        </div>
      )}

      {/* Commit Input - full width */}
      <SidebarInput
        placeholder={`Message (⌘⏎ to commit on "${branch}")`}
        value={commitMessage}
        onChange={(value) => {
          setCommitMessage(value);
          setError(null);
        }}
        onKeyDown={handleKeyDown}
        showClear={false}
        rightContent={
          commitMessage ? (
            <span className="text-xs text-muted-foreground">
              {commitMessage.length}
            </span>
          ) : undefined
        }
        disabled={isCommitting || isSyncing}
        wrapperClassName="w-full"
      />

      {/* Action Buttons - side by side */}
      <div className="flex items-center gap-2 w-full">
        {/* Main Action Button - Commit/Push/Pull */}
        <Tooltip content={buttonState.title}>
          <button
            onClick={buttonState.onClick}
            disabled={buttonState.disabled}
            style={{
              backgroundColor: 'hsl(var(--primary))',
              color: 'hsl(var(--primary-foreground))',
              borderColor: 'hsl(var(--primary) / 0.2)',
            }}
            className={cn(
              "flex-1 h-9 px-3 rounded-md text-sm font-medium transition-all flex items-center justify-center gap-1.5",
              "border hover:opacity-90 hover:brightness-95",
              "disabled:opacity-50 disabled:cursor-not-allowed",
              buttonState.loading && "opacity-70"
            )}
          >
            {buttonState.loading ? (
              <>
                <Loader2 className="w-4 h-4 animate-spin" />
                <span>{buttonState.label.startsWith("Commit") ? "Committing" : buttonState.label.startsWith("Push") ? "Pushing" : "Pulling"}...</span>
              </>
            ) : (
              <>
                <ButtonIcon className="w-4 h-4" />
                <span>{buttonState.label}</span>
              </>
            )}
          </button>
        </Tooltip>

        {/* PR button - side by side */}
        {existingPR?.exists && existingPR.url ? (
          <Tooltip content={`View PR #${existingPR.number}: ${existingPR.title}`}>
            <a
              href={existingPR.url}
              target="_blank"
              rel="noopener noreferrer"
              className={cn(
                "flex-1 h-9 rounded-md text-sm font-medium transition-all flex items-center justify-center gap-1.5",
                "border border-primary/40 bg-primary/10 hover:bg-primary/20 text-primary"
              )}
            >
              <GitPullRequest className="w-3.5 h-3.5" />
              <span className="truncate">PR #{existingPR.number}</span>
            </a>
          </Tooltip>
        ) : (
          <Tooltip content={prTooltip}>
            <button
              onClick={prDisabled ? undefined : onOpenPRDialog}
              disabled={prDisabled}
              className={cn(
                "flex-1 h-9 rounded-md text-sm font-medium transition-all flex items-center justify-center gap-1.5",
                "border border-border bg-background btn-hover-bg-muted hover:border-primary/40 text-foreground",
                prDisabled && "opacity-50 cursor-not-allowed"
              )}
            >
              <GitPullRequest className="w-3.5 h-3.5" />
              <span>Create PR</span>
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  );
}
