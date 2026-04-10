/**
 * ChatPresenter - Pure rendering component
 *
 * Receives all data as props and renders the chat interface.
 * No store subscriptions, no business logic - just pure rendering.
 */

import { useRef, useState, memo, useMemo, useEffect, useCallback } from "react";
import type { VirtuosoHandle } from "react-virtuoso";
import { GripHorizontal, GripVertical } from "lucide-react";
import { ChatInputWrapper } from "./ChatInputWrapper";
import { ChatThinkingIndicator } from "./ChatThinkingIndicator";
import { ChatMessagesContainer } from "./ChatMessagesContainer";
import { PermissionsPanelWrapper } from "./PermissionsPanelWrapper";
import { PermissionsPanel } from "./PermissionsPanel";
import { ChatHeader } from "./ChatHeader";
import type { WorkflowExecution } from "./ExecutionSidebar/types";
import { InterleavedTimeline } from "./thread-views";
import { WorkflowViewerPanel } from "../workflow/WorkflowViewerPanel";

import type { Message, ToolApprovalRequest } from "../../api/client";
import type {
  ErrorUpdate,
  InfoUpdate,
  RunOutputUpdate,
  ConnectionStatus,
} from "../../types/streaming";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { MessageRole } from "../../gen/reliant/v1/chat_pb";

interface ChatPresenterProps {
  // Message data
  messages: Message[];
  approvals: ToolApprovalRequest[];
  errorEvents: ErrorUpdate[];
  infoEvents: InfoUpdate[];
  runOutputs: RunOutputUpdate[];

  // Chat state
  chatId: string | null;
  isChatBusy: boolean;
  pendingApprovals: ToolApprovalRequest[];
  hasPendingYield?: boolean;
  connectionStatus: string;
  currentActivity?: string;

  // Chat metadata
  worktreeId?: string | null;
  projectId?: string | null;
  needsRecovery?: boolean; // True when Temporal workflow was lost and user needs to send message to continue
  
  // Recovery callback - called when user confirms they want to restart the conversation
  onRestartConversation?: () => void;

  // Callbacks
  onSendMessage: (
    content: string,
    attachmentIds?: string[],
    workflow?: string | null,
    workflowParams?: Record<string, unknown>,
    targetThread?: string | null,
    selectedPresets?: Record<string, string | null>
  ) => Promise<void>;
  onStopStreaming: () => Promise<void>;

  // UI state
  isFocused?: boolean;
  paneId?: string;
  isRecentChangesOpen?: boolean;
  onToggleRecentChanges?: () => void;

  // Workflow execution sidebar
  workflowExecution?: WorkflowExecution;
}

export const ChatPresenter = memo(function ChatPresenter({
  messages,
  approvals,
  errorEvents,
  infoEvents,
  runOutputs,
  chatId,
  isChatBusy,
  pendingApprovals,
  hasPendingYield = false,
  connectionStatus,
  currentActivity,
  worktreeId,
  projectId,
  needsRecovery,
  onRestartConversation,
  onSendMessage,
  onStopStreaming,
  isFocused = true,
  paneId,
  isRecentChangesOpen,
  onToggleRecentChanges,
  workflowExecution,
}: ChatPresenterProps) {
  const chatInputRef = useRef<HTMLDivElement>(null);

  // Thread selection state (lifted up to share between header and timeline)
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null);

  // Workspace state store for persisting workflow viewer state
  const getWorkflowViewerOpen = useWorkspaceStateStore((state) => state.getWorkflowViewerOpen);
  const setWorkflowViewerOpen = useWorkspaceStateStore((state) => state.setWorkflowViewerOpen);
  const getWorkflowViewerMode = useWorkspaceStateStore((state) => state.getWorkflowViewerMode);
  const setWorkflowViewerModeStore = useWorkspaceStateStore((state) => state.setWorkflowViewerMode);

  // Workflow viewer mode: 'inline' (above chat) or 'side' (beside chat)
  // Initialize from settings immediately
  const [workflowViewerMode, setWorkflowViewerMode] = useState<'inline' | 'side'>(() => {
    const stored = settingsSync.getSetting(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, 'side');
    return (stored === 'inline' || stored === 'side') ? stored : 'side';
  });
  const workflowViewerModeRef = useRef(workflowViewerMode);
  
  // Workflow viewer open state - load from persisted state
  const [isWorkflowViewerOpen, setIsWorkflowViewerOpen] = useState(false);
  
  const [sidePanelWidth, setSidePanelWidth] = useState(400);
  const [inlineViewerHeight, setInlineViewerHeight] = useState(400); // Height for inline viewer
  const [isWorkflowViewerExpanded, setIsWorkflowViewerExpanded] = useState(false);
  const isResizingRef = useRef(false); // Track if a resize operation is in progress

  // Helper to update both local and persisted state
  const updateWorkflowViewerOpen = useCallback((open: boolean) => {
    setIsWorkflowViewerOpen(open);
    if (chatId && projectId) {
      setWorkflowViewerOpen(projectId, worktreeId ?? null, chatId, open);
    }
  }, [chatId, projectId, worktreeId, setWorkflowViewerOpen]);

  const updateWorkflowViewerMode = useCallback((mode: 'inline' | 'side') => {
    setWorkflowViewerMode(mode);
    workflowViewerModeRef.current = mode;
    // Save chat-specific mode if we have both chatId and projectId
    if (chatId && projectId) {
      setWorkflowViewerModeStore(projectId, worktreeId ?? null, chatId, mode);
    }
  }, [chatId, projectId, worktreeId, setWorkflowViewerModeStore]);

  // Load persisted workflow viewer state when chatId changes
  useEffect(() => {
    if (!chatId || !projectId) return;

    // Load persisted state
    const persistedOpen = getWorkflowViewerOpen(projectId, worktreeId ?? null, chatId);
    const persistedMode = getWorkflowViewerMode(projectId, worktreeId ?? null, chatId);

    // If we have persisted mode for this chat, use it (user has set a preference for this specific chat)
    if (persistedMode !== null) {
      setWorkflowViewerMode(persistedMode);
      workflowViewerModeRef.current = persistedMode;
    } else {
      // NEW chat - no persisted mode, use the setting
      const stored = settingsSync.getSetting(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, 'side');
      const defaultMode = (stored === 'inline' || stored === 'side') ? stored : 'side';
      setWorkflowViewerMode(defaultMode);
      workflowViewerModeRef.current = defaultMode;
      // Don't persist it - only persist when user explicitly toggles
    }

    // Set open state from persisted state, or default based on workflow execution
    if (workflowExecution !== undefined) {
      // If workflow exists, use persisted state or default to true
      updateWorkflowViewerOpen(persistedOpen !== false);
    } else {
      // No workflow, use persisted state or default to false
      updateWorkflowViewerOpen(persistedOpen);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatId, projectId, worktreeId]); // Only depend on chatId, projectId, worktreeId

  // When workflowExecution appears, restore persisted mode if it exists
  // This handles the case where a workflow starts after the chat is already loaded
  useEffect(() => {
    if (!chatId || !projectId || !workflowExecution) return;
    
    const persistedMode = getWorkflowViewerMode(projectId, worktreeId ?? null, chatId);
    
    // If we have persisted mode, use it (user has set a preference for this specific chat)
    if (persistedMode !== null) {
      setWorkflowViewerMode(persistedMode);
      workflowViewerModeRef.current = persistedMode;
    } else {
      // No persisted mode, use setting (for new chats)
      const stored = settingsSync.getSetting(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, 'side');
      const defaultMode = (stored === 'inline' || stored === 'side') ? stored : 'side';
      setWorkflowViewerMode(defaultMode);
      workflowViewerModeRef.current = defaultMode;
      // Don't persist it - only persist when user explicitly toggles
    }
    
    // Open state is already handled in the chatId effect above
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workflowExecution?.id]); // Only when workflow execution ID changes


  // Keep ref in sync with state (already handled in updateWorkflowViewerMode)

  // Determine if viewers should be open based on mode and open state
  const isInlineWorkflowViewerOpen = workflowViewerMode === 'inline' && isWorkflowViewerOpen && workflowExecution !== undefined;
  const isSidePanelWorkflowViewerOpen = workflowViewerMode === 'side' && isWorkflowViewerOpen && workflowExecution !== undefined;


  const handleToggleWorkflowViewerMode = useCallback(() => {
    // If currently closed, open it using the current mode (which should already be set from persisted or setting)
    if (!isWorkflowViewerOpen) {
      updateWorkflowViewerOpen(true);
    } else {
      // Toggle between inline and side - this is an explicit user action, so persist it
      const newMode = workflowViewerMode === 'inline' ? 'side' : 'inline';
      updateWorkflowViewerMode(newMode);
    }
  }, [isWorkflowViewerOpen, workflowViewerMode, updateWorkflowViewerOpen, updateWorkflowViewerMode]);

  // Legacy handlers for compatibility
  const handleToggleInlineWorkflowViewer = useCallback(() => {
    // If viewer is closed, open it in inline mode
    if (!isWorkflowViewerOpen) {
      updateWorkflowViewerOpen(true);
      updateWorkflowViewerMode('inline');
    } else if (workflowViewerMode === 'inline') {
      // If already in inline mode, close it
      updateWorkflowViewerOpen(false);
    } else {
      // If in side mode, switch to inline
      updateWorkflowViewerMode('inline');
    }
  }, [workflowViewerMode, isWorkflowViewerOpen, updateWorkflowViewerOpen, updateWorkflowViewerMode]);

  const handleToggleSidePanelWorkflowViewer = useCallback(() => {
    // If viewer is closed, open it using the current mode (which should be set from setting)
    if (!isWorkflowViewerOpen) {
      updateWorkflowViewerOpen(true);
      // Don't change mode - use whatever is already set (from setting or persisted)
    } else if (workflowViewerMode === 'side') {
      // If already in side mode, close it
      updateWorkflowViewerOpen(false);
    } else {
      // If in inline mode, switch to side
      updateWorkflowViewerMode('side');
    }
  }, [workflowViewerMode, isWorkflowViewerOpen, updateWorkflowViewerOpen, updateWorkflowViewerMode]);


  // Listen for settings changes (only update if no persisted mode exists - i.e., new chats)
  // This ensures new chats use the updated setting, but existing chats keep their preference
  useEffect(() => {
    const handleSettingsChange = () => {
      if (!chatId || !projectId) return;
      
      // Only update if we don't have persisted state for this chat (new chat)
      // Existing chats with persisted modes should keep their preference
      const persistedMode = getWorkflowViewerMode(projectId, worktreeId ?? null, chatId);
      if (persistedMode === null) {
        const stored = settingsSync.getSetting(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, 'side');
        const newDefault = (stored === 'inline' || stored === 'side') ? stored : 'side';
        // Update the mode without persisting (since user hasn't explicitly set it for this chat)
        setWorkflowViewerMode(newDefault);
        workflowViewerModeRef.current = newDefault;
      }
    };
    
    window.addEventListener('appearance-updated', handleSettingsChange);
    return () => {
      window.removeEventListener('appearance-updated', handleSettingsChange);
    };
  }, [chatId, projectId, worktreeId, getWorkflowViewerMode]);

  // Reset thread selection when navigating to a different chat
  // Workflow viewer state is now loaded from persisted state in the effect above
  useEffect(() => {
    setSelectedThreadId(null);
  }, [chatId]);

  // Convert single thread ID to Set for timeline filtering
  const selectedThreads = useMemo(() => {
    if (selectedThreadId === null) return null; // null = show all
    return new Set([selectedThreadId]);
  }, [selectedThreadId]);

  // Wrapper to inject selectedThreadId into onSendMessage
  const handleSendWithThread = useCallback(
    async (
      content: string,
      attachmentIds?: string[],
      workflow?: string | null,
      workflowParams?: Record<string, unknown>,
      _targetThread?: string | null, // ignored - we use selectedThreadId instead
      selectedPresets?: Record<string, string | null>
    ) => {
      await onSendMessage(content, attachmentIds, workflow, workflowParams, selectedThreadId, selectedPresets);
    },
    [onSendMessage, selectedThreadId]
  );

  // Virtuoso scroll state bridge
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  const [virtuosoAtBottom, setVirtuosoAtBottom] = useState(true);
  // Keep a ref in sync for use in effects that need the latest value without re-triggering
  const virtuosoAtBottomRef = useRef(true);

  const handleAtBottomStateChange = useCallback((atBottom: boolean) => {
    virtuosoAtBottomRef.current = atBottom;
    setVirtuosoAtBottom(atBottom);
  }, []);

  // Scroll state from ChatMessagesContainer
  const [scrollState, setScrollState] = useState<{
    isAtBottom: boolean;
    hasScrolledUp: boolean;
    scrollToBottom: () => Promise<void>;
    resumeAutoScroll: () => void;
  } | null>(null);

  // Thinking indicator element, passed as Virtuoso footer
  const hasThinkingFooter = isChatBusy && pendingApprovals.length === 0 && !hasPendingYield;
  const thinkingFooter = hasThinkingFooter ? (
    <ChatThinkingIndicator
      chatId={chatId || undefined}
      filterThreadId={selectedThreadId}
    />
  ) : undefined;

  // When thinking indicator appears, scroll to bottom so it's visible
  const prevHasThinkingFooterRef = useRef(false);
  useEffect(() => {
    if (hasThinkingFooter && !prevHasThinkingFooterRef.current && virtuosoAtBottomRef.current) {
      // Footer just appeared and user was at the bottom — scroll to show it
      requestAnimationFrame(() => {
        virtuosoRef.current?.scrollToIndex({ index: "LAST", align: "end", behavior: "auto" });
      });
    }
    prevHasThinkingFooterRef.current = hasThinkingFooter;
  }, [hasThinkingFooter]);

  // Render the timeline
  const renderTimeline = () => {
    const filteredMessages = messages.filter(
      (message: Message) => message.role !== MessageRole.TOOL
    );

    return (
      <InterleavedTimeline
        messages={filteredMessages}
        approvals={approvals}
        errorEvents={errorEvents}
        infoEvents={infoEvents}
        runOutputs={runOutputs}
        chatId={chatId || ""}
        workflowExecution={workflowExecution}
        selectedThreads={selectedThreads}
        isStreaming={isChatBusy}
        virtuosoRef={virtuosoRef}
        onAtBottomStateChange={handleAtBottomStateChange}
        footer={thinkingFooter}
      />
    );
  };

  return (
    <div className="flex h-full layout-stable relative flex-1 min-w-0 min-h-0">
      {/* Main chat area */}
      <div
        className={isWorkflowViewerExpanded && workflowViewerMode === 'side' ? "hidden" : "flex flex-col flex-1 min-w-0 min-h-0"}
        data-testid="chat-interface"
      >
        {/* Chat Header */}
        <ChatHeader
          chatId={chatId}
          selectedThreadId={selectedThreadId}
          onSelectThread={setSelectedThreadId}
          workflowExecution={workflowExecution}
          onToggleInlineWorkflowViewer={handleToggleInlineWorkflowViewer}
          isInlineWorkflowViewerOpen={isInlineWorkflowViewerOpen}
          onToggleSidePanelWorkflowViewer={handleToggleSidePanelWorkflowViewer}
          isSidePanelWorkflowViewerOpen={isSidePanelWorkflowViewerOpen}
          workflowViewerMode={workflowViewerMode}
          onToggleWorkflowViewerMode={handleToggleWorkflowViewerMode}
        />

        {/* Inline Workflow Viewer Panel - shown when in inline mode */}
        {isInlineWorkflowViewerOpen && workflowExecution && projectId && (
          <div 
            className={isWorkflowViewerExpanded ? "flex-1 border-b border-border bg-background flex flex-col relative min-h-0" : "flex-shrink-0 border-b border-border bg-background flex flex-col relative"}
            style={isWorkflowViewerExpanded ? undefined : { height: inlineViewerHeight }}
          >
            {/* Resize handle at the bottom - hidden when expanded */}
            {!isWorkflowViewerExpanded && (
            <div
              className="absolute bottom-0 left-0 right-0 h-2 cursor-row-resize hover:bg-primary/20 transition-colors z-10 flex items-center justify-center group"
              onMouseDown={(e) => {
                e.preventDefault();
                e.stopPropagation();
                // Prevent multiple simultaneous resize operations
                if (isResizingRef.current) {
                  return;
                }
                // Only allow resize if we're actually in inline mode
                if (workflowViewerModeRef.current !== 'inline') {
                  return;
                }
                isResizingRef.current = true;
                const startY = e.clientY;
                const startHeight = inlineViewerHeight;
                const onMouseMove = (e: MouseEvent) => {
                  // Double-check mode hasn't changed during resize
                  if (workflowViewerModeRef.current !== 'inline') {
                    isResizingRef.current = false;
                    document.removeEventListener('mousemove', onMouseMove);
                    document.removeEventListener('mouseup', onMouseUp);
                    return;
                  }
                  // For bottom handle: dragging down (Y increases) = increase height
                  // Dragging up (Y decreases) = decrease height
                  const delta = e.clientY - startY;
                  const newHeight = Math.max(200, Math.min(800, startHeight + delta));
                  setInlineViewerHeight(newHeight);
                };
                const onMouseUp = () => {
                  isResizingRef.current = false;
                  document.removeEventListener('mousemove', onMouseMove);
                  document.removeEventListener('mouseup', onMouseUp);
                };
                document.addEventListener('mousemove', onMouseMove);
                document.addEventListener('mouseup', onMouseUp);
              }}
            >
              <GripHorizontal className="w-4 h-4 text-muted-foreground/40 group-hover:text-muted-foreground/70 transition-colors" />
            </div>
            )}
            <WorkflowViewerPanel
              projectId={projectId}
              workflowName={workflowExecution.workflowName}
              execution={workflowExecution}
              onClose={() => updateWorkflowViewerOpen(false)}
              compact={false}
              hideFullscreen={false}
              viewerMode={workflowViewerMode}
              onToggleViewerMode={handleToggleWorkflowViewerMode}
              onExpandedChange={setIsWorkflowViewerExpanded}
            />
          </div>
        )}

        {/* Messages Area - hidden when workflow viewer is expanded in inline mode */}
        {!(isWorkflowViewerExpanded && workflowViewerMode === 'inline') && (
        <ChatMessagesContainer
          chatId={chatId ?? undefined}
          onScrollStateChange={setScrollState}
          virtuosoRef={virtuosoRef}
          virtuosoAtBottom={virtuosoAtBottom}
        >
          {(messages.length > 0 ||
            runOutputs.length > 0 ||
            errorEvents.length > 0 ||
            infoEvents.length > 0) &&
            renderTimeline()}
        </ChatMessagesContainer>
        )}

        {/* Permissions Panel - positioned above chat input */}
        <PermissionsPanelWrapper>
          <PermissionsPanel chatId={chatId || undefined} />
        </PermissionsPanelWrapper>

        {/* Recovery Banner - shown when workflow was lost */}
        {needsRecovery && (
          <div className="flex-shrink-0 px-4 py-3 bg-amber-500/10 border-t border-amber-500/30 text-amber-600 dark:text-amber-400 text-sm">
            <div className="flex items-center justify-between gap-4">
              <div className="flex items-center gap-2">
                <svg className="w-4 h-4 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
                </svg>
                <span>Session was interrupted. Your message history is preserved.</span>
              </div>
              {onRestartConversation && (
                <button
                  onClick={onRestartConversation}
                  className="flex-shrink-0 px-3 py-1 text-xs font-medium bg-amber-500/20 hover:bg-amber-500/30 border border-amber-500/40 rounded-md transition-colors"
                >
                  Restart Conversation
                </button>
              )}
            </div>
          </div>
        )}


        {/* Input Area - Collapsible when not focused, hidden when workflow viewer is expanded in inline mode */}
        {!(isWorkflowViewerExpanded && workflowViewerMode === 'inline') && isFocused ? (
          <div className="flex-shrink-0">
            <ChatInputWrapper
              ref={chatInputRef}
              scrollState={scrollState}
              onSend={handleSendWithThread}
              onStop={onStopStreaming}
              disabled={false}
              worktreeId={worktreeId ?? undefined}
              isStreaming={isChatBusy}
              chatId={chatId ?? undefined}
              connectionStatus={
                connectionStatus as ConnectionStatus | undefined
              }
              isChatBusy={isChatBusy}
              currentActivity={currentActivity}
              messageCount={messages.length}
              onToggleRecentChanges={onToggleRecentChanges}
              isRecentChangesOpen={isRecentChangesOpen}
              projectId={projectId ?? undefined}
              paneId={paneId}
              selectedThreadId={selectedThreadId}
              workflowExecution={workflowExecution}
            />
          </div>
        ) : !(isWorkflowViewerExpanded && workflowViewerMode === 'inline') ? (
          <div className="p-2 border-t border-border bg-muted/20 text-center text-sm text-muted-foreground flex-shrink-0">
            Click to focus and type a message
          </div>
        ) : null}
      </div>
      
      {/* Side Panel Workflow Viewer - resizable panel beside chat */}
      {isSidePanelWorkflowViewerOpen && workflowExecution && projectId && (
        <div 
          className={isWorkflowViewerExpanded ? "flex-1 border-l border-border bg-background flex flex-col h-full relative min-w-0" : "flex-shrink-0 border-l border-border bg-background flex flex-col h-full relative"}
          style={isWorkflowViewerExpanded ? undefined : { width: sidePanelWidth }}
        >
          {/* Resize handle - hidden when expanded */}
          {!isWorkflowViewerExpanded && (
          <div
            className="absolute left-0 top-0 bottom-0 w-2 cursor-col-resize hover:bg-primary/20 transition-colors z-10 flex items-center justify-center group"
            onMouseDown={(e) => {
              e.preventDefault();
              e.stopPropagation();
              // Prevent multiple simultaneous resize operations
              if (isResizingRef.current) {
                return;
              }
              // Only allow resize if we're actually in side panel mode
              if (workflowViewerModeRef.current !== 'side') {
                return;
              }
              isResizingRef.current = true;
              const startX = e.clientX;
              const startWidth = sidePanelWidth;
              const onMouseMove = (e: MouseEvent) => {
                // Double-check mode hasn't changed during resize
                if (workflowViewerModeRef.current !== 'side') {
                  isResizingRef.current = false;
                  document.removeEventListener('mousemove', onMouseMove);
                  document.removeEventListener('mouseup', onMouseUp);
                  return;
                }
                // For left handle: dragging right (X increases) = decrease width (panel gets narrower)
                // Dragging left (X decreases) = increase width (panel gets wider)
                const delta = startX - e.clientX;
                const newWidth = Math.max(300, Math.min(800, startWidth + delta));
                setSidePanelWidth(newWidth);
              };
              const onMouseUp = () => {
                isResizingRef.current = false;
                document.removeEventListener('mousemove', onMouseMove);
                document.removeEventListener('mouseup', onMouseUp);
              };
              document.addEventListener('mousemove', onMouseMove);
              document.addEventListener('mouseup', onMouseUp);
            }}
          >
            <GripVertical className="w-4 h-4 text-muted-foreground/40 group-hover:text-muted-foreground/70 transition-colors" />
          </div>
          )}
          <WorkflowViewerPanel
            projectId={projectId}
            workflowName={workflowExecution.workflowName}
            execution={workflowExecution}
            onClose={() => updateWorkflowViewerOpen(false)}
            compact={false}
            hideFullscreen={false}
            viewerMode={workflowViewerMode}
            onToggleViewerMode={handleToggleWorkflowViewerMode}
            onExpandedChange={setIsWorkflowViewerExpanded}
          />
        </div>
      )}
    </div>
  );
});