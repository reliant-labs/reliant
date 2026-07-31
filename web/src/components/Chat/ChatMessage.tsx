import {
  useMemo,
  useEffect,
  useCallback,
  memo,
  useState,
  useRef,
  useLayoutEffect,
  Fragment,
  type JSX,
} from "react";
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
import {
  useActiveChatId,
  useToolResultsByCallId,
  useToolCallStates,
  useChat,
} from "../../store/chatStoreHooks";
// import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { useProjectStore } from "../../store/projectStore";
import { logger } from "../../lib/logger";
import { api } from "../../api/client";
import { toast } from "../../lib/toast-manager";
import {
  getProcessedMessage,
  type ToolCallData,
  type ToolResultData as ProcessedToolResultData,
  type MessageSegment,
} from "../../lib/messageProcessor";

export type ChatTimelineVariant = "compact" | "card" | "minimal";

interface ChatMessageProps {
  message: Message;
  approvals?: ToolApprovalRequest[];
  hideToolExecutions?: boolean;
  isLatestMessage?: boolean;
  isStreaming?: boolean;
  chatId?: string; // Chat ID for branching functionality
  onSelectThread?: (threadId: string | null) => void;
  timelineVariant?: ChatTimelineVariant;
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
  // Ordered text/tool timeline — the canonical render order. Empty for older
  // pre-processed store entries that predate segments (handled at render).
  segments: MessageSegment[];
}

const CONTEXT_MARKER_PATTERN = /\[\[([^\]]+):(\d+)-(\d+)\]\]/g;

function renderTextWithContextPills(
  text: string,
  worktreeId?: string,
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
      />,
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
  onSelectThread,
  timelineVariant = "compact",
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

  // Normalized tool-result index for this chat; joined into the message's
  // tool calls at read time via the reference-keyed processMessage memo below.
  const toolResultsByCallId = useToolResultsByCallId(chatId || "");

  const currentProject = useProjectStore((state) => state.currentProject);

  // Get worktree_id from chat data for file link context
  const currentChat = useChat(chatId || "");
  const chatWorktreeId = currentChat?.worktreeId;
  const [copied, setCopied] = useState(false);
  const [fontKey, setFontKey] = useState(0);
  const [isExpanded, setIsExpanded] = useState(false);
  const [isOverflowing, setIsOverflowing] = useState(false);
  const [branchMenuPosition, setBranchMenuPosition] = useState<{
    x: number;
    y: number;
  } | null>(null);
  const [showBranchToWorktreeModal, setShowBranchToWorktreeModal] =
    useState(false);
  const [
    showBranchToExistingWorktreeModal,
    setShowBranchToExistingWorktreeModal,
  ] = useState(false);
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
        changes.push(
          `blocks(${prev.messageBlockCount}→${message.contentBlocks?.length || 0})`,
        );
      }
      if (prev.messageUpdatedAt !== message.updatedAt) {
        changes.push(
          `updatedAt(${prev.messageUpdatedAt}→${message.updatedAt})`,
        );
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
    [approvalsByContentBlockId],
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
      logger.info("Branching chat:", {
        chatId: effectiveChatId,
        messageId: message.id,
      });
      await useChatStore.getState().branchChat(effectiveChatId, message.id);
    } catch (error) {
      logger.error("Failed to branch chat:", error);
      const errorMessage =
        error instanceof Error ? error.message : "Unknown error occurred";
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
              (val) => typeof val === "string" && val.length > 50,
            );
            const hasComplexObjects = Object.values(execution.call.input).some(
              (val) =>
                typeof val === "object" &&
                val !== null &&
                Object.keys(val).length > 3,
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

  // Parsed content via the reference-keyed processMessage memo. The memo
  // recomputes only when the message object, the chat's tool-result index, or
  // the approvals list changes reference (all fresh on immutable content
  // change, stable otherwise) — so this stays fast on tab switches / re-renders
  // without a hand-maintained store cache, and never renders stale results.
  const parsed = useMemo((): ParsedContent => {
    const processed = getProcessedMessage(message, toolResultsByCallId, approvals);
    return {
      text: processed.text,
      toolExecutions: processed.toolExecutions?.map((exec) => ({
        call: exec.call,
        result: exec.result,
        approval: exec.approval,
      })),
      segments: processed.segments,
    };
  }, [message, approvals, toolResultsByCallId]);

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
          // 3rem = 48px for max height
          // Reading scrollHeight here is batched with the browser's layout
          const isCurrentlyOverflowing = element.scrollHeight > 48;
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

    const result = parsed.toolExecutions.map(
      (execution): EnhancedToolExecution => {
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
      },
    );

    // Filter out ask_user tool calls — they render via the QuestionPrompt UI, not as tool cards
    return result.filter((exec) => exec.call.name !== "ask_user");
  }, [parsed.toolExecutions, chatId, toolCallStates, getApprovalStatus]);

  // Index the enhanced executions by their tool-call id so each ordered
  // segment can look up the enriched version (with live status / approval /
  // cancel handlers) while preserving the message's text↔tool interleaving.
  const enhancedById = useMemo(() => {
    const map = new Map<string, EnhancedToolExecution>();
    for (const exec of enhancedToolExecutions ?? []) {
      map.set(exec.call.id, exec);
    }
    return map;
  }, [enhancedToolExecutions]);

  // Task processing moved to chatStore.ts for better performance
  // - Real-time updates: handled in WebSocket handler
  // - Loaded messages: handled in loadMessages
  // This eliminates 300+ redundant useEffect calls when loading a chat

  // PERFORMANCE: Track when message finishes rendering
  // NOTE: Must be before early return to satisfy Rules of Hooks
  useLayoutEffect(() => {
    if (renderStartRef.current && tabSwitchProfiler.isEnabled()) {
      renderStartRef.current = undefined; // Reset for next render
    }
  });

  const hasAttachments = Boolean(message.attachments?.length);

  // Hide messages that have no useful content at all
  // When hideToolExecutions is true, treat tool executions as if they don't exist
  const hasVisibleContent =
    parsed.text ||
    hasAttachments ||
    (!hideToolExecutions &&
      enhancedToolExecutions &&
      enhancedToolExecutions.length > 0);

  if (!hasVisibleContent) {
    return null;
  }

  // Decide grouping: if more than 1 execution, show single grouped component
  const shouldGroup = (enhancedToolExecutions?.length || 0) > 1;

  const toolDensity =
    timelineVariant === "card"
      ? "card"
      : timelineVariant === "minimal"
        ? "minimal"
        : "compact";

  // Render one contiguous run of tool executions (a single "tools" segment).
  // Read-only tools collapse together; write/planning tools use the existing
  // group-or-list logic. keyPrefix keeps React keys unique across segments.
  const renderToolRun = (
    executions: EnhancedToolExecution[],
    keyPrefix: string,
  ): JSX.Element | null => {
    if (executions.length === 0) return null;
    const readOnlyTools = executions.filter((exec) =>
      isReadOnlyTool(exec.call.name),
    );
    const otherTools = executions.filter(
      (exec) => !isReadOnlyTool(exec.call.name),
    );

    return (
      <div
        className={cn(
          "tool-executions-container mb-1",
          timelineVariant === "card" ? "space-y-2" : "space-y-1",
        )}
      >
        {/* Render read-only tools - only group if 2+ tools */}
        {readOnlyTools.length > 1 ? (
          <ToolExecutionCollapsibleGroup
            executions={readOnlyTools}
            messageId={message.id}
            chatId={chatId || undefined}
            showRichContent={true}
            onSelectThread={onSelectThread}
            density={toolDensity}
          />
        ) : (
          readOnlyTools.map((exec, idx) => (
            <ToolExecution
              key={`${keyPrefix}-readonly-${idx}`}
              toolCall={exec.call}
              toolResult={exec.result}
              status={exec.status}
              onCancel={exec.onCancel}
              onConvertToBackground={exec.onConvertToBackground}
              approval={exec.approval}
              chatId={chatId || undefined}
              showRichContent={true}
              onSelectThread={onSelectThread}
              density={toolDensity}
            />
          ))
        )}

        {/* Render other tools with existing logic */}
        {otherTools.length > 0 &&
          (shouldGroup && otherTools.length > 1 ? (
            <ToolExecutionGroup
              executions={otherTools}
              messageId={message.id}
              defaultCollapsed={true}
              approvals={approvals}
              chatId={chatId || undefined}
              showRichContent={true}
              onSelectThread={onSelectThread}
              density={toolDensity}
            />
          ) : (
            otherTools.map((exec, idx) => (
              <ToolExecution
                key={`${keyPrefix}-exec-${idx}`}
                toolCall={exec.call}
                toolResult={exec.result}
                status={exec.status}
                onCancel={exec.onCancel}
                onConvertToBackground={exec.onConvertToBackground}
                approval={exec.approval}
                chatId={chatId || undefined}
                showRichContent={true}
                onSelectThread={onSelectThread}
                density={toolDensity}
              />
            ))
          ))}
      </div>
    );
  };

  const isOptimistic = message.id.startsWith("optimistic-");
  const timestampText = message.createdAt ? formatTimestamp(message.createdAt) : "";
  const variantClass = `chat-message-${timelineVariant}`;

  const messageActions = (
    <div
      className={cn(
        "message-actions mt-0.5 flex items-center gap-0.5 text-[9px] text-muted-foreground/70 opacity-0 transition-all duration-150 group-hover:opacity-100 group-focus-within:opacity-100",
        isUser ? "justify-start pl-0.5" : "justify-start px-0.5"
      )}
    >
      {timestampText && (
        <time
          dateTime={message.createdAt}
          className="px-0.5 text-[9px] leading-none text-muted-foreground/70"
          title={new Date(message.createdAt).toLocaleString()}
        >
          {timestampText}
        </time>
      )}
      <button
        onClick={(e) => {
          e.stopPropagation();
          handleCopy();
        }}
        title={copied ? "Copied" : "Copy"}
        aria-label={copied ? "Copied" : "Copy message"}
        className={cn(
          "rounded p-0.5 transition-colors duration-150 hover:bg-muted/70 hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring/40",
          copied && "text-success"
        )}
        type="button"
      >
        {copied ? <Check className="h-2.5 w-2.5" /> : <Copy className="h-2.5 w-2.5" />}
      </button>
      <button
        onClick={(e) => {
          e.stopPropagation();
          handleBranchClick(e);
        }}
        disabled={isOptimistic}
        title={isOptimistic ? "Waiting for message to save" : "Branch"}
        aria-label="Branch from message"
        data-contextual-tip="branch-button"
        className={cn(
          "rounded p-0.5 transition-colors duration-150 hover:bg-muted/70 hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring/40",
          isOptimistic && "cursor-not-allowed opacity-50"
        )}
        type="button"
      >
        <GitBranch className="h-2.5 w-2.5" />
      </button>
    </div>
  );

  return (
    <div
      className={cn(
        "group copy-toast message-container relative",
        "mb-1",
        variantClass,
        copied && "copied",
        isOptimistic && "opacity-60",
      )}
      data-testid={`message-${message.id}`}
      data-chat-timeline-variant={timelineVariant}
    >
      <div className="message-layout lg:message-layout-lg">
        {/* Message Content */}
        <div className="flex-1 min-w-0">
          {/* User message bubble - stable width */}
          {isUser ? (
            <div className="group/usermsg relative inline-flex max-w-[85%] flex-col items-start sm:max-w-2xl">
              <div className="relative">
                <div
                  ref={bubbleRef}
                  className={cn(
                    "user-message-content relative block cursor-pointer overflow-hidden rounded-2xl border border-primary/25 bg-primary/15 text-foreground shadow-sm transition-colors duration-200",
                    "hover:border-primary/35 hover:bg-primary/20",
                    timelineVariant === "card" && "shadow-md",
                    timelineVariant === "minimal" && "shadow-none"
                  )}
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
                    <div
                      ref={contentRef}
                      className="overflow-hidden text-sm leading-relaxed text-foreground"
                      style={{
                        maxHeight: isExpanded ? "none" : "3rem",
                        transition: "max-height 0.2s ease-in-out",
                      }}
                    >
                      <div className="whitespace-pre-wrap break-words">
                        {renderTextWithContextPills(parsed.text, chatWorktreeId)}
                      </div>
                    </div>
                  )}

                  {/* Expand button - styled like diff expand button */}
                  {!isExpanded && isOverflowing && (
                    <div className="mt-1 flex justify-center border-t border-primary/15 pt-1">
                      <div className="flex w-full items-center justify-center gap-1 rounded-lg px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-primary/10 hover:text-foreground">
                        <ChevronDown className="w-3 h-3" />
                        Show more
                      </div>
                    </div>
                  )}

                  {/* Attachments */}
                  {hasAttachments && (
                    <MessageAttachments
                      attachments={message.attachments}
                      isUser={isUser}
                      className={!parsed.text ? "pt-1" : ""}
                    />
                  )}
                </div>
              </div>
              {messageActions}
            </div>
          ) : (
            // Assistant message - full width
            <div
              className={cn(
                "message-content group/assistant relative w-full px-2",
                timelineVariant === "card" && "rounded-xl border border-border/60 bg-card/60 p-3 shadow-sm",
                timelineVariant === "minimal" && "px-0"
              )}
            >
              {/* Ordered content: text and tool runs interleaved exactly as
                  they occurred in the message, so a mid-message tool call
                  renders between paragraphs, not after all the prose. */}
              {parsed.segments.map((segment, segIdx) => {
                if (segment.kind === "text") {
                  if (!segment.text) return null;
                  return (
                    <div
                      key={`${message.id}-seg-${segIdx}`}
                      className="message-bubble w-full"
                    >
                      {segment.text.trim().startsWith("Error:") ? (
                        <ErrorMessage content={segment.text} />
                      ) : (
                        <div className="relative">
                          <MarkdownRenderer
                            content={segment.text}
                            isUser={false}
                            isStreaming={isStreaming}
                            worktreeId={chatWorktreeId}
                            className="text-sm w-full"
                          />
                        </div>
                      )}
                    </div>
                  );
                }

                // Tool run — show during streaming too (preparing state).
                if (hideToolExecutions) return null;
                const runExecutions = segment.executions
                  .map((exec) => enhancedById.get(exec.call.id))
                  .filter((e): e is EnhancedToolExecution => e !== undefined);
                return (
                  <Fragment key={`${message.id}-seg-${segIdx}`}>
                    {renderToolRun(runExecutions, `${message.id}-seg-${segIdx}`)}
                  </Fragment>
                );
              })}

              {/* Attachments */}
              {hasAttachments && (
                <MessageAttachments
                  attachments={message.attachments}
                  isUser={isUser}
                  className={!parsed.text ? "pt-1" : ""}
                />
              )}

              {messageActions}
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
      prev.timelineVariant === next.timelineVariant &&
      prev.onSelectThread === next.onSelectThread &&
      prev.approvals === next.approvals
    );
  }

  // Message reference changed - check individual fields
  if (prev.message.id !== next.message.id) return false;
  if (prev.message.updatedAt !== next.message.updatedAt) return false;
  if (prev.message.contentBlocks !== next.message.contentBlocks) return false;
  if (prev.message.attachments !== next.message.attachments) return false;
  if (prev.isLatestMessage !== next.isLatestMessage) return false;
  if (prev.isStreaming !== next.isStreaming) return false;
  if (prev.chatId !== next.chatId) return false;
  if (prev.hideToolExecutions !== next.hideToolExecutions) return false;
  if (prev.timelineVariant !== next.timelineVariant) return false;
  if (prev.onSelectThread !== next.onSelectThread) return false;

  // Check approvals - if same reference, skip deep comparison
  if (prev.approvals === next.approvals) return true;

  // Deep compare approvals array - only re-render if actual approval data changed
  const prevApprovals = prev.approvals || [];
  const nextApprovals = next.approvals || [];

  if (prevApprovals.length !== nextApprovals.length) return false;

  // If lengths match, compare approval IDs and statuses
  for (let i = 0; i < prevApprovals.length; i++) {
    if (
      prevApprovals[i].id !== nextApprovals[i].id ||
      prevApprovals[i].status !== nextApprovals[i].status
    ) {
      return false;
    }
  }

  // All checks passed - props are equal, skip re-render
  return true;
});