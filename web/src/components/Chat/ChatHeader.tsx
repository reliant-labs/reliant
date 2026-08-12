/**
 * ChatHeader - Chat header with title and stats
 * 
 * Shows:
 * - Chat title (editable)
 * - Menu button
 * - Stats row: file changes, processes, tasks, context usage, time ago
 */

import { useMemo, useState, useEffect, useRef } from "react";
import { useContextUsage, useContextUsageByThread } from "../../store/chatStoreHooks";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useTerminalStore } from "../../store/terminalStore";
import {
  MoreHorizontal,
  Copy,
  Edit2,
  Trash2,
  Download,
  Terminal,
  Workflow,
  ArrowLeft,
} from "lucide-react";
import { formatDistanceToNow } from "date-fns";
import { useChat, useDeleteChat, useRenameChat } from "../../hooks/chat-queries";
import { useMessages } from "../../hooks/message-queries";
import { ContextUsageIndicator } from "./ContextUsageIndicator";
import { Tooltip } from "../ui/Tooltip";
import { useThreads, ThreadTabs } from "./thread-views";
import type { WorkflowExecution } from "./ExecutionSidebar/types";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";

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
  /** Workflow execution for thread metadata */
  workflowExecution?: WorkflowExecution;
  /**
   * Opens/closes the workflow viewer. The always-visible workflow chip was
   * removed from the header for compactness; this moved into the overflow menu
   * so the viewer stays reachable.
   */
  onToggleWorkflowViewer?: () => void;
  /** Whether the workflow viewer is currently open (labels the menu item) */
  isWorkflowViewerOpen?: boolean;
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
}: ChatHeaderProps) {
  const chatQuery = useChat(chatId || undefined);
  const chat = chatQuery.data;
  const messagesQuery = useMessages(chatId || undefined);
  const messages = messagesQuery.data?.messages ?? [];
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const deleteMutation = useDeleteChat();
  const renameMutation = useRenameChat();
  const createTerminalSession = useTerminalStore((state) => state.createSession);
  const showTerminal = useTerminalStore((state) => state.showTerminal);
  const setActiveSession = useTerminalStore((state) => state.setActiveSession);
  const getWorktreeSessions = useTerminalStore((state) => state.getWorktreeSessions);

  // Get threads from messages with workflow metadata for better names.
  // Spawn-tool threads are short-lived sub-agents owned by a single tool call;
  // they're surfaced inline via the spawn preview, not in the header.
  const allThreads = useThreads(messages, chatId || "", workflowExecution);
  const threads = useMemo(() => allThreads.filter((t) => !t.isSpawn), [allThreads]);
  const hasMultipleThreads = threads.length > 1;

  // Currently viewed thread (may be a spawn thread, which is excluded from tabs)
  const selectedThread = useMemo(
    () => (selectedThreadId ? allThreads.find((t) => t.id === selectedThreadId) : undefined),
    [allThreads, selectedThreadId],
  );
  // Back target when viewing a spawned/child thread: the parent thread if it's a
  // known non-main thread, otherwise the default main chat view (null)
  const backTargetThread = useMemo(() => {
    if (!selectedThread?.parentThread) return undefined;
    return allThreads.find((t) => t.id === selectedThread.parentThread && !t.isMain);
  }, [allThreads, selectedThread]);
  const showBackButton = Boolean(selectedThread && !selectedThread.isMain && onSelectThread);

  const effectiveWorktreeId = propWorktreeId || chat?.worktreeId;

  // Context usage for compaction indicator
  const contextUsage = useContextUsage(chatId || "");
  const contextUsageByThread = useContextUsageByThread(chatId || "");

  const [isMenuOpen, setIsMenuOpen] = useState(false);
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editedTitle, setEditedTitle] = useState("");
  const menuRef = useRef<HTMLDivElement>(null);
  const titleInputRef = useRef<HTMLInputElement>(null);

  // Determine the title to show
  const title = isNewChat ? "New Chat" : (chat?.title || "New Chat");

  // Get the workspace and path
  const { workspaceName, workspacePath, worktree } = useMemo(() => {
    const wtId = propWorktreeId || chat?.worktreeId;
    if (!wtId) return { workspaceName: null, workspacePath: null, worktree: null };
    const worktree = worktrees.find((w) => w.id === wtId);
    if (!worktree) return { workspaceName: null, workspacePath: null, worktree: null };
    
    // Use branch name if available, otherwise use name
    return {
      workspaceName: worktree.branch || worktree.name || null,
      workspacePath: worktree.path || null,
      worktree,
    };
  }, [chat?.worktreeId, propWorktreeId, worktrees]);

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
    if (!chatId || !editedTitle.trim() || editedTitle === chat?.title) {
      setIsEditingTitle(false);
      setEditedTitle("");
      return;
    }

    try {
      await renameMutation.mutateAsync({ chatId, title: editedTitle.trim() });
      setIsEditingTitle(false);
      setEditedTitle("");
    } catch (err) {
      console.error('Failed to rename chat:', err);
    }
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

  const handleDeleteChat = async () => {
    if (!chatId || !confirm('Are you sure you want to delete this chat?')) return;
    setIsMenuOpen(false);
    try {
      await deleteMutation.mutateAsync(chatId);
    } catch (err) {
      console.error('Failed to delete chat:', err);
    }
  };

  const handleExportChat = () => {
    setIsMenuOpen(false);
    
    if (!chatId || !chat) {
      console.error('No chat to export');
      return;
    }

    try {
      // Build markdown export
      let markdown = `# ${chat.title || 'Chat Export'}\n\n`;
      markdown += `Exported: ${new Date().toLocaleString()}\n`;
      if (workspaceName) {
        markdown += `Workspace: ${workspaceName}\n`;
      }
      markdown += `\n---\n\n`;

      // Add messages
      messages.forEach((message) => {
        const role = message.role === MessageRole.USER ? 'User' : 'Assistant';
        const timestamp = new Date(message.createdAt || '').toLocaleString();
        markdown += `## ${role} - ${timestamp}\n\n`;
        
        // Iterate typed contentBlocks directly
        const blocks = message.contentBlocks || [];
        blocks.forEach((block) => {
          if (block.type === ContentBlockType.TEXT && block.content) {
            markdown += `${block.content}\n\n`;
          } else if (block.type === ContentBlockType.TOOL_CALL) {
            markdown += `**Tool Call:** ${block.toolName}\n\n`;
            if (block.input) {
              try {
                const parsed = JSON.parse(block.input);
                markdown += `\`\`\`json\n${JSON.stringify(parsed, null, 2)}\n\`\`\`\n\n`;
              } catch {
                markdown += `\`\`\`\n${block.input}\n\`\`\`\n\n`;
              }
            }
          } else if (block.type === ContentBlockType.TOOL_RESULT) {
            markdown += `**Tool Result:** ${block.toolName || 'Unknown'}\n\n`;
            if (block.content) {
              markdown += `${block.content}\n\n`;
            }
          }
        });
        
        markdown += `---\n\n`;
      });

      // Create and download file
      const blob = new Blob([markdown], { type: 'text/markdown' });
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      const filename = `${chat.title?.replace(/[^a-z0-9]/gi, '_') || 'chat'}_${new Date().toISOString().split('T')[0]}.md`;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      URL.revokeObjectURL(url);
    } catch (err) {
      console.error('Failed to export chat:', err);
    }
  };

  const handleOpenTerminal = () => {
    setIsMenuOpen(false);
    
    if (!workspacePath || !worktree) {
      console.error('No workspace path available');
      return;
    }

    try {
      // Check if there are existing sessions for this worktree
      const existingSessions = getWorktreeSessions(effectiveWorktreeId);
      
      if (existingSessions.length > 0) {
        // Activate the first existing session
        setActiveSession(existingSessions[0].id);
      } else {
        // Create a new terminal session in the worktree directory
        createTerminalSession(
          workspacePath,
          worktree.project_id, // Include project_id from worktree
          effectiveWorktreeId // worktreeId
        );
      }
      
      // Show the terminal panel
      showTerminal();
    } catch (err) {
      console.error('Failed to open terminal:', err);
    }
  };

  const handleCopyChatId = async () => {
    if (!chatId) return;
    try {
      await navigator.clipboard.writeText(chatId);
      setIsMenuOpen(false);
    } catch (err) {
      console.error('Failed to copy chat ID:', err);
    }
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
                  placement="bottom"
                >
                  <button
                    onClick={() => onSelectThread?.(backTargetThread ? backTargetThread.id : null)}
                    className="p-1 hover:bg-accent rounded transition-colors flex-shrink-0"
                    aria-label="Back to parent thread"
                  >
                    <ArrowLeft className="h-4 w-4 text-muted-foreground" />
                  </button>
                </Tooltip>
              )}

              {/* Title - click to edit inline */}
              {isEditingTitle ? (
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
                  <button 
                    onClick={() => setIsMenuOpen(!isMenuOpen)}
                    className="p-1 hover:bg-accent rounded transition-colors"
                    aria-label="More options"
                  >
                    <MoreHorizontal className="h-4 w-4 text-muted-foreground" />
                  </button>
                  
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

                      {workspacePath && (
                        <button
                          onClick={handleOpenTerminal}
                          className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                        >
                          <Terminal className="h-4 w-4" />
                          Open Terminal
                        </button>
                      )}
                      
                      <button
                        onClick={handleExportChat}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <Download className="h-4 w-4" />
                        Export Chat
                      </button>
                      
                      <button
                        onClick={handleCopyChatId}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2"
                      >
                        <Copy className="h-4 w-4" />
                        Copy Chat ID
                      </button>
                      
                      <div className="border-t border-border my-1" />
                      
                      <button
                        onClick={handleDeleteChat}
                        className="w-full px-3 py-2 text-left text-sm hover:bg-accent flex items-center gap-2 text-destructive"
                      >
                        <Trash2 className="h-4 w-4" />
                        Delete Chat
                      </button>
                    </div>
                  )}
                </div>
              )}
              
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
              {timeAgo && (
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

          </div>
        </div>
      </div>
    </div>
  );
}
