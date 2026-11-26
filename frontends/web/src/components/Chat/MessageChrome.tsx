/**
 * The chrome a transcript row carries for the message behind it: when it was
 * written, and branching the chat from it. Shared by ChatMessage and the
 * simplified thread's work fold, so a fold branches exactly as a message does.
 */

import { createContext, useCallback, useContext, useState, type JSX, type ReactNode } from "react";
import { FolderSync, GitBranch, MoreHorizontal } from "lucide-react";
import { MessageActionsSheet } from "./MessageActionsSheet";
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

// Route-owned surfaces supply navigation; desktop keeps its existing tab navigation.
const BranchNavigationContext = createContext<((chatId: string) => void) | undefined>(undefined);

export function BranchNavigationProvider({ onNavigate, children }: { onNavigate: (chatId: string) => void; children: ReactNode }) {
  return <BranchNavigationContext.Provider value={onNavigate}>{children}</BranchNavigationContext.Provider>;
}

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
      className={cn("text-meta leading-none text-muted-foreground", className)}
      title={new Date(at).toLocaleString()}
    >
      {formatRelativeTimestamp(at)}
    </time>
  );
}

export interface MessageBranching {
  /**
   * Whether this person may branch the chat at all: not a viewer of another
   * member's shared chat (the server refuses them BranchChat; a branch would
   * copy someone else's transcript into the viewer's own chats).
   */
  available: boolean;
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
  const navigateToBranch = useContext(BranchNavigationContext);
  const currentProject = useProjectStore((state) => state.currentProject);
  const sourceChat = useChat(effectiveChatId || "");
  const sourceWorktreeId = sourceChat?.worktreeId;
  const available = !sourceChat?.viewerAccess;
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
      const newChat = await useChatStore.getState().branchChat(effectiveChatId, messageId);
      if (newChat) navigateToBranch?.(newChat.id);
    } catch (error) {
      logger.error("Failed to branch chat:", error);
      const errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      toast.error(`Failed to branch chat: ${errorMessage}`, { duration: 5000 });
    }
  }, [effectiveChatId, messageId, navigateToBranch]);

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
          onBranched={navigateToBranch}
        />
      )}
    </>
  );

  return { available, openBranchMenu, branchInPlace, branchToExistingWorkspace, branchUi };
}

/** Actions for a work-only fold, whose message has no prose toolbar. */
export function MobileWorkBranchActions({ chatId, messageId }: { chatId?: string; messageId: string }) {
  const [open, setOpen] = useState(false);
  const { available, branchInPlace, branchToExistingWorkspace, branchUi } = useMessageBranching(chatId, messageId);
  if (!available) return null;
  return (
    <>
      <button type="button" aria-label="Activity actions" aria-haspopup="dialog" aria-expanded={open}
        onClick={() => setOpen(true)}
        className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring">
        <MoreHorizontal className="h-4 w-4" />
      </button>
      {branchUi}
      <MessageActionsSheet isOpen={open} onClose={() => setOpen(false)} actions={[
        { key: "branch", label: "Branch in place", icon: <GitBranch className="h-4 w-4" />, onSelect: () => void branchInPlace() },
        { key: "branch-existing", label: "Branch to existing workspace", icon: <FolderSync className="h-4 w-4" />, onSelect: branchToExistingWorkspace },
      ]} />
    </>
  );
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
  const { available, openBranchMenu, branchUi } = useMessageBranching(chatId, messageId);
  if (!available) return null;
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
