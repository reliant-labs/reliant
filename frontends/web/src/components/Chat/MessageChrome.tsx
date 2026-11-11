/**
 * The chrome a transcript row carries for the message behind it: when it was
 * written, and branching the chat from it. Shared by ChatMessage and the
 * simplified thread's work fold, so a fold branches exactly as a message does.
 */

import { useCallback, useState, type JSX } from "react";
import { GitBranch } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";
import { useChatStore } from "../../store/chatStore"; // For getState() only
import { useChatIdOrActive, useChat } from "../../store/chatStoreHooks";
import { useProjectStore } from "../../store/projectStore";
import { BranchOptionsMenu } from "./BranchOptionsMenu";
import { BranchToWorktreeModal } from "./BranchToWorktreeModal";
import { BranchToExistingWorktreeModal } from "./BranchToExistingWorktreeModal";

/** A message's age in words ("2 minutes ago"). */
export function formatRelativeTimestamp(dateString: string, now: number = Date.now()): string {
  const seconds = Math.floor((now - new Date(dateString).getTime()) / 1000);

  if (seconds < 60) return "less than a minute ago";
  if (seconds < 120) return "1 minute ago";
  if (seconds < 3600) return `${Math.floor(seconds / 60)} minutes ago`;
  if (seconds < 7200) return "1 hour ago";
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} hours ago`;
  if (seconds < 172800) return "1 day ago";
  if (seconds < 2592000) return `${Math.floor(seconds / 86400)} days ago`;
  if (seconds < 5184000) return "1 month ago";
  if (seconds < 31536000) return `${Math.floor(seconds / 2592000)} months ago`;
  return `${Math.floor(seconds / 31536000)} year${Math.floor(seconds / 31536000) > 1 ? "s" : ""} ago`;
}

/** Relative time, with the full date and time on hover. */
export function MessageTimestamp({ at, className }: { at: string; className?: string }) {
  return (
    <time
      dateTime={at}
      className={cn("text-xs leading-none text-muted-foreground", className)}
      title={new Date(at).toLocaleString()}
    >
      {formatRelativeTimestamp(at)}
    </time>
  );
}

export interface MessageBranching {
  /** Opens the branch options menu under the clicked control. */
  openBranchMenu: (event: React.MouseEvent) => void;
  /** Branches the chat at the message, in the same worktree. */
  branchInPlace: () => Promise<void>;
  /** Opens the branch-to-an-existing-workspace modal. */
  branchToExistingWorkspace: () => void;
  /** The menu and modals; render it once, anywhere in the row. */
  branchUi: JSX.Element;
}

/** Branching the chat from one message: the menu, its modals and handlers. */
export function useMessageBranching(chatId: string | undefined | null, messageId: string): MessageBranching {
  const effectiveChatId = useChatIdOrActive(chatId);
  const currentProject = useProjectStore((state) => state.currentProject);
  const sourceWorktreeId = useChat(effectiveChatId || "")?.worktreeId;
  const [menuPosition, setMenuPosition] = useState<{ x: number; y: number } | null>(null);
  const [showNewWorktree, setShowNewWorktree] = useState(false);
  const [showExistingWorktree, setShowExistingWorktree] = useState(false);

  const openBranchMenu = useCallback((event: React.MouseEvent) => {
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuPosition({ x: rect.left, y: rect.bottom + 4 });
  }, []);

  const branchInPlace = useCallback(async () => {
    if (!effectiveChatId) {
      logger.error("No chat ID available for branching");
      return;
    }
    try {
      logger.info("Branching chat:", { chatId: effectiveChatId, messageId });
      await useChatStore.getState().branchChat(effectiveChatId, messageId);
    } catch (error) {
      logger.error("Failed to branch chat:", error);
      const errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      toast.error(`Failed to branch chat: ${errorMessage}`, { duration: 5000 });
    }
  }, [effectiveChatId, messageId]);

  const branchToExistingWorkspace = useCallback(() => setShowExistingWorktree(true), []);

  const branchUi = (
    <>
      {menuPosition && (
        <BranchOptionsMenu
          position={menuPosition}
          onClose={() => setMenuPosition(null)}
          onBranchChat={branchInPlace}
          onBranchToWorkspace={() => setShowNewWorktree(true)}
          onBranchToExistingWorkspace={branchToExistingWorkspace}
        />
      )}
      {showNewWorktree && effectiveChatId && currentProject && (
        <BranchToWorktreeModal
          isOpen={showNewWorktree}
          onClose={() => setShowNewWorktree(false)}
          chatId={effectiveChatId}
          messageId={messageId}
          projectId={currentProject.id}
          sourceWorktreeId={sourceWorktreeId}
        />
      )}
      {showExistingWorktree && effectiveChatId && currentProject && (
        <BranchToExistingWorktreeModal
          isOpen={showExistingWorktree}
          onClose={() => setShowExistingWorktree(false)}
          chatId={effectiveChatId}
          messageId={messageId}
          projectId={currentProject.id}
          sourceWorktreeId={sourceWorktreeId}
        />
      )}
    </>
  );

  return { openBranchMenu, branchInPlace, branchToExistingWorkspace, branchUi };
}

/** The branch control ChatMessage's toolbar uses, as a standalone button. */
export function BranchFromMessageButton({
  chatId,
  messageId,
  className,
}: {
  chatId?: string;
  messageId: string;
  className?: string;
}) {
  const { openBranchMenu, branchUi } = useMessageBranching(chatId, messageId);
  return (
    <>
      <Tooltip content="Branch from this message" side="top" delay={300} triggerClassName="inline-flex">
        <button
          type="button"
          onClick={(event) => {
            event.stopPropagation();
            openBranchMenu(event);
          }}
          aria-label="Branch from message"
          className={cn(
            "rounded p-1 text-foreground/70 transition-colors duration-150 hover:bg-muted hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring",
            className,
          )}
        >
          <GitBranch className="h-3.5 w-3.5" />
        </button>
      </Tooltip>
      {branchUi}
    </>
  );
}
