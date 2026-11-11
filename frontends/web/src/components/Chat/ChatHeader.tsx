/**
 * ChatHeader - Chat header with title and stats
 * 
 * Shows:
 * - Chat title (editable)
 * - Menu button
 * - Stats row: file changes, processes, tasks, context usage, time ago
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
} from "lucide-react";
import { formatDistanceToNow } from "date-fns";
import { useChat } from "../../hooks/chat-queries";
import { NoMachinePill } from "./NoMachine";
import { WorkspaceRecoveryMenuItems } from "./WorkspaceRecoveryMenuItems";
import { useMessages } from "../../hooks/message-queries";
import { ContextUsageIndicator } from "./ContextUsageIndicator";
import Tooltip from "../forge-ui/tooltip";
import { useThreads, ThreadTabs } from "./thread-views";
import { ChildRunsStrip } from "../runs/ChildRunsStrip";
import type { WorkflowExecution } from "./ExecutionSidebar/types";
import { useChatActions } from "./useChatActions";
import { MoveChatToProjectDialog } from "./MoveChatToProjectDialog";
import { useThreadNavigation } from "./useThreadNavigation";
import { setChatThreadMode, useChatThreadMode } from "./thread-views/useChatThreadMode";

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
    if (isNewChat) return; // Don't allow editing for new chat
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

  return (
    <div className="flex items-center bg-card/30 flex-shrink-0">
      <div className="px-4 sm:px-6 lg:px-8 py-1 w-full">
        <div className="max-w-[1200px] mx-auto">
          <div className="flex flex-col gap-0.5">
            {/* Row 1: Title, Menu, and Time */}
            <div className="flex items-center gap-1">
              {/* Back to parent thread - only when viewing a spawned/child thread */}
              {showBackButton && (
                <Tooltip
                  content={backTargetThread ? `Back to ${backTargetThread.name}` : "Back to main thread"}
                  side="bottom"
                >
                  <button
                    onClick={() => onSelectThread?.(backTargetId)}
                    className="p-1 hover:bg-accent rounded transition-colors flex-shrink-0"
                    aria-label="Back to parent thread"
                  >
                    <ArrowLeft className="h-4 w-4 text-muted-foreground" />
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
                  className="text-base font-semibold text-foreground leading-tight bg-transparent border-b border-primary focus:outline-none focus:border-primary flex-1 min-w-0"
                />
              ) : (
                <h1 
                  onClick={handleTitleClick}
                  className={`text-base font-semibold text-foreground leading-tight truncate ${
                    isNewChat ? '' : 'cursor-text hover:text-foreground/80'
                  } transition-colors`}
                >
                  {title}
                </h1>
              )}
              
              {/* Menu button - right after title */}
              {!isNewChat && chatId && (
                <div className="relative flex-shrink-0" ref={menuRef}>
<Tooltip content="Chat options" side="bottom" delay={300} triggerClassName="inline-flex">
                  <button 
                    onClick={() => setIsMenuOpen(!isMenuOpen)}
                    className="touch-target inline-flex items-center justify-center p-1 hover:bg-accent rounded transition-colors"
                    aria-label="More options"
                  >
                    <MoreHorizontal className="h-4 w-4 text-muted-foreground" />
                  </button>
</Tooltip>
                  
                  {/* Dropdown menu */}
                  {isMenuOpen && (
                    <div className="absolute left-0 top-full mt-1 w-64 bg-popover border border-border rounded-md shadow-lg py-1 z-50">
                      <button
                        onClick={() => {
                          handleTitleClick();
                          setIsMenuOpen(false);
                        }}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <Edit2 className="h-4 w-4" />
                        Edit Title
                      </button>
                      
                      {workflowExecution && onToggleWorkflowViewer && (
                        <button
                          onClick={() => {
                            onToggleWorkflowViewer();
                            setIsMenuOpen(false);
                          }}
                          className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                        >
                          <Workflow className="h-4 w-4" />
                          {isWorkflowViewerOpen ? "Hide" : "Show"} Workflow
                        </button>
                      )}

                      <button
                        onClick={() => {
                          setChatThreadMode(isSimplified ? "detailed" : "simplified");
                          setIsMenuOpen(false);
                        }}
                        aria-pressed={isSimplified}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <ListCollapse className="h-4 w-4" />
                        <span className="flex-1">Simplified view</span>
                        {isSimplified && <Check className="h-4 w-4 text-primary-ink" aria-hidden />}
                      </button>

                      {actions.openTerminal && (
                        <button
                          onClick={runAndClose(actions.openTerminal)}
                          className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                        >
                          <Terminal className="h-4 w-4" />
                          Open Terminal
                        </button>
                      )}

                      {actions.worktree && (
                        <WorkspaceRecoveryMenuItems
                          chatId={chatId}
                          worktree={actions.worktree}
                          mainWorktree={actions.mainWorktree}
                          onSelect={() => setIsMenuOpen(false)}
                        />
                      )}
                      
                      <button
                        onClick={() => {
                          setIsMoveOpen(true);
                          setIsMenuOpen(false);
                        }}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <FolderInput className="h-4 w-4" />
                        Move to project…
                      </button>

                      <button
                        onClick={runAndClose(actions.exportChat)}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <Download className="h-4 w-4" />
                        Export Chat
                      </button>
                      
                      <button
                        onClick={runAndClose(actions.copyChatId)}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <Copy className="h-4 w-4" />
                        Copy Chat ID
                      </button>
                      
                      <div className="border-t border-border my-1" />
                      
                      <button
                        onClick={runAndClose(actions.archive)}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <Archive className="h-4 w-4" />
                        Archive chat
                      </button>
                    </div>
                  )}
                </div>
              )}
              
              {/* A chat with no machine says so where the user looks first, and
                  the pill is the way to connect one (NO_MACHINE_CHATS.md §2.4). */}
              {!isNewChat && chatId && chat?.noMachine && <NoMachinePill chatId={chatId} />}

              {/* Spacer to push time to the right */}
              <div className="flex-1" />

              {/* Context usage - the one stat that changes what you do next */}
              {!isNewChat && (
                <ContextUsageIndicator
                  threadTokenCount={contextUsage?.threadTokenCount ?? 0}
                  compactionThreshold={contextUsage?.compactionThreshold ?? 200000}
                  compact={true}
                />
              )}

              {/* Time ago - top right */}
              {timeAgo && !hideTitle && (
                <span className="text-xs text-muted-foreground flex-shrink-0">{timeAgo}</span>
              )}
            </div>
            
            {/* Row 2: Thread tabs (only show if multiple threads) */}
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

            {/* Row 3: runs an agent started from this chat (decision 4).
                Renders nothing for a chat that started none. */}
            {!isNewChat && chatId && <ChildRunsStrip chatId={chatId} />}
          </div>
        </div>
      </div>
      {!isNewChat && chatId && (
        <MoveChatToProjectDialog chatId={chatId} open={isMoveOpen} onClose={() => setIsMoveOpen(false)} />
      )}
    </div>
  );
}
