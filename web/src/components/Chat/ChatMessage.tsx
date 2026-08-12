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
import { GitBranch, FolderSync, Copy, Check, ChevronDown } from "lucide-react";
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
import { MessageActionsSheet, type MessageActionsSheetAction } from "./MessageActionsSheet";
import { CodeContextPill } from "./CodeContextPill";
import type { Message, ToolApprovalRequest } from "../../api/client";
import { useChatStore } from "../../store/chatStore"; // For getState() only
import {
  useActiveChatId,
  useToolResultsByCallId,
  useToolCallStates,
  useChat,
} from "../../store/chatStoreHooks";
import { useSurface } from "../../lib/surfaceContext";
import { useLongPress } from "../../hooks/useLongPress";
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

/**
 * User-message bubble heights.
 *
 * Collapsed is the teaser shown by default. Expanded is a scroll cap, not a
 * fit — a pasted stack trace or a wall of prose stays a bounded block the user
 * scrolls inside of, so it can never dominate the viewport. It is capped in
 * `vh` so the bubble can never outgrow the viewport it scrolls within; a `rem`
 * cap taller than the window leaves the tail unreachable, because the
 * virtualized timeline scrolls the row rather than the bubble. Pinned is the
 * single-line breadcrumb in the sticky header.
 */
const COLLAPSED_MESSAGE_MAX_HEIGHT = "3rem";
const COLLAPSED_MESSAGE_MAX_HEIGHT_PX = 48;
const EXPANDED_MESSAGE_MAX_HEIGHT = "50vh";
const PINNED_MESSAGE_MAX_HEIGHT = "1.5rem";

/** Fades the last line of a clipped message out instead of cutting it mid-glyph. */
const COLLAPSED_FADE_MASK = "linear-gradient(to bottom, #000 60%, transparent 100%)";

interface ChatMessageProps {
  message: Message;
  approvals?: ToolApprovalRequest[];
  hideToolExecutions?: boolean;
  isLatestMessage?: boolean;
  isStreaming?: boolean;
  chatId?: string; // Chat ID for branching functionality
  onSelectThread?: (threadId: string | null) => void;
  timelineVariant?: ChatTimelineVariant;
  /**
   * Rendered as the sticky header above the timeline rather than in the flow.
   * Clamps to a single line, drops attachments and hover actions, and never
   * expands — the pin is a breadcrumb, not a copy of the message.
   */
  pinned?: boolean;
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
  pinned = false,
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
  const surface = useSurface();
  const isMobile = surface === "mobile";
  const [showActionsSheet, setShowActionsSheet] = useState(false);
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
          // Reading scrollHeight here is batched with the browser's layout
          const isCurrentlyOverflowing =
            element.scrollHeight > COLLAPSED_MESSAGE_MAX_HEIGHT_PX;
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
        // Get the live tool call state from the store. Keyed by the LLM
        // tool_use id (execution.call.id, e.g. "toolu_01..."), which is what
        // the backend addresses tool status by. Note this is a DIFFERENT
        // identifier space from the content_block_id the approval lookup just
        // below uses — status and approvals are not interchangeable keys.
        const toolCallState = toolCallStates.get(execution.call.id);

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

  // Long-press is wired unconditionally (before the early return, per Rules
  // of Hooks); the handlers are only spread onto message markup on mobile.
  const handleLongPress = useCallback(() => {
    if (typeof navigator !== "undefined" && typeof navigator.vibrate === "function") {
      navigator.vibrate(10);
    }
    setShowActionsSheet(true);
  }, []);
  const longPressHandlers = useLongPress(handleLongPress);

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
  const fullTimestampText = message.createdAt
    ? new Date(message.createdAt).toLocaleString()
    : "";
  const variantClass = `chat-message-${timelineVariant}`;

  // Mobile-only: the bottom sheet substitute for the hover toolbar. Built
  // from the same handlers the desktop buttons call, so branch/copy behavior
  // never diverges between surfaces — only the affordance to reach them does.
  const mobileMessageActions: MessageActionsSheetAction[] = isMobile
    ? [
        {
          key: "copy",
          label: copied ? "Copied" : "Copy message",
          icon: copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />,
          onSelect: () => void handleCopy(),
        },
        {
          key: "branch",
          label: "Branch in place",
          icon: <GitBranch className="h-4 w-4" />,
          disabled: isOptimistic,
          onSelect: () => void handleBranchChat(),
        },
        {
          key: "branch-existing",
          label: "Branch to existing workspace",
          icon: <FolderSync className="h-4 w-4" />,
          disabled: isOptimistic,
          onSelect: handleBranchToExistingWorkspace,
        },
      ]
    : [];

  // The floating toolbar's box is inert (see `floatingMessageActions`), so each
  // control has to opt back in for itself — and only while the toolbar is
  // actually visible. Reviving them unconditionally would leave invisible
  // buttons swallowing clicks at rest, which is the bug this avoids.
  //
  // The inline variant is in the flow with nothing underneath it and does not
  // disable its box, so these classes are simply redundant there.
  const revealedControl =
    "pointer-events-none group-hover:pointer-events-auto group-focus-within:pointer-events-auto";

  const actionControls = (
    <>
      {timestampText && (
        <time
          dateTime={message.createdAt}
          className="px-1 text-[11px] leading-none text-muted-foreground"
          title={fullTimestampText}
        >
          {timestampText}
        </time>
      )}
      {/* Separates the timestamp (information) from the buttons (actions). */}
      {timestampText && <span aria-hidden className="h-3 w-px bg-border" />}
      <button
        onClick={(e) => {
          e.stopPropagation();
          handleCopy();
        }}
        title={copied ? "Copied" : "Copy"}
        aria-label={copied ? "Copied" : "Copy message"}
        className={cn(
          "rounded p-1 text-foreground/70 transition-colors duration-150 hover:bg-muted hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring/40",
          revealedControl,
          copied && "text-success"
        )}
        type="button"
      >
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
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
          "rounded p-1 text-foreground/70 transition-colors duration-150 hover:bg-muted hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring/40",
          revealedControl,
          isOptimistic && "cursor-not-allowed opacity-50"
        )}
        type="button"
      >
        <GitBranch className="h-3.5 w-3.5" />
      </button>
    </>
  );

  // The newest message is the one being read, so its toolbar sits in the flow
  // underneath instead of floating over the text. Nothing follows it, so the
  // row it reserves shifts nothing, and it needs none of the opaque backing the
  // floating variant requires to stay legible over text.
  //
  // Because the row is reserved either way, it stays on screen rather than
  // fading in — dimmed at rest so it reads as chrome, full strength on hover.
  const inlineMessageActions = (
    <div
      className={cn(
        "message-actions mt-1 flex w-max items-center gap-1 whitespace-nowrap",
        "rounded-md px-1 py-0.5 text-[11px] font-medium",
        "opacity-60 transition-opacity duration-150",
        "hover:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100"
      )}
    >
      {actionControls}
    </div>
  );

  // Floated out of flow for every older message. In-flow controls would reserve
  // their height under all of them even while hidden (that was ~24px of dead
  // space per message), and revealing them in flow would shove the conversation
  // on hover. Absolute positioning costs nothing when idle and moves nothing
  // when shown.
  //
  // The wrapper spans the message's full height and the toolbar sticks inside
  // it, so hovering a message taller than the viewport still shows the toolbar:
  // anchoring it to the message's top hid it above the fold exactly when the
  // message was long enough to need it. The sticky offset clears the pinned
  // user-message header the timeline floats over the top of the transcript
  // (`--chat-pinned-header-h`, 0 when there is no header or no timeline).
  //
  // Anchored to the message ROW — the full content column — for both roles,
  // never to the message's own box. A user bubble is only as wide as its text,
  // so hanging the toolbar off the bubble's edge (`left-full`) put it at an
  // x-position that depended on message length and on how much room the column
  // had left. Every ancestor between here and the viewport clips overflow (the
  // Virtuoso scroller, ChatMessagesContainer, #layout-main-content, <main>), so
  // as soon as the sidebar, file viewer, or terminal squeezed the column, a
  // long message's toolbar ran past the column edge and was cut off — reading
  // as the toolbar hiding behind the neighbouring panel.
  //
  // Anchoring to the column instead makes overflow impossible: the toolbar can
  // never be wider than the column it is right-aligned inside. It also lands in
  // the same place for every message regardless of role or length, so it is a
  // stationary mouse target rather than one that moves with the text.
  //
  // The overlap is resolved on the VERTICAL axis: the column reserves a band of
  // height above its content (`pt-7`), and since `inset-y-0` measures from the
  // padding box, `top-0` is the top of that band. So at rest the toolbar sits in
  // a strip of its own with the message starting below it, and it covers nothing.
  //
  // Vertical is the right axis because the collision is with things that are
  // pinned to the right edge — an assistant message is a stack of tool rows
  // whose own controls sit flush right, exactly where a right-aligned toolbar
  // wants to be. Buying room horizontally means narrowing every message to hold
  // a toolbar that is only visible on hover, and it has to be wide enough for
  // the timestamp too. Buying it vertically costs one 28px strip that the
  // message was already leaving empty above its first line.
  //
  // It still needs the opaque backing: sticky keeps it reachable on a message
  // taller than the viewport (see above), and once stuck mid-message it does
  // float over text. That is the pre-existing trade, and `pointer-events-none`
  // on the box means even then it only ever intercepts its own two buttons.
  const floatingMessageActions = (
    <div className="pointer-events-none absolute inset-y-0 right-2 z-20">
      <div
        className={cn(
          "message-actions sticky flex items-center gap-1",
          // Out of flow, so the toolbar has no width to lay out against and
          // will happily wrap "1 minute ago" onto three lines. Size to content.
          "w-max whitespace-nowrap",
          // The fill must be fully opaque: this floats over message text, and
          // anything translucent lets that text read through the toolbar.
          //
          // Do NOT express the lift as a `bg-gradient-*` wash over `bg-popover` —
          // tailwind-merge treats both as the `bg-*` group and drops the opaque
          // colour, leaving only the translucent gradient. The raised tone is a
          // solid custom value instead, so there is exactly one background.
          //
          // It also has to separate from a pure-black page (--background 0%,
          // --popover 6%: a 1.10:1 edge, i.e. invisible), hence the lighter fill
          // plus a white-alpha hairline and a real shadow.
          "rounded-md text-popover-foreground",
          "bg-[hsl(var(--popover))] dark:bg-[hsl(0_0%_14%)]",
          "border border-foreground/15 shadow-lg shadow-black/50",
          "px-1 py-0.5 text-[11px] font-medium",
          "pointer-events-none opacity-0 transition-opacity duration-150",
          "group-hover:opacity-100",
          "group-focus-within:opacity-100"
        )}
        style={{ top: "var(--chat-pinned-header-h, 0px)" }}
      >
        {actionControls}
      </div>
    </div>
  );

  // The pin is a breadcrumb of a message that already exists below; it gets no
  // toolbar at all.
  const showInlineActions = isLatestMessage && !pinned;

  // Only the floating variant overlaps the message, so only it needs the column
  // to reserve the band. Charging it unconditionally would push every message
  // down for a toolbar that is not there: mobile shows these actions in a
  // long-press sheet and never reveals the hover toolbar at all, the pinned
  // breadcrumb has no toolbar, and the newest message uses the inline variant.
  const reservesToolbarBand = !pinned && !showInlineActions && !isMobile;

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
        {/* Message Content. `relative` so the floating toolbar below anchors to
            the full content column for every message, rather than to whatever
            width the individual message happens to occupy.

            `pt-7` reserves a 28px band above the content for the floating
            toolbar to occupy. The toolbar's wrapper is `inset-y-0`, which
            measures from the padding box, so its `top-0` is the top of that
            band and the message starts underneath it — the toolbar covers
            nothing at rest, whatever its width.

            The band is vertical rather than a right-hand gutter because what it
            collides with is right-pinned: a tool row keeps its own controls
            flush right, precisely where a right-aligned toolbar sits. Reserving
            width would narrow every message permanently to make room for chrome
            that only appears on hover, and would have to fit the timestamp too.
            The strip above the first line is space the message was already
            leaving blank. */}
        <div
          className={cn(
            "relative flex-1 min-w-0",
            reservesToolbarBand && "pt-7"
          )}
        >
          {/* User message bubble - stable width */}
          {isUser ? (
            <div
              className={cn(
                "group/usermsg relative flex flex-col items-start",
                pinned ? "w-full" : "inline-flex max-w-[85%] sm:max-w-2xl"
              )}
            >
              <div className={cn("relative", pinned && "w-full")}>
                <div
                  ref={bubbleRef}
                  className={cn(
                    "user-message-content relative block overflow-hidden rounded-2xl border border-primary/25 bg-primary/15 text-foreground shadow-sm transition-colors duration-200",
                    pinned
                      ? "w-full cursor-default"
                      : "cursor-pointer hover:border-primary/35 hover:bg-primary/20",
                    !pinned && timelineVariant === "card" && "shadow-md",
                    !pinned && timelineVariant === "minimal" && "shadow-none"
                  )}
                  onClick={() => {
                    if (pinned) return;
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
                  {...(isMobile && !pinned ? longPressHandlers : {})}
                >
                  {/* Text Content - show exactly as sent */}
                  {parsed.text && (
                    <div
                      ref={contentRef}
                      className={cn(
                        "text-sm leading-relaxed text-foreground",
                        // Expanded messages scroll inside the bubble instead of
                        // growing without bound — a pasted log should not push
                        // the whole conversation off screen.
                        isExpanded && !pinned ? "overflow-y-auto" : "overflow-hidden"
                      )}
                      style={{
                        maxHeight: pinned
                          ? PINNED_MESSAGE_MAX_HEIGHT
                          : isExpanded
                            ? EXPANDED_MESSAGE_MAX_HEIGHT
                            : COLLAPSED_MESSAGE_MAX_HEIGHT,
                        transition: "max-height 0.2s ease-in-out",
                        // Truncation reads as text fading out rather than a
                        // full-width button bar, which cost more vertical space
                        // than the teaser it labelled. A mask is used instead of
                        // a gradient overlay because the bubble is a translucent
                        // tint — an opaque gradient would not match it.
                        ...(!pinned && !isExpanded && isOverflowing
                          ? { maskImage: COLLAPSED_FADE_MASK, WebkitMaskImage: COLLAPSED_FADE_MASK }
                          : {}),
                      }}
                    >
                      <div
                        className={cn(
                          "whitespace-pre-wrap break-words",
                          // The pin is a one-line breadcrumb; ellipsize rather
                          // than reflow the sticky header as the user scrolls.
                          pinned && "line-clamp-1"
                        )}
                      >
                        {renderTextWithContextPills(parsed.text, chatWorktreeId)}
                      </div>
                    </div>
                  )}

                  {/* Attachments — the pin is text-only, images belong in the flow */}
                  {!pinned && hasAttachments && (
                    <MessageAttachments
                      attachments={message.attachments}
                      isUser={isUser}
                      className={!parsed.text ? "pt-0.5" : ""}
                    />
                  )}

                  {/* Expand/collapse affordance — a single compact chevron row.
                      Also renders when expanded so a long message can be closed
                      without scrolling back to find the top of the bubble. */}
                  {!pinned && (isOverflowing || isExpanded) && (
                    <div className="flex items-center gap-1 pt-0.5 text-[11px] font-medium text-muted-foreground/80">
                      <ChevronDown
                        className={cn(
                          "h-3 w-3 transition-transform duration-200",
                          isExpanded && "rotate-180"
                        )}
                      />
                      {isExpanded ? "Show less" : "Show more"}
                    </div>
                  )}
                </div>
              </div>
              {showInlineActions && inlineMessageActions}
            </div>
          ) : (
            // Assistant message - full width
            <div
              className={cn(
                "message-content group/assistant relative w-full px-2",
                timelineVariant === "card" && "rounded-xl border border-border/60 bg-card/60 p-3 shadow-sm",
                timelineVariant === "minimal" && "px-0"
              )}
              {...(isMobile && !pinned ? longPressHandlers : {})}
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

              {showInlineActions && inlineMessageActions}
            </div>
          )}

          {/* Rendered once, outside the role branches, so it anchors to the
              content column rather than to the user bubble or the assistant
              block. The pin is a breadcrumb and gets no toolbar at all. */}
          {!pinned && !showInlineActions && floatingMessageActions}
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

      {/* Mobile long-press actions sheet — the touch substitute for the
          desktop hover toolbar (see the long-press wiring above). */}
      {isMobile && (
        <MessageActionsSheet
          isOpen={showActionsSheet}
          onClose={() => setShowActionsSheet(false)}
          actions={mobileMessageActions}
          timestampLabel={fullTimestampText || undefined}
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