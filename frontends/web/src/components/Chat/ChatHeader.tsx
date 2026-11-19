/**
 * ChatHeader - the one bar above the transcript.
 *
 * Shows, in one row at the shell's bar height:
 * - the chat title (editable), and its shared / no-machine state
 * - the prompt scrolled out of view (rendered into the bar by the timeline)
 * - a thread switcher, a runs count and context usage, when they apply
 * - every other action in the ⋯ menu
 *
 * The menu's actions live in `useChatActions`, shared with the mobile Chat
 * sheet; this component owns only the dropdown and the inline title editor.
 * The mobile chat screen does not render it at all (its top bar replaces it).
 */

import { useMemo, useState, useEffect, useRef } from "react";
import { useContextUsage, useContextUsageByThread } from "../../store/chatStoreHooks";
import {
  MoreHorizontal,
  Copy,
  Edit2,
  Archive,
  Download,
  Terminal,
  Workflow,
  ArrowLeft,
  Check,
  ListCollapse,
  FolderInput,
  Users,
  Bot,
} from "lucide-react";
import { formatDistanceToNow } from "date-fns";
import { useChat } from "../../hooks/chat-queries";
import { NoMachinePill } from "./NoMachine";
import { WorkspaceRecoveryMenuItems } from "./WorkspaceRecoveryMenuItems";
import { useMessages } from "../../hooks/message-queries";
import { ContextUsageIndicator } from "./ContextUsageIndicator";
import Tooltip from "../forge-ui/tooltip";
import { useThreads, ThreadTabs } from "./thread-views";
import { Link } from "@tanstack/react-router";
import { useChildRuns } from "../../hooks/run-queries";
import { useCapability } from "../../lib/surfaceContext";
import { runsPath } from "../../lib/paths";
import { cn } from "../../lib/utils";
import type { WorkflowExecution } from "./ExecutionSidebar/types";
import { useChatActions } from "./useChatActions";
import { MoveChatToProjectDialog } from "./MoveChatToProjectDialog";
import { useThreadNavigation } from "./useThreadNavigation";
import { setChatThreadMode, useChatThreadMode } from "./thread-views/useChatThreadMode";
import { ShareChatDialog } from "../Sharing/ShareChatDialog";
import { ResourceVisibility } from "../../gen/reliant/v1/common_pb";
import { hasControlPlane } from "../../services/controlPlane/config";

interface ChatHeaderProps {
  chatId: string | null;
  /** For new chat view - display "New Chat" with no editing */
  isNewChat?: boolean;
  /** Workspace ID override for new chat view */
  worktreeId?: string;
  /** Selected thread ID for filtering (null = all threads) */
  selectedThreadId?: string | null;
  /** Callback when thread selection changes */
  onSelectThread?: (threadId: string | null) => void;
  /** Workflow execution tree(s) for thread metadata */
  workflowExecution?: WorkflowExecution | WorkflowExecution[];
  /**
   * Opens/closes the workflow viewer. The always-visible workflow chip was
   * removed from the header for compactness; this moved into the overflow menu
   * so the viewer stays reachable.
   */
  onToggleWorkflowViewer?: () => void;
  /** Whether the workflow viewer is currently open (labels the menu item) */
  isWorkflowViewerOpen?: boolean;
  /**
   * The host renders the chat's title itself (run detail's RunHeader). The
   * menu, context usage and thread tabs stay; the title and time-ago go.
   */
  hideTitle?: boolean;
  /**
   * Receives the element in the bar where the timeline renders the prompt
   * you scrolled away from (InterleavedTimeline's `pinnedPromptSlot`).
   */
  pinnedPromptSlotRef?: (el: HTMLElement | null) => void;
}

export function ChatHeader({
  chatId,
  isNewChat = false,
  worktreeId: propWorktreeId,
  selectedThreadId,
  onSelectThread,
  workflowExecution,
  onToggleWorkflowViewer,
  isWorkflowViewerOpen,
  hideTitle = false,
  pinnedPromptSlotRef,
}: ChatHeaderProps) {
  const chatQuery = useChat(chatId || undefined);
  const chat = chatQuery.data;
  const messagesQuery = useMessages(chatId || undefined);
  const messages = messagesQuery.data?.messages ?? [];
  const actions = useChatActions(chatId, propWorktreeId);
  const { mode: threadMode } = useChatThreadMode();
  const isSimplified = threadMode === "simplified";

  // Get threads from messages with workflow metadata for better names. ThreadTabs
  // filters spawn-origin threads at the render boundary; keep the full list here
  // so selected spawned threads can still resolve parent/back navigation.
  const threads = useThreads(messages, chatId || "", workflowExecution);
  const {
    hasMultipleThreads,
    isInSubThread,
    backTarget: backTargetThread,
    backTargetId,
  } = useThreadNavigation(threads, selectedThreadId);
  const showBackButton = isInSubThread && Boolean(onSelectThread);

  // Context usage for compaction indicator
  const contextUsage = useContextUsage(chatId || "");
  const contextUsageByThread = useContextUsageByThread(chatId || "");

  const [isMenuOpen, setIsMenuOpen] = useState(false);
  const [isMoveOpen, setIsMoveOpen] = useState(false);
  const [isShareOpen, setIsShareOpen] = useState(false);
  // A viewer reads someone else's shared chat: nothing here may change it.
  const isViewer = !!chat?.viewerAccess;
  const isShared = chat?.visibility === ResourceVisibility.ORG;
  // Only the owner shares, and only a chat of an org (SharingService).
  const canShare = hasControlPlane && !isViewer && !!chat?.orgId;
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editedTitle, setEditedTitle] = useState("");
  const menuRef = useRef<HTMLDivElement>(null);
  const titleInputRef = useRef<HTMLInputElement>(null);

  // Determine the title to show
  const title = isNewChat ? "New Chat" : (chat?.title || "New Chat");

  // Format time ago
  const timeAgo = useMemo(() => {
    if (isNewChat || !chat?.createdAt) return null;
    try {
      return formatDistanceToNow(new Date(chat.createdAt), { addSuffix: false });
    } catch {
      return null;
    }
  }, [chat?.createdAt, isNewChat]);

  // Close menu when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setIsMenuOpen(false);
      }
    };

    if (isMenuOpen) {
      document.addEventListener('mousedown', handleClickOutside);
      return () => document.removeEventListener('mousedown', handleClickOutside);
    }
  }, [isMenuOpen]);

  // Handle inline title editing
  const handleTitleClick = () => {
    if (isNewChat || isViewer) return; // A new chat has no title yet; a viewer's is read-only
    setEditedTitle(chat?.title || "");
    setIsEditingTitle(true);
    setTimeout(() => titleInputRef.current?.select(), 0);
  };

  const handleSaveTitle = async () => {
    if (!(await actions.rename(editedTitle))) return;
    setIsEditingTitle(false);
    setEditedTitle("");
  };

  const handleCancelEdit = () => {
    setIsEditingTitle(false);
    setEditedTitle("");
  };

  const handleTitleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      handleSaveTitle();
    } else if (e.key === 'Escape') {
      handleCancelEdit();
    }
  };

  const handleTitleBlur = () => {
    handleSaveTitle();
  };

  // Each menu item closes the menu, then runs the shared action.
  const runAndClose = (action: () => unknown) => () => {
    setIsMenuOpen(false);
    void action();
  };

  // Focus title input when editing starts
  useEffect(() => {
    if (isEditingTitle && titleInputRef.current) {
      titleInputRef.current.focus();
    }
  }, [isEditingTitle]);

  const menuItemClass =
    "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-ui text-foreground transition-colors hover:bg-foreground/5 focus-visible:bg-foreground/5 focus-visible:outline-none";
  const iconButtonClass =
    "touch-target inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

  // One bar above the transcript: the title, a couple of quiet indicators,
  // and everything else in the ⋯ menu. Threads are one switcher, not a row
  // of tabs; the prompt you scrolled away from is a line inside the bar.
  return (
    <header
      className="flex h-[var(--shell-bar-h)] flex-shrink-0 items-center gap-1 border-b border-border-subtle px-3 sm:px-5"
      data-testid="chat-top-bar"
    >
      {/* Back to parent thread - only when viewing a spawned/child thread */}
      {showBackButton && (
        <Tooltip
          content={backTargetThread ? `Back to ${backTargetThread.name}` : "Back to main thread"}
          side="bottom"
        >
          <button
            onClick={() => onSelectThread?.(backTargetId)}
            className={iconButtonClass}
            aria-label="Back to parent thread"
          >
            <ArrowLeft className="size-4" />
          </button>
        </Tooltip>
      )}

      {/* Title - click to edit inline */}
      {hideTitle && !isEditingTitle ? null : isEditingTitle ? (
        <input
          ref={titleInputRef}
          type="text"
          value={editedTitle}
          onChange={(e) => setEditedTitle(e.target.value)}
          onKeyDown={handleTitleKeyDown}
          onBlur={handleTitleBlur}
          aria-label="Chat title"
          className="h-8 min-w-0 flex-1 rounded-lg border border-ring bg-transparent px-2 text-ui font-medium text-foreground focus:outline-none"
        />
      ) : (
        <h1
          onClick={handleTitleClick}
          title={title}
          className={cn(
            "min-w-0 truncate rounded-md px-1 text-ui font-medium text-foreground transition-colors",
            !(isNewChat || isViewer) && "cursor-text hover:text-foreground/80",
          )}
        >
          {title}
        </h1>
      )}

      {/* A chat with no machine says so where the user looks first, and
          the pill is the way to connect one (NO_MACHINE_CHATS.md §2.4). */}
      {!isNewChat && chatId && chat?.noMachine && !isViewer && <NoMachinePill chatId={chatId} />}

      {/* Shared with the org: the owner's reminder, the viewer's "read only". */}
      {!isNewChat && (isShared || isViewer) && (
        <span
          className="inline-flex flex-shrink-0 items-center gap-1 px-1 text-meta text-muted-foreground"
          data-testid="chat-shared-chip"
        >
          <Users className="size-3.5" aria-hidden />
          {isViewer ? "Read only" : "Shared"}
        </span>
      )}

      <div className="min-w-2 flex-1" />

      {/* The prompt scrolled out of view: the timeline renders it here. */}
      {pinnedPromptSlotRef && (
        <div ref={pinnedPromptSlotRef} className="flex min-w-0 max-w-[40%] shrink justify-end" data-testid="chat-pinned-prompt-slot" />
      )}

      {/* Threads: one switcher, only when there is more than one. */}
      {!isNewChat && hasMultipleThreads && onSelectThread && (
        <ThreadTabs
          threads={threads}
          selectedThreadId={selectedThreadId ?? null}
          onSelectThread={onSelectThread}
          showAllOption={true}
          contextUsageByThread={contextUsageByThread}
          chatId={chatId || undefined}
        />
      )}

      {/* Runs an agent started from this chat (decision 4): a count that
          opens them. Renders nothing for a chat that started none. */}
      {!isNewChat && chatId && <ChildRunsLink chatId={chatId} />}

      {/* Context usage - the one stat that changes what you do next */}
      {!isNewChat && (
        <ContextUsageIndicator
          threadTokenCount={contextUsage?.threadTokenCount ?? 0}
          compactionThreshold={contextUsage?.compactionThreshold ?? 200000}
          compact={true}
        />
      )}

      {/* Secondary actions */}
      {!isNewChat && chatId && (
        <div className="relative flex-shrink-0" ref={menuRef}>
          <Tooltip content="Chat options" side="bottom" delay={300} triggerClassName="inline-flex">
            <button
              onClick={() => setIsMenuOpen(!isMenuOpen)}
              className={iconButtonClass}
              aria-label="More options"
              aria-expanded={isMenuOpen}
            >
              <MoreHorizontal className="size-4" />
            </button>
          </Tooltip>

          {isMenuOpen && (
            <div className="absolute right-0 top-full z-50 mt-1 w-64 rounded-xl border border-border bg-popover p-1 shadow-lg">
              {!isViewer && (
                <button
                  onClick={() => {
                    handleTitleClick();
                    setIsMenuOpen(false);
                  }}
                  className={menuItemClass}
                >
                  <Edit2 className="size-4 text-muted-foreground" />
                  Edit title
                </button>
              )}

              {canShare && (
                <button
                  onClick={() => {
                    setIsShareOpen(true);
                    setIsMenuOpen(false);
                  }}
                  className={menuItemClass}
                  data-testid="chat-menu-share"
                >
                  <Users className="size-4 text-muted-foreground" />
                  {isShared ? "Stop sharing…" : "Share with organization…"}
                </button>
              )}

              {workflowExecution && onToggleWorkflowViewer && (
                <button
                  onClick={() => {
                    onToggleWorkflowViewer();
                    setIsMenuOpen(false);
                  }}
                  className={menuItemClass}
                >
                  <Workflow className="size-4 text-muted-foreground" />
                  {isWorkflowViewerOpen ? "Hide" : "Show"} workflow
                </button>
              )}

              <button
                onClick={() => {
                  setChatThreadMode(isSimplified ? "detailed" : "simplified");
                  setIsMenuOpen(false);
                }}
                aria-pressed={isSimplified}
                className={menuItemClass}
              >
                <ListCollapse className="size-4 text-muted-foreground" />
                <span className="flex-1">Simplified view</span>
                {isSimplified && <Check className="size-4 text-primary-ink" aria-hidden />}
              </button>

              {!isViewer && actions.openTerminal && (
                <button onClick={runAndClose(actions.openTerminal)} className={menuItemClass}>
                  <Terminal className="size-4 text-muted-foreground" />
                  Open terminal
                </button>
              )}

              {!isViewer && actions.worktree && (
                <WorkspaceRecoveryMenuItems
                  chatId={chatId}
                  worktree={actions.worktree}
                  mainWorktree={actions.mainWorktree}
                  onSelect={() => setIsMenuOpen(false)}
                />
              )}

              {!isViewer && (
                <>
                  <button
                    onClick={() => {
                      setIsMoveOpen(true);
                      setIsMenuOpen(false);
                    }}
                    className={menuItemClass}
                  >
                    <FolderInput className="size-4 text-muted-foreground" />
                    Move to project…
                  </button>

                  <button onClick={runAndClose(actions.exportChat)} className={menuItemClass}>
                    <Download className="size-4 text-muted-foreground" />
                    Export chat
                  </button>
                </>
              )}

              <button onClick={runAndClose(actions.copyChatId)} className={menuItemClass}>
                <Copy className="size-4 text-muted-foreground" />
                Copy chat ID
              </button>

              {!isViewer && (
                <>
                  <div className="my-1 border-t border-border-subtle" />
                  <button onClick={runAndClose(actions.archive)} className={menuItemClass}>
                    <Archive className="size-4 text-muted-foreground" />
                    Archive chat
                  </button>
                </>
              )}

              {/* When the chat started: an aside, not a header stat. */}
              {timeAgo && !hideTitle && (
                <div className="px-2.5 pb-1 pt-2 text-meta text-muted-foreground">Started {timeAgo} ago</div>
              )}
            </div>
          )}
        </div>
      )}

      {!isNewChat && chatId && !isViewer && (
        <MoveChatToProjectDialog chatId={chatId} open={isMoveOpen} onClose={() => setIsMoveOpen(false)} />
      )}
      {canShare && chatId && (
        <ShareChatDialog chatId={chatId} open={isShareOpen} onClose={() => setIsShareOpen(false)} />
      )}
    </header>
  );
}

/**
 * The runs an agent started from this chat, as one quiet link in the top bar
 * ("2 runs") to the Runs list filtered to them. Nothing for a chat that
 * started none, and nothing where there is no Runs area.
 */
function ChildRunsLink({ chatId }: { chatId: string }) {
  const runsArea = useCapability("runsArea");
  const children = useChildRuns(chatId, runsArea);
  const runs = children.data?.runs ?? [];
  if (!runsArea || runs.length === 0) return null;
  const more = children.data?.hasMore ?? false;
  const count = more ? `${runs.length}+` : String(runs.length);
  const label = `${count} ${runs.length === 1 && !more ? "run" : "runs"}`;
  return (
    <Link
      to={runsPath()}
      search={{ parent: chatId }}
      data-testid="child-runs-link"
      title={`${label} started here`}
      className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-2 text-ui text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Bot className="size-4" aria-hidden="true" />
      {label}
    </Link>
  );
}
