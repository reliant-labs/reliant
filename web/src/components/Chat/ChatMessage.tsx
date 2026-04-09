import { useMemo, useEffect, useCallback, memo, useState, useRef, useLayoutEffect, type JSX } from "react";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";
import { GitBranch, Copy, Check, ChevronDown } from "lucide-react";
import { cn } from "../../lib/utils";
import { tabSwitchProfiler } from "../../lib/tabSwitchProfiler";
import { ToolExecution, type ToolResultData } from "./ToolExecution";
import { ToolExecutionGroup } from "./ToolExecutionGroup";
import { ToolExecutionCollapsibleGroup } from "./ToolExecutionCollapsibleGroup";
import { isReadOnlyTool } from "../../lib/toolFormatters";
import { MarkdownRenderer } from "./MarkdownRenderer";
import { ErrorMessage } from "./ErrorMessage";
import { MessageAttachments } from "./MessageAttachments";
import { BranchOptionsMenu } from "./BranchOptionsMenu";
import { BranchToWorktreeModal } from "./BranchToWorktreeModal";
import { BranchToExistingWorktreeModal } from "./BranchToExistingWorktreeModal";
import { CodeContextPill } from "./CodeContextPill";
import type { Message, ToolApprovalRequest } from "../../api/client";
import { useChatStore } from "../../store/chatStore"; // For getState() only
import { useActiveChatId, useProcessedMessages, useToolCallStates, useChat } from "../../store/chatStoreHooks";
// import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { useProjectStore } from "../../store/projectStore";
import { logger } from "../../lib/logger";
import { api } from "../../api/client";
import { toast } from "../../lib/toast-manager";
import {
  processMessage,
  type ToolCallData,
  type ToolResultData as ProcessedToolResultData,
} from "../../lib/messageProcessor";

interface ChatMessageProps {
  message: Message;
  approvals?: ToolApprovalRequest[];
  hideToolExecutions?: boolean;
  isLatestMessage?: boolean;
  isStreaming?: boolean;
  chatId?: string; // Chat ID for branching functionality
  onForceYield?: (toolCallId: string) => void;
  onSelectThread?: (threadId: string | null) => void;
}

// Re-export for components that import from here
type LocalToolResultData = ProcessedToolResultData;

interface ParsedContent {
  text?: string;
  toolExecutions?: Array<{
    call: ToolCallData;
    result?: LocalToolResultData;
    approval?: ToolApprovalRequest;
  }>;
}

const CONTEXT_MARKER_PATTERN = /\[\[([^\]]+):(\d+)-(\d+)\]\]/g;

function renderTextWithContextPills(
  text: string,
  worktreeId?: string
): Array<string | JSX.Element> {
  CONTEXT_MARKER_PATTERN.lastIndex = 0;
  const out: Array<string | JSX.Element> = [];
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = CONTEXT_MARKER_PATTERN.exec(text)) !== null) {
    const [marker, filePath, startLineStr, endLineStr] = match;
    const startLine = parseInt(startLineStr, 10);
    const endLine = parseInt(endLineStr, 10);

    if (match.index > lastIndex) {
      out.push(text.slice(lastIndex, match.index));
    }

    const fileName = filePath.split("/").pop() || filePath;
    out.push(
      <CodeContextPill
        key={`${filePath}:${startLine}-${endLine}:${match.index}`}
        context={{ filePath, fileName, startLine, endLine }}
        worktreeId={worktreeId}
        className="mx-1"
      />
    );

    lastIndex = match.index + marker.length;
  }

  if (lastIndex < text.length) {
    out.push(text.slice(lastIndex));
  }

  return out;
}

interface EnhancedToolExecution {
  call: ToolCallData;
  result?: ToolResultData;
  approval?: ToolApprovalRequest;
  status?:
    | "pending" // Queued but not started
    | "preparing" // LLM is writing the tool request
    | "requested" // LLM finished, ready for approval
    | "writing_input" // Legacy: use "preparing"
    | "executing" // Currently executing
    | "cancelling" // Cancellation requested
    | "cancelled" // Cancelled by user
    | "completed" // Successfully completed
    | "backgrounded" // Running in background
    | "denied" // Denied approval
    | "failed"; // Execution failed
  onCancel?: (id: string) => void;
  onConvertToBackground?: (id: string) => void;
}



function ChatMessageComponent({
  message,
  approvals = [],
  hideToolExecutions = false,
  isLatestMessage = false,
  isStreaming = false,
  chatId: propChatId,
  onForceYield,
  onSelectThread,
}: ChatMessageProps) {
  const isUser = message.role === MessageRole.USER;
  const activeChatId = useActiveChatId();
  // Prefer chatId from props (command center), fallback to chat navigation store
  const chatId = propChatId || activeChatId;

  // PERFORMANCE: Track message render timing
  const renderStartRef = useRef<number | undefined>(undefined);
  const renderCountRef = useRef(0);
  renderCountRef.current++;

  if (!renderStartRef.current && tabSwitchProfiler.isEnabled()) {
    renderStartRef.current = performance.now();
  }

  // Get pre-processed message data from store (FAST - no parsing needed)
  const processedMessages = useProcessedMessages(chatId || "");
  const processedFromStore = chatId ? processedMessages.get(message.id) || null : null;

  const currentProject = useProjectStore((state) => state.currentProject);

  // Get worktree_id from chat data for file link context
  const currentChat = useChat(chatId || "");
  const chatWorktreeId = currentChat?.worktreeId;
  const [copied, setCopied] = useState(false);
  const [fontKey, setFontKey] = useState(0);
  const [isExpanded, setIsExpanded] = useState(false);
  const [isOverflowing, setIsOverflowing] = useState(false);
  const [branchMenuPosition, setBranchMenuPosition] = useState<{ x: number; y: number } | null>(null);
  const [showBranchToWorktreeModal, setShowBranchToWorktreeModal] = useState(false);
  const [showBranchToExistingWorktreeModal, setShowBranchToExistingWorktreeModal] = useState(false);
  const bubbleRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  // Track prop changes to diagnose re-renders
  const prevPropsRef = useRef<null | {
    messageBlockCount: number;
    messageUpdatedAt?: string;
    approvalsLength: number;
    isLatestMessage: boolean;
    isStreaming: boolean;
    chatId?: string;
  }>(null);

  useEffect(() => {
    const prev = prevPropsRef.current;
    if (prev) {
      const changes: string[] = [];
      if (prev.messageBlockCount !== (message.contentBlocks?.length || 0)) {
        changes.push(`blocks(${prev.messageBlockCount}→${message.contentBlocks?.length || 0})`);
      }
      if (prev.messageUpdatedAt !== message.updatedAt) {
        changes.push(`updatedAt(${prev.messageUpdatedAt}→${message.updatedAt})`);
      }
      if (prev.approvalsLength !== approvals.length) {
        changes.push(`approvals(${prev.approvalsLength}→${approvals.length})`);
      }
      if (prev.isLatestMessage !== isLatestMessage) {
        changes.push(`isLatest(${prev.isLatestMessage}→${isLatestMessage})`);
      }
      if (prev.isStreaming !== isStreaming) {
        changes.push(`isStreaming(${prev.isStreaming}→${isStreaming})`);
      }
      if (prev.chatId !== chatId) {
        changes.push(`chatId(${prev.chatId?.slice(-8)}→${chatId?.slice(-8)})`);
      }

      // Debug logging removed
    }

    prevPropsRef.current = {
      messageBlockCount: message.contentBlocks?.length || 0,
      messageUpdatedAt: message.updatedAt,
      approvalsLength: approvals.length,
      isLatestMessage,
      isStreaming,
      chatId: chatId ?? undefined,
    };
  });

  // Listen for font changes and force re-render
  useEffect(() => {
    const handleFontChange = () => {
      setFontKey((prev) => prev + 1);
    };
    window.addEventListener("font-changed", handleFontChange);
    return () => window.removeEventListener("font-changed", handleFontChange);
  }, []);

  // Close expanded UI when clicking outside
  useEffect(() => {
    if (!isExpanded) return;

    const handleClickOutside = (event: MouseEvent) => {
      if (
        bubbleRef.current &&
        !bubbleRef.current.contains(event.target as Node)
      ) {
        setIsExpanded(false);
      }
    };

    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [isExpanded]);

  // Format timestamp to relative time (e.g., "2 minutes ago")
  const formatTimestamp = (dateString: string) => {
    const now = new Date();
    const then = new Date(dateString);
    const seconds = Math.floor((now.getTime() - then.getTime()) / 1000);

    if (seconds < 60) return "less than a minute ago";
    if (seconds < 120) return "1 minute ago";
    if (seconds < 3600) return `${Math.floor(seconds / 60)} minutes ago`;
    if (seconds < 7200) return "1 hour ago";
    if (seconds < 86400) return `${Math.floor(seconds / 3600)} hours ago`;
    if (seconds < 172800) return "1 day ago";
    if (seconds < 2592000) return `${Math.floor(seconds / 86400)} days ago`;
    if (seconds < 5184000) return "1 month ago";
    if (seconds < 31536000)
      return `${Math.floor(seconds / 2592000)} months ago`;
    return `${Math.floor(seconds / 31536000)} year${
      Math.floor(seconds / 31536000) > 1 ? "s" : ""
    } ago`;
  };

  // OPTIMIZED: Memoize approvals by content_block_id to avoid re-filtering on every render
  const approvalsByContentBlockId = useMemo(() => {
    const map = new Map<string, ToolApprovalRequest>();
    for (const approval of approvals) {
      if (approval.content_block_id) {
        map.set(approval.content_block_id, approval);
      }
    }
    return map;
  }, [approvals]);

  // Helper to find approval - matches by content_block_id (unique identifier for each content block)
  // This handles both: 1) approvals embedded by backend, 2) approvals that arrive via WebSocket after message
  const getApprovalStatus = useCallback(
    (contentBlockId: string): ToolApprovalRequest | undefined => {
      return approvalsByContentBlockId.get(contentBlockId);
    },
    [approvalsByContentBlockId]
  );

  // Show branch options menu when clicking the branch button
  const handleBranchClick = useCallback((e: React.MouseEvent) => {
    e.stopPropagation();
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    setBranchMenuPosition({ x: rect.left, y: rect.bottom + 4 });
  }, []);

  // Branch chat in same worktree (original behavior)
  const handleBranchChat = useCallback(async () => {
    const effectiveChatId = chatId || activeChatId;
    if (!effectiveChatId) {
      logger.error("No chat ID available for branching");
      return;
    }
    try {
      logger.info("Branching chat:", { chatId: effectiveChatId, messageId: message.id });
      await useChatStore.getState().branchChat(effectiveChatId, message.id);
    } catch (error) {
      logger.error("Failed to branch chat:", error);
      const errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      toast.error(`Failed to branch chat: ${errorMessage}`, { duration: 5000 });
    }
  }, [activeChatId, chatId, message.id]);

  // Open modal to branch chat to a new worktree
  const handleBranchToWorkspace = useCallback(() => {
    setShowBranchToWorktreeModal(true);
  }, []);

  // Open modal to branch chat to an existing worktree
  const handleBranchToExistingWorkspace = useCallback(() => {
    setShowBranchToExistingWorktreeModal(true);
  }, []);



  const handleCopy = async () => {
    try {
      // Copy formatted content instead of raw JSON
      let contentToCopy = "";

      if (parsed.text) {
        // Include full text with code contexts for copy
        contentToCopy += parsed.text + "\n\n";
      }

      if (parsed.toolExecutions && parsed.toolExecutions.length > 0) {
        parsed.toolExecutions.forEach((execution, index) => {
          // Format tool call with parameters like Claude does
          let toolCallText = `Tool: ${execution.call.name}`;
          if (
            execution.call.input &&
            typeof execution.call.input === "object" &&
            execution.call.input !== null
          ) {
            const params = Object.entries(execution.call.input)
              .map(([key, value]) => {
                if (typeof value === "string") {
                  return `${key}: ${value}`;
                } else if (typeof value === "object") {
                  return `${key}: ${JSON.stringify(value)}`;
                } else {
                  return `${key}: ${value}`;
                }
              })
              .join(", ");

            // Check if the formatted parameters would be too long or cluttered
            const totalLength = params.length;
            const hasLongStrings = Object.values(execution.call.input).some(
              (val) => typeof val === "string" && val.length > 50
            );
            const hasComplexObjects = Object.values(execution.call.input).some(
              (val) =>
                typeof val === "object" &&
                val !== null &&
                Object.keys(val).length > 3
            );

            // If parameters are too long, complex, or would be cluttered, just show the tool name
            if (totalLength > 100 || hasLongStrings || hasComplexObjects) {
              toolCallText += "()";
            } else {
              toolCallText += `(${params})`;
            }
          } else {
            toolCallText += "()";
          }

          contentToCopy += `${toolCallText}\n`;
          if (execution.result && execution.result.content) {
            contentToCopy += execution.result.content + "\n";
          } else {
            contentToCopy += "No output available\n";
          }
          if (index < parsed.toolExecutions!.length - 1) {
            contentToCopy += "\n";
          }
        });
      }

      // If no formatted content, fall back to text from contentBlocks
      if (!contentToCopy.trim()) {
        contentToCopy = (message.contentBlocks || [])
          .filter((b) => b.type === ContentBlockType.TEXT)
          .map((b) => b.content || "")
          .join("");
      }

      await navigator.clipboard.writeText(contentToCopy.trim());
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (error) {
      logger.error("Failed to copy message:", error);
    }
  };

  // OPTIMIZED: Get parsed content - uses pre-processed data from store for FAST rendering
  // Fallback uses processMessage() from messageProcessor.ts to avoid code duplication
  const parsed = useMemo((): ParsedContent => {
    // FAST PATH: Use pre-processed data from store if available
    // This is the common case after our performance optimization
    if (processedFromStore) {
      return {
        text: processedFromStore.text,
        toolExecutions: processedFromStore.toolExecutions?.map((exec) => ({
          call: exec.call,
          result: exec.result,
          approval: exec.approval,
        })),
      };
    }

    // FALLBACK: Process on-the-fly for streaming or edge cases
    // This uses the same logic as the store pre-processing
    const processed = processMessage(message, approvals);
    return {
      text: processed.text,
      toolExecutions: processed.toolExecutions?.map((exec) => ({
        call: exec.call,
        result: exec.result,
        approval: exec.approval,
      })),
    };
  }, [message, approvals, processedFromStore]);


  // Check for overflow after rendering - runs on text changes, font changes, and initial render
  // PERFORMANCE: Use requestAnimationFrame to batch DOM reads and avoid forced synchronous layout
  useEffect(() => {
    if (!contentRef.current || isExpanded) {
      setIsOverflowing(false);
      return;
    }

    const element = contentRef.current;
    let rafId: number;

    // Check overflow using requestAnimationFrame to batch with browser's layout pass
    // This avoids forcing a synchronous layout reflow
    const checkOverflow = () => {
      rafId = requestAnimationFrame(() => {
        if (element) {
          // 4.5rem = 72px for max height
          // Reading scrollHeight here is batched with the browser's layout
          const isCurrentlyOverflowing = element.scrollHeight > 72;
          setIsOverflowing(isCurrentlyOverflowing);
        }
      });
    };

    // Check after a brief delay to let React finish rendering
    // Using setTimeout + rAF ensures we're in the next frame after layout
    const timeoutId = setTimeout(checkOverflow, 0);

    // Also check after a delay for async font loading
    const fontTimeoutId = setTimeout(checkOverflow, 100);

    return () => {
      cancelAnimationFrame(rafId);
      clearTimeout(timeoutId);
      clearTimeout(fontTimeoutId);
    };
  }, [parsed.text, fontKey, isExpanded]);

  // Subscribe to tool call states for real-time updates
  // OPTIMIZED: Use stable selector to prevent re-renders when unrelated chats change
  const toolCallStates = useToolCallStates(chatId || "");

  // Enhance tool executions with status and cancel functionality
  const enhancedToolExecutions = useMemo(():
    | EnhancedToolExecution[]
    | undefined => {
    if (!parsed.toolExecutions || !chatId) return parsed.toolExecutions;

    const result = parsed.toolExecutions.map((execution): EnhancedToolExecution => {
      // Get the live tool call state from the store
      // CRITICAL: Use content_block_id for lookup, as that's what the backend sends in WebSocket updates
      // The execution.call.id is the LLM-generated tool_use ID (e.g., toolu_01...)
      // but the backend tracks by content_block_id
      const lookupKey = execution.call.content_block_id || execution.call.id;
      const toolCallState = toolCallStates.get(lookupKey);

      // Prefer embedded approval, but fall back to separate approvals list
      // Match approvals by content_block_id (unique identifier for content blocks)
      // This handles the race where approval arrives after the message
      const approval =
        execution.approval ||
        (execution.call.content_block_id
          ? getApprovalStatus(execution.call.content_block_id)
          : undefined);

      return {
        ...execution,
        approval, // Use resolved approval (embedded or from approvals array)
        status: toolCallState?.status,
        onCancel: async (toolCallId: string) => {
          if (chatId) {
            await useChatStore.getState().cancelToolCall(chatId, toolCallId);
          }
        },
        onConvertToBackground: async (toolCallId: string) => {
          if (chatId) {
            await api.toolCalls.convertToBackground(toolCallId);
          }
        },
      };
    });

    // Filter out ask_user tool calls — they render via the QuestionPrompt UI, not as tool cards
    return result.filter((exec) => exec.call.name !== "ask_user");
  }, [
    parsed.toolExecutions,
    chatId,
    toolCallStates,
    getApprovalStatus,
  ]);

  // Task processing moved to chatStore.ts for better performance
  // - Real-time updates: handled in WebSocket handler
  // - Loaded messages: handled in loadMessages/loadMoreMessages
  // This eliminates 300+ redundant useEffect calls when loading a chat

  // PERFORMANCE: Track when message finishes rendering
  // NOTE: Must be before early return to satisfy Rules of Hooks
  useLayoutEffect(() => {
    if (renderStartRef.current && tabSwitchProfiler.isEnabled()) {
      renderStartRef.current = undefined; // Reset for next render
    }
  });

  // Hide messages that have no useful content at all
  // When hideToolExecutions is true, treat tool executions as if they don't exist
  const hasVisibleContent =
    parsed.text ||
    (message.attachments && message.attachments.length > 0) ||
    (!hideToolExecutions &&
      enhancedToolExecutions &&
      enhancedToolExecutions.length > 0);

  if (!hasVisibleContent) {
    return null;
  }

  // Decide grouping: if more than 1 execution, show single grouped component
  const shouldGroup = (enhancedToolExecutions?.length || 0) > 1;

  const isOptimistic = message.id.startsWith("optimistic-");

  return (
    <div
      className={cn(
        "group copy-toast message-container",
        isUser ? "mb-3" : "mb-1.5",
        copied && "copied",
        isOptimistic && "opacity-60"
      )}
      data-testid={`message-${message.id}`}
    >
      <div className="message-layout lg:message-layout-lg">
        {/* Avatar - always on the left, perfectly aligned */}
        {/* <div className="avatar-container">
          <div
            className={cn(
              "w-8 h-8 rounded-full flex items-center justify-center message-avatar",
              isUser
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground border border-border/20"
            )}
          >
            {isUser ? (
              <User className="w-4 h-4 flex-shrink-0" />
            ) : (
              <Bot className="w-4 h-4 flex-shrink-0" />
            )}
          </div>
        </div> */}

        {/* Message Content */}
        <div
          className={cn(
            "flex-1 min-w-0",
            isUser && "flex flex-col items-start"
          )}
        >
          {/* User message bubble - flexible width */}
          {isUser ? (
            <div
              ref={bubbleRef}
              className={cn(
                "user-message-content border-2 border-border/70 rounded-lg w-full cursor-pointer block transition-all duration-200",
                isOverflowing && !isExpanded && "hover:border-border"
              )}
              style={{
                backgroundColor: "var(--chat-input-bg)",
              }}
              onClick={() => {
                setIsExpanded((prev) => {
                  const willExpand = !prev;
                  if (willExpand) {
                    // After expanding, scroll just enough so the bottom of the
                    // message bubble is visible (not hidden behind the chat input).
                    requestAnimationFrame(() => {
                      bubbleRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
                    });
                  }
                  return willExpand;
                });
              }}
            >
              {/* Text Content - show exactly as sent */}
              {parsed.text && (
                <div className="message-bubble relative">
                  <div
                    ref={contentRef}
                    className={cn(
                      "text-sm leading-relaxed",
                      !isExpanded && "overflow-hidden",
                      isExpanded && "overflow-y-auto"
                    )}
                    style={{
                      maxHeight: isExpanded ? "60vh" : "4.5rem",
                    }}
                  >
                    <div className="whitespace-pre-wrap break-words">
                      {renderTextWithContextPills(parsed.text, chatWorktreeId)}
                    </div>
                  </div>
                  {/* Fade effect only when content is actually overflowing */}
                </div>
              )}

              {/* Expand button - styled like diff expand button */}
              {!isExpanded && isOverflowing && (
                <div className="flex justify-center border-t border-border/50">
                  <div
                    className={cn(
                      "flex items-center gap-1 px-2 py-1 text-[11px] font-medium w-full",
                      "bg-muted/50 hover:bg-muted/80 transition-colors",
                      "text-muted-foreground hover:text-foreground justify-center"
                    )}
                  >
                    <ChevronDown className="w-3 h-3" />
                    Show more
                  </div>
                </div>
              )}

              {/* Attachments */}
              {isExpanded && message.attachments && message.attachments.length > 0 && (
                <MessageAttachments
                  attachments={message.attachments}
                  isUser={isUser}
                  className={!parsed.text ? "pt-1" : ""}
                />
              )}

              {/* Action buttons and timestamp - inside bubble, only shown when expanded */}
              {isExpanded && (
                <div
                  className="flex items-center justify-between text-xs text-muted-foreground pt-2 border-t mt-2"
                  style={{ borderColor: "var(--chat-border)" }}
                >
                  <div className="flex items-center gap-1">
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        handleCopy();
                      }}
                      title={copied ? "Copied!" : "Copy"}
                      className={cn(
                        "flex items-center gap-1 px-2 py-1 rounded-md hover:bg-muted/50 transition-colors duration-200 focus-ring",
                        copied && "text-success"
                      )}
                    >
                      {copied ? (
                        <Check className="w-3 h-3" />
                      ) : (
                        <Copy className="w-3 h-3" />
                      )}
                    </button>
                    <button
                      onClick={handleBranchClick}
                      disabled={isOptimistic}
                      title={isOptimistic ? "Waiting for message to save..." : "Branch"}
                      data-contextual-tip="branch-button"
                      className={cn(
                        "flex items-center gap-1 px-2 py-1 rounded-md transition-colors duration-200 focus-ring",
                        isOptimistic
                          ? "opacity-50 cursor-not-allowed"
                          : "hover:bg-muted/50"
                      )}
                    >
                      <GitBranch className="w-3 h-3" />
                    </button>
                  </div>

                  {/* Timestamp on the right */}
                  <span className="text-xs">
                    {formatTimestamp(message.createdAt || "")}
                  </span>
                </div>
              )}
            </div>
          ) : (
            // Assistant message - full width
            <div className="message-content w-full px-2">
              {/* Text Content */}
              {parsed.text && (
                <div className="message-bubble w-full">
                  {parsed.text.trim().startsWith("Error:") ? (
                    <ErrorMessage content={parsed.text} />
                  ) : (
                    <div className="relative">
                      <MarkdownRenderer
                        content={parsed.text}
                        isUser={false}
                        isStreaming={isStreaming}
                        worktreeId={chatWorktreeId}
                        className="text-sm w-full"
                      />
                    </div>
                  )}
                </div>
              )}

              {/* Attachments */}
              {message.attachments && message.attachments.length > 0 && (
                <MessageAttachments
                  attachments={message.attachments}
                  isUser={isUser}
                  className={!parsed.text ? "pt-1" : ""}
                />
              )}

              {/* Tool Executions - grouped to reduce clutter */}
              {/* Show tools even during streaming - they'll display in "preparing" state */}
              {!hideToolExecutions &&
                enhancedToolExecutions &&
                enhancedToolExecutions.length > 0 && (() => {
                  // Separate read-only tools from write/planning tools
                  const readOnlyTools = enhancedToolExecutions.filter((exec) =>
                    isReadOnlyTool(exec.call.name)
                  );
                  const otherTools = enhancedToolExecutions.filter((exec) =>
                    !isReadOnlyTool(exec.call.name)
                  );

                  return (
                    <div className="tool-executions-container space-y-1 mb-1">
                      {/* Render read-only tools - only group if 2+ tools */}
                      {readOnlyTools.length > 1 ? (
                        <ToolExecutionCollapsibleGroup
                          executions={readOnlyTools}
                          messageId={message.id}
                          chatId={chatId || undefined}
                          showRichContent={true}
                          onSelectThread={onSelectThread}
                        />
                      ) : (
                        readOnlyTools.map((exec, idx) => (
                          <ToolExecution
                            key={`${message.id}-readonly-${idx}`}
                            toolCall={exec.call}
                            toolResult={exec.result}
                            status={exec.status}
                            onCancel={exec.onCancel}
                            onConvertToBackground={exec.onConvertToBackground}
                            approval={exec.approval}
                            chatId={chatId || undefined}
                            showRichContent={true}
                            onSelectThread={onSelectThread}
                          />
                        ))
                      )}

                      {/* Render other tools with existing logic */}
                      {otherTools.length > 0 && (
                        shouldGroup && otherTools.length > 1 ? (
                          <ToolExecutionGroup
                            executions={otherTools}
                            messageId={message.id}
                            defaultCollapsed={true}
                            approvals={approvals}
                            chatId={chatId || undefined}
                            showRichContent={true}
                            onForceYield={onForceYield}
                            onSelectThread={onSelectThread}
                          />
                        ) : (
                          otherTools.map((exec, idx) => (
                            <ToolExecution
                              key={`${message.id}-exec-${idx}`}
                              toolCall={exec.call}
                              toolResult={exec.result}
                              status={exec.status}
                              onCancel={exec.onCancel}
                              onConvertToBackground={exec.onConvertToBackground}
                              onForceYield={onForceYield}
                              approval={exec.approval}
                              chatId={chatId || undefined}
                              showRichContent={true}
                              onSelectThread={onSelectThread}
                            />
                          ))
                        )
                      )}
                    </div>
                  );
                })()}

            </div>
          )}

          {/* Message Footer - action buttons for assistant messages */}
          {!isUser && (
            <div
              className={cn(
                "items-center gap-1 text-xs text-muted-foreground px-2 h-6",
                isLatestMessage
                  ? "flex"
                  : "hidden group-hover:flex"
              )}
            >
              <button
                onClick={handleCopy}
                title={copied ? "Copied!" : "Copy"}
                className={cn(
                  "flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-muted/50 transition-colors duration-200 focus-ring",
                  copied && "text-success"
                )}
              >
                {copied ? (
                  <Check className="w-3 h-3" />
                ) : (
                  <Copy className="w-3 h-3" />
                )}
              </button>
              {!isStreaming && (
                <button
                  onClick={handleBranchClick}
                  title="Branch"
                  data-contextual-tip="branch-button"
                  className="flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-muted/50 transition-colors duration-200 focus-ring"
                >
                  <GitBranch className="w-3 h-3" />
                </button>
              )}
              <span className="ml-1 text-[11px] text-muted-foreground/70">
                {formatTimestamp(message.createdAt || "")}
              </span>
            </div>
          )}
        </div>
      </div>

      {/* Branch options menu */}
      {branchMenuPosition && (
        <BranchOptionsMenu
          position={branchMenuPosition}
          onClose={() => setBranchMenuPosition(null)}
          onBranchChat={handleBranchChat}
          onBranchToWorkspace={handleBranchToWorkspace}
          onBranchToExistingWorkspace={handleBranchToExistingWorkspace}
        />
      )}

      {/* Branch to worktree modal */}
      {showBranchToWorktreeModal && chatId && currentProject && (
        <BranchToWorktreeModal
          isOpen={showBranchToWorktreeModal}
          onClose={() => setShowBranchToWorktreeModal(false)}
          chatId={chatId}
          messageId={message.id}
          projectId={currentProject.id}
          sourceWorktreeId={chatWorktreeId}
        />
      )}

      {/* Branch to existing worktree modal */}
      {showBranchToExistingWorktreeModal && chatId && currentProject && (
        <BranchToExistingWorktreeModal
          isOpen={showBranchToExistingWorktreeModal}
          onClose={() => setShowBranchToExistingWorktreeModal(false)}
          chatId={chatId}
          messageId={message.id}
          projectId={currentProject.id}
          sourceWorktreeId={chatWorktreeId}
        />
      )}
    </div>
  );
}

// OPTIMIZED: React.memo with custom comparison to prevent unnecessary re-renders
// Custom comparison prevents re-renders when only message reference changes
// but actual content hasn't changed (common during store updates)
export const ChatMessage = memo(ChatMessageComponent, (prev, next) => {
  // Return TRUE if props are equal (skip re-render)
  // Return FALSE if props changed (do re-render)

  // Fast path: if message object reference is the same, only check other props
  if (prev.message === next.message) {
    return (
      prev.isLatestMessage === next.isLatestMessage &&
      prev.isStreaming === next.isStreaming &&
      prev.chatId === next.chatId &&
      prev.hideToolExecutions === next.hideToolExecutions &&
      prev.approvals === next.approvals
    );
  }

  // Message reference changed - check individual fields
  if (prev.message.id !== next.message.id) return false;
  if (prev.message.updatedAt !== next.message.updatedAt) return false;
  if (prev.message.contentBlocks !== next.message.contentBlocks) return false;
  if (prev.isLatestMessage !== next.isLatestMessage) return false;
  if (prev.isStreaming !== next.isStreaming) return false;
  if (prev.chatId !== next.chatId) return false;
  if (prev.hideToolExecutions !== next.hideToolExecutions) return false;

  // Check approvals - if same reference, skip deep comparison
  if (prev.approvals === next.approvals) return true;

  // Deep compare approvals array - only re-render if actual approval data changed
  const prevApprovals = prev.approvals || [];
  const nextApprovals = next.approvals || [];

  if (prevApprovals.length !== nextApprovals.length) return false;

  // If lengths match, compare approval IDs and statuses
  for (let i = 0; i < prevApprovals.length; i++) {
    if (prevApprovals[i].id !== nextApprovals[i].id ||
        prevApprovals[i].status !== nextApprovals[i].status) {
      return false;
    }
  }

  // All checks passed - props are equal, skip re-render
  return true;
});