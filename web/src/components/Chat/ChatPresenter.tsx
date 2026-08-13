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
import { ScrollToBottomButton } from "./ScrollToBottomButton";
import { PermissionsPanelWrapper } from "./PermissionsPanelWrapper";
import { PermissionsPanel } from "./PermissionsPanel";
import { ChatHeader } from "./ChatHeader";
import { ResumeDaemonPill } from "./ResumeDaemonPill";
import { OomKillBanner } from "./OomKillBanner";
import { BackgroundWorkPill } from "./BackgroundWorkPill";
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
import { useViewerStore } from "../../store/viewerStore";
import { MessageRole } from "../../gen/reliant/v1/chat_pb";
import { useCapability } from "../../lib/surfaceContext";
import { useThreadMessages } from "../../hooks/message-queries";

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
  connectionStatus: string;

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

  // Workflow execution sidebar
  workflowExecution?: WorkflowExecution;

  // Discuss mode
  isDiscussMode?: boolean;
  onToggleDiscuss?: () => void;

  // Question (ask_user) state
  hasPendingQuestion?: boolean;

  // Scroll-back paging (the initial message snapshot is bounded, so older
  // history is fetched on demand when the user scrolls to the top)
  onLoadOlderMessages?: () => void;
  isLoadingOlderMessages?: boolean;
  hasOlderMessages?: boolean;
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
  connectionStatus,
  worktreeId,
  projectId,
  needsRecovery,
  onRestartConversation,
  onSendMessage,
  onStopStreaming,
  isFocused = true,
  paneId,
  workflowExecution,
  isDiscussMode,
  onToggleDiscuss,
  hasPendingQuestion,
  onLoadOlderMessages,
  isLoadingOlderMessages,
  hasOlderMessages,
}: ChatPresenterProps) {
  const chatInputRef = useRef<HTMLDivElement>(null);

  // The workflow viewer (side/inline panel, its mouse-drag resize handles,
  // and the execution-detail chrome it exposes) is desktop-only: it can't
  // fit an iPhone viewport even at its resize floor, and its handles have no
  // touch equivalent. chatExecutionSidebar is false for both mobile and
  // embed, so this single flag decides whether any of that state, effects,
  // or markup mount at all — never render-and-hide.
  const showDesktopChrome = useCapability("chatExecutionSidebar");

  // Thread selection state (lifted up to share between header and timeline)
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null);

  // Background-work pill reveals a running command in the Commands tab, which
  // already accepts a processId to preselect.
  const openCommandsViewer = useViewerStore((state) => state.openCommandsViewer);
  const handleSelectBackgroundCommand = useCallback(
    (processId: string) => {
      if (!projectId) return;
      openCommandsViewer(projectId, worktreeId ?? undefined, processId);
    },
    [openCommandsViewer, projectId, worktreeId],
  );

  // Workspace state store for persisting workflow viewer state
  const getWorkflowViewerOpen = useWorkspaceStateStore((state) => state.getWorkflowViewerOpen);
  const setWorkflowViewerOpen = useWorkspaceStateStore((state) => state.setWorkflowViewerOpen);
  const getWorkflowViewerMode = useWorkspaceStateStore((state) => state.getWorkflowViewerMode);
  const setWorkflowViewerModeStore = useWorkspaceStateStore((state) => state.setWorkflowViewerMode);

  // Workflow viewer mode: 'inline' (above chat) or 'side' (beside chat)
  // Initialize from settings immediately. Skipped entirely off desktop —
  // the value is never read once showDesktopChrome is false, and reading
  // it anyway would just be dead code exercising settingsSync for nothing.
  const [workflowViewerMode, setWorkflowViewerMode] = useState<'inline' | 'side'>(() => {
    if (!showDesktopChrome) return 'side';
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

  // Load persisted workflow viewer state when chatId changes. Skipped off
  // desktop: the viewer never renders there (showDesktopChrome below), so
  // there is nothing for this state to drive.
  useEffect(() => {
    if (!showDesktopChrome || !chatId || !projectId) return;

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
    if (!showDesktopChrome || !chatId || !projectId || !workflowExecution) return;
    
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
  const isInlineWorkflowViewerOpen = showDesktopChrome && workflowViewerMode === 'inline' && isWorkflowViewerOpen && workflowExecution !== undefined;
  const isSidePanelWorkflowViewerOpen = showDesktopChrome && workflowViewerMode === 'side' && isWorkflowViewerOpen && workflowExecution !== undefined;


  // Show/hide the viewer, keeping whichever mode (inline or side) is already
  // set from the persisted per-chat preference or the global setting. Mode is
  // switched from inside the viewer panel itself, not from here.
  const handleToggleWorkflowViewer = useCallback(() => {
    updateWorkflowViewerOpen(!isWorkflowViewerOpen);
  }, [isWorkflowViewerOpen, updateWorkflowViewerOpen]);

  // Mode switch, driven by the viewer panel's own inline/side control.
  const handleToggleWorkflowViewerMode = useCallback(() => {
    updateWorkflowViewerMode(workflowViewerMode === 'inline' ? 'side' : 'inline');
  }, [workflowViewerMode, updateWorkflowViewerMode]);

  // Listen for settings changes (only update if no persisted mode exists - i.e., new chats)
  // This ensures new chats use the updated setting, but existing chats keep their preference.
  // No-op off desktop — nothing here is read once showDesktopChrome is false.
  useEffect(() => {
    if (!showDesktopChrome) return;
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
  }, [showDesktopChrome, chatId, projectId, worktreeId, getWorkflowViewerMode]);

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

  // A selected SIDE thread is fetched on its own; the main thread keeps
  // reading the chat-wide list, which is already exactly the main transcript
  // (plus the interleaved thread-start / handoff markers the timeline draws
  // around it, which only exist in the chat-wide view).
  // The `!!` matters: `selectedThreadId && …` yields the FALSY OPERAND when
  // selectedThreadId is null, so the ternary's condition — and therefore the
  // whole expression — was `string | null`, while useThreadMessages takes
  // `string | undefined`. Coercing to a boolean keeps the branch value in the
  // `string | undefined` the hook expects.
  const selectedSideThreadId =
    !!selectedThreadId &&
    selectedThreadId !== chatId &&
    selectedThreadId !== "0" &&
    selectedThreadId !== ""
      ? selectedThreadId
      : undefined;
  // chatId is `string | null` on this component's props, while the hook takes
  // `string | undefined` — both mean "no chat selected", so normalize at the
  // boundary rather than widening the hook.
  const { data: selectedThreadMessages } = useThreadMessages(
    chatId ?? undefined,
    selectedSideThreadId,
  );

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
  // Resume-follow callback registered by InterleavedTimeline — resets userScrolledUpRef
  const resumeFollowRef = useRef<(() => void) | null>(null);

  const handleAtBottomStateChange = useCallback((atBottom: boolean) => {
    virtuosoAtBottomRef.current = atBottom;
    setVirtuosoAtBottom(atBottom);
  }, []);

  const handleResumeFollow = useCallback((cb: () => void) => {
    resumeFollowRef.current = cb;
  }, []);

  // Scroll state from ChatMessagesContainer
  const [scrollState, setScrollState] = useState<{
    isAtBottom: boolean;
    hasScrolledUp: boolean;
    scrollToBottom: () => Promise<void>;
    resumeAutoScroll: () => void;
  } | null>(null);

  // Thinking indicator element, passed as Virtuoso footer
  const hasThinkingFooter = isChatBusy && pendingApprovals.length === 0 && !hasPendingQuestion;
  // Memoized because this element's identity flows into Virtuoso's `context`
  // prop (via footerContext below). A fresh element on every render defeats
  // that memo and re-renders the header, footer, and every visible row on each
  // pass — which, during streaming, is many times per second.
  const thinkingFooter = useMemo(
    () =>
      hasThinkingFooter ? (
        <ChatThinkingIndicator
          chatId={chatId || undefined}
          filterThreadId={selectedThreadId}
        />
      ) : undefined,
    [hasThinkingFooter, chatId, selectedThreadId]
  );

  // When thinking indicator appears, scroll to bottom so it's visible
  const prevHasThinkingFooterRef = useRef(false);
  useEffect(() => {
    if (hasThinkingFooter && !prevHasThinkingFooterRef.current && virtuosoAtBottomRef.current) {
      // Footer just appeared and user was at the bottom — use resumeFollow
      // to avoid the programmatic scroll being mistaken for user scroll-up
      requestAnimationFrame(() => {
        if (resumeFollowRef.current) {
          resumeFollowRef.current();
        } else {
          virtuosoRef.current?.scrollToIndex({ index: "LAST", align: "end", behavior: "auto" });
        }
      });
    }
    prevHasThinkingFooterRef.current = hasThinkingFooter;
  }, [hasThinkingFooter]);

  // Render the timeline
  const renderTimeline = () => {
    // With a thread selected, render THAT THREAD's own messages rather than
    // the chat-wide list narrowed down to it. Filtering only showed whatever
    // part of the thread happened to survive a window sized for the main
    // transcript, so selecting a busy spawn could show a fraction of it and
    // look complete. The thread-scoped read has the whole thread.
    const timelineMessages = selectedThreadMessages ?? messages;
    const filteredMessages = timelineMessages.filter(
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
        onResumeFollow={handleResumeFollow}
        footer={thinkingFooter}
        onSelectThread={setSelectedThreadId}
        onLoadOlderMessages={onLoadOlderMessages}
        isLoadingOlderMessages={isLoadingOlderMessages}
        hasOlderMessages={hasOlderMessages}
      />
    );
  };

  return (
    <div className="flex h-full layout-stable relative flex-1 min-w-0 min-h-0">
      {/* Main chat area */}
      <div
        className={isWorkflowViewerExpanded && workflowViewerMode === 'side' ? "hidden" : "flex flex-col flex-1 min-w-0 min-h-0 relative"}
        data-testid="chat-interface"
      >
        {/* Chat Header */}
        <ChatHeader
          chatId={chatId}
          selectedThreadId={selectedThreadId}
          onSelectThread={setSelectedThreadId}
          workflowExecution={workflowExecution}
          // Omitting the callback (rather than passing a no-op) is what
          // suppresses the "Show/Hide Workflow" menu entry — ChatHeader
          // gates on `onToggleWorkflowViewer &&`.
          onToggleWorkflowViewer={showDesktopChrome ? handleToggleWorkflowViewer : undefined}
          isWorkflowViewerOpen={isWorkflowViewerOpen}
        />

        <ResumeDaemonPill />

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
              chatId={chatId}
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
          resumeFollowRef={resumeFollowRef}
        >
          {(messages.length > 0 ||
            runOutputs.length > 0 ||
            errorEvents.length > 0 ||
            infoEvents.length > 0) &&
            renderTimeline()}
        </ChatMessagesContainer>
        )}

        {/* Background work (async spawns + running commands) — the spawn tool
            call that started them scrolls away, so this is the fixed surface
            that keeps them reachable. */}
        <BackgroundWorkPill
          chatId={chatId || undefined}
          worktreeId={worktreeId ?? undefined}
          onSelectThread={setSelectedThreadId}
          onSelectCommand={handleSelectBackgroundCommand}
        />

        {/* Permissions Panel - positioned above chat input */}
        <PermissionsPanelWrapper>
          <PermissionsPanel chatId={chatId || undefined} />
        </PermissionsPanelWrapper>

        {/* OOM banner — machine ran out of memory recently (cloud daemons) */}
        <OomKillBanner />

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


        {/* Floating scroll-to-bottom button */}
        <ScrollToBottomButton
          visible={!!(scrollState?.hasScrolledUp)}
          onClick={() => scrollState?.scrollToBottom()}
        />

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
              projectId={projectId ?? undefined}
              paneId={paneId}
              selectedThreadId={selectedThreadId}
              workflowExecution={workflowExecution}
              isDiscussMode={isDiscussMode}
              onToggleDiscuss={onToggleDiscuss}
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
            chatId={chatId}
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