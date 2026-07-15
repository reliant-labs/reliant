/**
 * ChatHeader - Chat header with title and stats
 * 
 * Shows:
 * - Chat title (editable)
 * - Menu button
 * - Stats row: file changes, processes, tasks, context usage, time ago
 */

import { useMemo, useState, useEffect, useRef, useCallback } from "react";
import { useContextUsage, useContextUsageByThread } from "../../store/chatStoreHooks";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useTerminalStore } from "../../store/terminalStore";
import { useTaskStats } from "../../hooks/task-queries";
import { useProcessStore } from "../../store/processStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore, type RightSidebarTab } from "../../store/workspaceStateStore";
import {
  MoreHorizontal,
  Copy,
  Edit2,
  Trash2,
  Download,
  Terminal,
  ListTodo,
  GitBranch,
  Workflow,
  Activity,
  BookMarked,
  ArrowLeft,
} from "lucide-react";
import { formatDistanceToNow } from "date-fns";
import { useChat, useDeleteChat, useRenameChat } from "../../hooks/chat-queries";
import { useMessages } from "../../hooks/message-queries";
import { useWorktreeChanges } from "../../hooks/useWorktreeChanges";
import { ContextUsageIndicator } from "./ContextUsageIndicator";
import { Tooltip } from "../ui/Tooltip";
import { useThreads, ThreadTabs } from "./thread-views";
import type { WorkflowExecution } from "./ExecutionSidebar/types";
import { isDev } from "../../lib/constants";
import { useLoadedSkills } from "../../hooks/useLoadedSkills";
import { openExternalLink } from "../../lib/open-link";
import { BackgroundProcessStatus } from "../../gen/reliant/v1/common_pb";
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
  /** Callback to toggle inline workflow viewer (original position - above chat) */
  onToggleInlineWorkflowViewer?: () => void;
  /** Whether inline workflow viewer is currently shown */
  isInlineWorkflowViewerOpen?: boolean;
  /** Callback to toggle side panel workflow viewer */
  onToggleSidePanelWorkflowViewer?: () => void;
  /** Whether side panel workflow viewer is currently shown */
  isSidePanelWorkflowViewerOpen?: boolean;
  /** Current workflow viewer mode - used to determine which handler to call when opening */
  workflowViewerMode?: 'inline' | 'side';
  /** Callback to toggle workflow viewer mode (uses setting) */
  onToggleWorkflowViewerMode?: () => void;
}

export function ChatHeader({ 
  chatId, 
  isNewChat = false, 
  worktreeId: propWorktreeId,
  selectedThreadId,
  onSelectThread,
  workflowExecution,
  onToggleInlineWorkflowViewer,
  isInlineWorkflowViewerOpen,
  onToggleSidePanelWorkflowViewer,
  isSidePanelWorkflowViewerOpen,
  workflowViewerMode: _workflowViewerMode,
  onToggleWorkflowViewerMode,
}: ChatHeaderProps) {
  const chatQuery = useChat(chatId || undefined);
  const chat = chatQuery.data;
  const messagesQuery = useMessages(chatId || undefined);
  const messages = messagesQuery.data?.messages ?? [];
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const currentProject = useProjectStore((state) => state.currentProject);
  const deleteMutation = useDeleteChat();
  const renameMutation = useRenameChat();
  const createTerminalSession = useTerminalStore((state) => state.createSession);
  const showTerminal = useTerminalStore((state) => state.showTerminal);
  const setActiveSession = useTerminalStore((state) => state.setActiveSession);
  const getWorktreeSessions = useTerminalStore((state) => state.getWorktreeSessions);
  
  // Right sidebar tab control
  const setRightSidebarTab = useWorkspaceStateStore((state) => state.setRightSidebarTab);
  const setRightPanelState = useWorkspaceStateStore((state) => state.setRightPanelState);
  
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
  
  // Task stats from React Query
  const taskStats = useTaskStats(chatId);
  
  // Process stats - get running processes for the worktree
  const effectiveWorktreeId = propWorktreeId || chat?.worktreeId;
  const processes = useProcessStore((state) => state.processes);
  const runningProcessCount = useMemo(() => {
    if (!effectiveWorktreeId) return 0;
    return processes.filter(
      (p) =>
        p.status === BackgroundProcessStatus.RUNNING &&
        p.worktree_id === effectiveWorktreeId,
    ).length;
  }, [processes, effectiveWorktreeId]);
  
  // File change stats
  const fileStats = useWorktreeChanges(effectiveWorktreeId);
  
  // Context usage for compaction indicator
  const contextUsage = useContextUsage(chatId || "");
  const contextUsageByThread = useContextUsageByThread(chatId || "");
  
  // Handler to open right sidebar tab
  const openSidebarTab = useCallback((tab: RightSidebarTab) => {
    if (currentProject?.id) {
      setRightSidebarTab(currentProject.id, currentWorktree?.id ?? null, tab);
      // Also open the right sidebar if it's closed
      setRightPanelState(currentProject.id, currentWorktree?.id ?? null, { fileBrowser: true });
      // Dispatch event for ModernApp to update its local state (avoids store subscription issues)
      window.dispatchEvent(new CustomEvent("open-file-browser"));
    }
  }, [currentProject?.id, currentWorktree?.id, setRightSidebarTab, setRightPanelState]);

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

  // Dev-only Temporal deep link for this chat's active workflow run
  const hasTemporalRunLink = Boolean(chat?.workflowId && chat?.runId);
  const temporalRunHistoryUrl = useMemo(() => {
    if (!hasTemporalRunLink) return null;
    const temporalUIPort = window.RELIANT_CONFIG?.temporalUIPort || 8233;
    return `http://localhost:${temporalUIPort}/namespaces/reliant/workflows/${chat?.workflowId}/${chat?.runId}/history`;
  }, [hasTemporalRunLink, chat?.workflowId, chat?.runId]);

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

  // Load processes for this worktree on mount/worktree change
  const lastFetchedWorktreeRef = useRef<string | null>(null);
  useEffect(() => {
    // Only fetch if we have a worktree and it's different from what we last fetched
    if (effectiveWorktreeId && effectiveWorktreeId !== lastFetchedWorktreeRef.current) {
      lastFetchedWorktreeRef.current = effectiveWorktreeId;
      useProcessStore.getState().fetchProcesses(effectiveWorktreeId);
    }
  }, [effectiveWorktreeId]);

  return (
    <div className="flex items-center bg-card/30 flex-shrink-0">
      <div className="px-4 sm:px-6 lg:px-8 py-2 w-full">
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
              
              {/* Time ago - top right */}
              {timeAgo && (
                <span className="text-sm text-muted-foreground flex-shrink-0">{timeAgo}</span>
              )}
            </div>
            
            {/* Row 2: Stats row with graph view button */}
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              {/* File changes with additions/deletions - clickable */}
              <Tooltip 
                content={fileStats.totalFiles > 0 
                  ? `${fileStats.totalFiles} file${fileStats.totalFiles !== 1 ? 's' : ''} changed: +${fileStats.additions} -${fileStats.deletions}` 
                  : 'No file changes'
                } 
                placement="bottom"
              >
                <button 
                  onClick={() => openSidebarTab('changes')}
                  className="inline-flex items-center gap-1.5 px-2 py-0.5 h-6 border border-border rounded hover:bg-accent hover:text-foreground transition-colors text-xs"
                >
                  <GitBranch className="h-3.5 w-3.5" />
                  <span className="tabular-nums">{fileStats.totalFiles}</span>
                  <span className="text-green-500 tabular-nums">+{fileStats.additions}</span>
                  <span className="text-red-500 tabular-nums">-{fileStats.deletions}</span>
                </button>
              </Tooltip>
              
              {/* Running processes - clickable */}
              <Tooltip content={runningProcessCount > 0 ? `${runningProcessCount} process${runningProcessCount !== 1 ? 'es' : ''} running` : 'No processes running'} placement="bottom">
                <button 
                  onClick={() => openSidebarTab('processes')}
                  className="inline-flex items-center gap-1.5 px-2 py-0.5 h-6 border border-border rounded hover:bg-accent hover:text-foreground transition-colors text-xs"
                >
                  <Terminal className={`h-3.5 w-3.5 ${runningProcessCount > 0 ? 'text-green-500' : ''}`} />
                  <span className="tabular-nums">{runningProcessCount}</span>
                </button>
              </Tooltip>
              
              {/* Task stats - clickable */}
              <Tooltip content={taskStats.total > 0 ? `${taskStats.completed} of ${taskStats.total} tasks completed` : 'No tasks'} placement="bottom">
                <button 
                  onClick={() => openSidebarTab('tasks')}
                  className="inline-flex items-center gap-1.5 px-2 py-0.5 h-6 border border-border rounded hover:bg-accent hover:text-foreground transition-colors text-xs"
                >
                  <ListTodo className="h-3.5 w-3.5" />
                  <span className="tabular-nums">{taskStats.completed}/{taskStats.total}</span>
                </button>
              </Tooltip>
              
              {/* Loaded skills indicator */}
              <LoadedSkillsBadge chatId={chatId} />

              {/* Workflow status indicator - clickable button */}
              {workflowExecution && (
                <Tooltip 
                  content={`Workflow: ${workflowExecution.workflowName} (${workflowExecution.status})`}
                  placement="bottom"
                >
                  <button 
                    onClick={() => {
                      // Open or toggle the workflow viewer
                      // If viewer is open, close it using the appropriate handler
                      // If viewer is closed, use the toggle handler which respects the setting
                      if (isSidePanelWorkflowViewerOpen || isInlineWorkflowViewerOpen) {
                        // Viewer is open - close it using the appropriate handler
                        if (isSidePanelWorkflowViewerOpen) {
                          onToggleSidePanelWorkflowViewer?.();
                        } else {
                          onToggleInlineWorkflowViewer?.();
                        }
                      } else {
                        // Viewer is closed - use the toggle handler which respects the setting
                        onToggleWorkflowViewerMode?.();
                      }
                    }}
                    className={`inline-flex items-center gap-1.5 px-2 py-0.5 h-6 border rounded hover:bg-accent hover:text-foreground transition-colors text-xs ${
                      (isSidePanelWorkflowViewerOpen || isInlineWorkflowViewerOpen)
                        ? 'border-primary bg-primary/10 text-primary' 
                        : 'border-border text-muted-foreground'
                    }`}
                  >
                    <Workflow className={`h-3.5 w-3.5 ${
                      workflowExecution.status === 'running' ? 'text-blue-500 animate-pulse' : ''
                    }`} />
                    <span>{workflowExecution.workflowName}</span>
                  </button>
                </Tooltip>
              )}
              
              {/* Spacer to push right-side items */}
              <div className="flex-1" />
              
              {/* Dev-only: open this chat's current Temporal workflow run */}
              {!isNewChat && isDev && (
                <Tooltip
                  content={
                    hasTemporalRunLink
                      ? 'Open current chat workflow run in Temporal (dev only)'
                      : 'No workflow run available for this chat yet (dev only)'
                  }
                  placement="bottom"
                >
                  <button
                    onClick={() => {
                      if (!temporalRunHistoryUrl) return;
                      void openExternalLink(temporalRunHistoryUrl);
                    }}
                    disabled={!hasTemporalRunLink}
                    className="inline-flex items-center gap-1.5 px-2 py-0.5 h-6 border border-border rounded hover:bg-accent hover:text-foreground transition-colors text-xs disabled:opacity-50 disabled:cursor-not-allowed"
                    aria-label="Open current chat Temporal workflow run"
                  >
                    <Activity className="h-3.5 w-3.5" />
                  </button>
                </Tooltip>
              )}

              {/* Context usage indicator */}
              {!isNewChat && (
                <ContextUsageIndicator
                  threadTokenCount={contextUsage?.threadTokenCount ?? 0}
                  compactionThreshold={contextUsage?.compactionThreshold ?? 200000}
                  compact={true}
                />
              )}
            </div>
            
            {/* Row 3: Thread tabs (only show if multiple threads) */}
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

/** Small badge showing loaded skills count with tooltip */
function LoadedSkillsBadge({ chatId }: { chatId: string | null }) {
  const skills = useLoadedSkills(chatId || undefined);
  if (skills.length === 0) return null;

  return (
    <Tooltip
      content={`Skills: ${skills.join(', ')}`}
      placement="bottom"
    >
      <div className="inline-flex items-center gap-1.5 px-2 py-0.5 h-6 border border-border rounded hover:bg-accent hover:text-foreground transition-colors text-xs">
        <BookMarked className="h-3.5 w-3.5 text-primary" />
        <span className="tabular-nums">{skills.length}</span>
      </div>
    </Tooltip>
  );
}