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
import Tooltip from "../forge-ui/tooltip";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";
import { GitBranch, FolderSync, Copy, Check, MoreHorizontal } from "lucide-react";
import { cn } from "../../lib/utils";
import { tabSwitchProfiler } from "../../lib/tabSwitchProfiler";
import { ToolExecution, type ToolResultData } from "./ToolExecution";
import { ToolExecutionGroup } from "./ToolExecutionGroup";
import { fetchFullToolBodies } from "../../hooks/tool-result-queries";
import { ToolExecutionCollapsibleGroup } from "./ToolExecutionCollapsibleGroup";
import { isReadOnlyTool, isRequestMachineTool } from "../../lib/toolFormatters";
import { RequestMachineCard, requestMachineReason } from "./tool-renderers/RequestMachineToolRenderer";
import { DraftAutomationCard, isDraftAutomationTool } from "./tool-renderers/DraftAutomationToolRenderer";
import { AskUserCard } from "./AskUserCard";
import { MarkdownRenderer } from "./MarkdownRenderer";
import { ErrorMessage } from "./ErrorMessage";
import { MessageAttachments } from "./MessageAttachments";
import { MessageGeneratedImages } from "./MessageGeneratedImages";
import { MessageGeneratedVideos, isVideoMimeType } from "./MessageGeneratedVideos";
import { MessageTimestamp, useMessageBranching } from "./MessageChrome";
import { viewerSafeMessage } from "../../lib/viewerResults";
import { MessageActionsSheet, type MessageActionsSheetAction } from "./MessageActionsSheet";
import { CodeContextPill } from "./CodeContextPill";
import { FailedSendStatus } from "./FailedSendStatus";
import { clientIdOfFailedSend } from "../../lib/pendingSends";
import type { Message } from "../../api/client";
import type { Approval } from "../../gen/reliant/v1/approval_pb";
import { useChatStore } from "../../store/chatStore"; // For getState() only
import {
  useChatIdOrActive,
  useToolResultsByCallId,
  useToolCallStates,
  useChat,
} from "../../store/chatStoreHooks";
import { useSurface, useCapability } from "../../lib/surfaceContext";

// import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";
import {
  getProcessedMessage,
  type ToolCallData,
  type ToolResultData as ProcessedToolResultData,
  type MessageSegment,
} from "../../lib/messageProcessor";
import * as chatActions from "../../hooks/chat-actions";
import { isQuietToolRun } from "./thread-views/transcriptPresentation";
import type { MessageSlice } from "./thread-views/simplifiedTurns";

type CopiedExecution = {
  call: Pick<ToolCallData, "id" | "input" | "inputElided" | "content_block_id">;
  result?: Pick<ProcessedToolResultData, "content" | "truncated" | "elided" | "block_id">;
};

function parseCopiedInput(raw: string | undefined): ToolCallData["input"] {
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return raw;
  }
}

/**
 * Each execution's input and result as a copy writes them: the bodies a stub
 * read elided and the results a read previewed, fetched whole in one batch
 * (fetchFullToolBodies). Throws rather than copy a skeleton or a stub.
 */
async function fullBodiesOfExecutions(
  chatId: string | null | undefined,
  executions: CopiedExecution[],
): Promise<{ inputs: ToolCallData["input"][]; results: (string | undefined)[] }> {
  const elidedBlockIds: string[] = [];
  const truncatedCallIds: string[] = [];
  for (const { call, result } of executions) {
    if (call.inputElided) {
      if (!call.content_block_id) throw new Error("A tool call's input was left out and cannot be read");
      elidedBlockIds.push(call.content_block_id);
    }
    if (result?.elided) {
      if (!result.block_id) throw new Error("A tool result was left out and cannot be read");
      elidedBlockIds.push(result.block_id);
    } else if (result?.truncated) {
      truncatedCallIds.push(call.id);
    }
  }
  const needsFetch = elidedBlockIds.length > 0 || truncatedCallIds.length > 0;
  if (needsFetch && !chatId) throw new Error("Tool bodies cannot be read without the chat");
  const full = needsFetch && chatId
    ? await fetchFullToolBodies(chatId, { elidedBlockIds, truncatedCallIds })
    : { bodies: {}, fullResults: new Map<string, string>() };
  return {
    inputs: executions.map(({ call }) =>
      call.inputElided && call.content_block_id
        ? parseCopiedInput(full.bodies[call.content_block_id]?.input)
        : call.input,
    ),
    results: executions.map(({ call, result }) =>
      result?.elided && result.block_id
        ? full.bodies[result.block_id]?.content
        : result?.truncated
          ? full.fullResults.get(call.id)
          : result?.content,
    ),
  };
}

/**
 * The user bubble's clamp: eight lines, then "Show more". Expanded, it is a
 * scroll cap rather than a fit, so a pasted stack trace stays a bounded block
 * the user scrolls inside of. The cap is in `vh` so the bubble never outgrows
 * the viewport it scrolls within: the virtualized timeline scrolls the row,
 * not the bubble, so a taller cap leaves the tail unreachable.
 */
const USER_MESSAGE_CLAMP_CLASS = "line-clamp-[8]";
const USER_MESSAGE_EXPANDED_CLASS = "max-h-[50vh] overflow-y-auto";

/**
 * Transcript reading type: the user bubble and the assistant's answer.
 *
 * Desktop reads at `text-copy` (16/26). The mobile root is 17px, so the
 * mobile surface spells the same 16px as `text-ui` with 1.6 leading
 * (16/25.6): `text-copy` there would read larger on a phone than on a
 * desktop. Both are rem, so the Appearance step still scales them.
 *
 * `--chat-prose-leading` reaches MarkdownRenderer's paragraphs, which set
 * their own leading.
 */
const MOBILE_PROSE_TYPE_CLASS = "text-ui leading-[1.6] [--chat-prose-leading:1.6]";
const DESKTOP_PROSE_TYPE_CLASS = "text-copy";

/** A 28px ghost icon button: the actions row's copy and branch. */
const ACTION_ICON_BUTTON_CLASS =
  "inline-flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors duration-150 hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

interface ChatMessageProps {
  message: Message;
  approvals?: Approval[];
  hideToolExecutions?: boolean;
  isLatestMessage?: boolean;
  isStreaming?: boolean;
  chatId?: string; // Chat ID for branching functionality
  onSelectThread?: (threadId: string | null) => void;
  /**
   * Rendered as the sticky header above the timeline rather than in the flow.
   * Clamps to a single line, drops attachments and actions, and never
   * expands — the pin is a breadcrumb, not a copy of the message.
   */
  pinned?: boolean;
  /**
   * Render only part of an assistant message, for the simplified thread (see
   * `MessageSlice`). Absent: the whole message, as always.
   */
  slice?: MessageSlice;
  /**
   * False when a later row of the same turn follows: only the row that ends
   * the turn carries the actions row. Absent: it does.
   */
  turnEnd?: boolean;
}

function sameSlice(a: MessageSlice | undefined, b: MessageSlice | undefined): boolean {
  if (a === b) return true;
  if (!a || !b || a.role !== b.role || Boolean(a.chrome) !== Boolean(b.chrome)) return false;
  return sameList(a.segmentIndexes, b.segmentIndexes) && sameList(a.callIds, b.callIds);
}

function sameList<T>(a: readonly T[] | undefined, b: readonly T[] | undefined): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  return a.every((value, i) => value === b[i]);
}

// Re-export for components that import from here
type LocalToolResultData = ProcessedToolResultData;

interface ParsedContent {
  text?: string;
  toolExecutions?: Array<{
    call: ToolCallData;
    result?: LocalToolResultData;
    approval?: Approval;
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
  approval?: Approval;
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
  pinned = false,
  slice: sliceProp,
  turnEnd = true,
}: ChatMessageProps) {
  const isUser = message.role === MessageRole.USER;
  // A slice only ever cuts an assistant message.
  const slice = isUser ? undefined : sliceProp;
  const sliceRole = slice?.role;
  // Prefer chatId from props (command center), fallback to chat navigation store
  const chatId = useChatIdOrActive(propChatId);

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

  // Get worktree_id from chat data for file link context
  const currentChat = useChat(chatId || "");
  const chatWorktreeId = currentChat?.worktreeId;
  const [copied, setCopied] = useState(false);
  const [fontKey, setFontKey] = useState(0);
  const [isExpanded, setIsExpanded] = useState(false);
  const [isOverflowing, setIsOverflowing] = useState(false);
  const {
    available: branchAvailable,
    openBranchMenu,
    branchInPlace: handleBranchChat,
    branchToExistingWorkspace: handleBranchToExistingWorkspace,
    branchUi,
  } = useMessageBranching(chatId, message.id);
  const surface = useSurface();
  const isMobile = surface === "mobile";
  const canBranch = useCapability("chatBranching") && branchAvailable;
  const [showActionsSheet, setShowActionsSheet] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);

  // Listen for font changes and force re-render
  useEffect(() => {
    const handleFontChange = () => {
      setFontKey((prev) => prev + 1);
    };
    window.addEventListener("font-changed", handleFontChange);
    return () => window.removeEventListener("font-changed", handleFontChange);
  }, []);

  // OPTIMIZED: Memoize approvals by content_block_id to avoid re-filtering on every render
  const approvalsByContentBlockId = useMemo(() => {
    const map = new Map<string, Approval>();
    for (const approval of approvals) {
      if (approval.entityId) {
        map.set(approval.entityId, approval);
      }
    }
    return map;
  }, [approvals]);

  // Helper to find approval - matches by content_block_id (unique identifier for each content block)
  // This handles both: 1) approvals embedded by backend, 2) approvals that arrive via WebSocket after message
  const getApprovalStatus = useCallback(
    (contentBlockId: string): Approval | undefined => {
      return approvalsByContentBlockId.get(contentBlockId);
    },
    [approvalsByContentBlockId],
  );

  const handleCopy = async () => {
    try {
      // Copy formatted content instead of raw JSON
      let contentToCopy = "";

      if (sliceRole === "prose") {
        // The message's prose, every slice of it — the chrome stands for the
        // whole message — but never the work folded between its slices.
        contentToCopy += parsed.segments
          .flatMap((segment) => (segment.kind === "text" && segment.text.trim() ? [segment.text] : []))
          .join("\n\n");
      } else if (parsed.text) {
        // Include full text with code contexts for copy
        contentToCopy += parsed.text + "\n\n";
      }

      if (!slice && parsed.toolExecutions && parsed.toolExecutions.length > 0) {
        // The transcript may hold a call's input as a skeleton, its result as
        // a stub (both elided by a stub read) or a large result as a preview:
        // copy the bodies, read whole in one batch, never what labelled the row.
        // A viewer of another member's shared chat copies each call's input
        // and never its result, however the result reached this client.
        const viewer = isViewer;
        const executions = viewer
          ? parsed.toolExecutions.map((execution) => ({ ...execution, result: undefined }))
          : parsed.toolExecutions;
        const { inputs, results } = await fullBodiesOfExecutions(chatId, executions);
        executions.forEach((execution, index) => {
          const input = inputs[index];
          // Format tool call with parameters like Claude does
          let toolCallText = `Tool: ${execution.call.name}`;
          if (
            input &&
            typeof input === "object" &&
            input !== null
          ) {
            const params = Object.entries(input)
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
            const hasLongStrings = Object.values(input).some(
              (val) => typeof val === "string" && val.length > 50,
            );
            const hasComplexObjects = Object.values(input).some(
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
          const resultContent = results[index];
          if (viewer) {
            contentToCopy += "Result withheld: it stays on the owner's machine\n";
          } else if (resultContent) {
            contentToCopy += resultContent + "\n";
          } else {
            contentToCopy += "No output available\n";
          }
          if (index < executions.length - 1) {
            contentToCopy += "\n";
          }
        });
      }

      // If no formatted content, fall back to text from contentBlocks
      if (!slice && !contentToCopy.trim()) {
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
      // A copy that would need a body it could not read copies nothing.
      void toast.error(error);
    }
  };

  // Parsed content via the reference-keyed processMessage memo. The memo
  // recomputes only when the message object, the chat's tool-result index, or
  // the approvals list changes reference (all fresh on immutable content
  // change, stable otherwise) — so this stays fast on tab switches / re-renders
  // without a hand-maintained store cache, and never renders stale results.
  // A viewer of another member's shared chat never sees a tool's result:
  // every view of the message below (rows, cards, media, copy) reads it
  // through viewerSafeMessage.
  const isViewer = !!currentChat?.viewerAccess;
  const parsed = useMemo((): ParsedContent => {
    const processed = viewerSafeMessage(getProcessedMessage(message, toolResultsByCallId, approvals), isViewer);
    return {
      text: processed.text,
      toolExecutions: processed.toolExecutions?.map((exec) => ({
        call: exec.call,
        result: exec.result,
        approval: exec.approval,
      })),
      segments: processed.segments,
    };
  }, [message, approvals, toolResultsByCallId, isViewer]);

  // The segments this row renders: all of them, or the slice's.
  const visibleSegments = (() => {
    const all = parsed.segments.map((segment, segIdx) => ({ segment, segIdx }));
    if (!slice) return all;
    const wanted = new Set(slice.segmentIndexes);
    return all.filter(({ segIdx }) => wanted.has(segIdx));
  })();

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
          // Reading scrollHeight here is batched with the browser's layout.
          // The clamp hides the overflow, so clipped text is the difference.
          const isCurrentlyOverflowing =
            element.scrollHeight > element.clientHeight + 1;
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

        // A spawn's child workflow (= the thread its card previews) comes off
        // the content block on reload, where the snapshot joins it from the
        // durable row. Live, that block was persisted before the spawn ran
        // and never has it; the spawn's own status event does. Fill the gap
        // so every reader of call.childWorkflowId sees the same fact either
        // way.
        const childWorkflowId =
          execution.call.childWorkflowId ?? toolCallState?.childWorkflowId;
        const call =
          childWorkflowId === execution.call.childWorkflowId
            ? execution.call
            : { ...execution.call, childWorkflowId };

        return {
          ...execution,
          call,
          approval, // Use resolved approval (embedded or from approvals array)
          status: toolCallState?.status,
          onCancel: async (toolCallId: string) => {
            if (chatId) {
              await useChatStore.getState().cancelToolCall(chatId, toolCallId);
            }
          },
          onConvertToBackground: async (toolCallId: string) => {
            if (chatId) {
              await chatActions.convertToolCallToBackground(toolCallId);
            }
          },
        };
      },
    );

    // Every ask_user call stays, blocking or not: it renders as its own card
    // (AskUserCard), below, and never as nothing.
    return result;
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

  // Hide messages that have no useful content at all.
  // When hideToolExecutions is true, treat tool executions as if they don't
  // exist — except the questions to the user (ask_user, request_machine),
  // whose cards render whatever the tool rows show.
  const hasUserCards = Boolean(
    enhancedToolExecutions?.some(
      (exec) =>
        exec.call.name === "ask_user" ||
        isRequestMachineTool(exec.call.name) ||
        (isDraftAutomationTool(exec.call.name) && !!exec.result && !exec.result.is_error),
    ),
  );
  // A prose slice without the chrome is text alone: no actions, timestamp or
  // attachments (those are the message's last prose slice's).
  const hasChrome = !slice || Boolean(slice.chrome);
  // A slice judges visibility from its own segments. work / surfaced rows
  // only know once they have rendered, so they are checked after the helpers.
  const hasVisibleContent = slice
    ? sliceRole !== "prose" ||
      (hasChrome && hasAttachments) ||
      visibleSegments.some(({ segment }) => segment.kind === "text" && Boolean(segment.text))
    : parsed.text ||
      hasAttachments ||
      hasUserCards ||
      (!hideToolExecutions &&
        enhancedToolExecutions &&
        enhancedToolExecutions.length > 0);

  if (!hasVisibleContent) {
    return null;
  }

  // Decide grouping: if more than 1 execution, show single grouped component
  const shouldGroup = (enhancedToolExecutions?.length || 0) > 1;

  // Render one contiguous run of tool executions (a single "tools" segment).
  // Read-only tools collapse together; write/planning tools use the existing
  // group-or-list logic. keyPrefix keeps React keys unique across segments.
  const renderToolRun = (
    executions: EnhancedToolExecution[],
    keyPrefix: string,
  ): JSX.Element | null => {
    if (executions.length === 0) return null;
    // Mobile keeps original run order and ownership. Only successful, settled
    // work may recede; approvals, errors and live work stay directly visible.
    if (isMobile) {
      if (isQuietToolRun(executions)) {
        return <ToolExecutionGroup executions={executions} messageId={message.id}
          chatId={chatId || undefined} showRichContent onSelectThread={onSelectThread} />;
      }
      return <div className="space-y-1">{executions.map((exec) => (
        <ToolExecution key={exec.call.content_block_id || exec.call.id}
          toolCall={exec.call} toolResult={exec.result} status={exec.status}
          onCancel={exec.onCancel} onConvertToBackground={exec.onConvertToBackground}
          approval={exec.approval} chatId={chatId || undefined} showRichContent
          onSelectThread={onSelectThread} />
      ))}</div>;
    }
    const readOnlyTools = executions.filter((exec) =>
      isReadOnlyTool(exec.call.name),
    );
    const otherTools = executions.filter(
      (exec) => !isReadOnlyTool(exec.call.name),
    );

    return (
      <div className="tool-executions-container mb-1 space-y-1">
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
            />
          ))
        )}

        {/* Render other tools with existing logic */}
        {otherTools.length > 0 &&
          (shouldGroup && otherTools.length > 1 ? (
            <ToolExecutionGroup
              executions={otherTools}
              messageId={message.id}
              chatId={chatId || undefined}
              showRichContent={true}
              onSelectThread={onSelectThread}
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
              />
            ))
          ))}
      </div>
    );
  };

  const renderTextSegment = (text: string, segIdx: number): JSX.Element | null => {
    if (!text) return null;
    return (
      <div key={`${message.id}-seg-${segIdx}`} className="w-full">
        {text.trim().startsWith("Error:") ? (
          <ErrorMessage content={text} />
        ) : (
          <div className="relative">
            <MarkdownRenderer
              content={text}
              isUser={false}
              isStreaming={isStreaming}
              worktreeId={chatWorktreeId}
              className={cn(isMobile ? MOBILE_PROSE_TYPE_CLASS : DESKTOP_PROSE_TYPE_CLASS, "w-full")}
            />
          </div>
        )}
      </div>
    );
  };

  const renderToolsSegment = (
    executions: EnhancedToolExecution[],
    segIdx: number,
  ): JSX.Element | null => {
    const keyBase = `${message.id}-seg-${segIdx}`;
    // A prose row carries no tools. A fold's work row and a surfaced call's
    // row render what they were given exactly as the detailed transcript
    // does: the simplified pass decides which calls go where.
    if (sliceRole === "prose") return null;

    // request_machine is a question to the user, not a record of
    // work: it renders as its own card, outside the tool rows and
    // even when tool rows are hidden (NO_MACHINE_CHATS.md §3).
    const machineRequestCards = executions
      .filter((exec) => isRequestMachineTool(exec.call.name))
      .map((exec) => (
        <RequestMachineCard
          key={`${keyBase}-request-machine-${exec.call.id}`}
          chatId={chatId || undefined}
          reason={requestMachineReason(exec.call.input)}
        />
      ));
    // An ask_user is a question to the user too, shown on its own
    // card whatever the tool rows show.
    const askUserCards = executions
      .filter((exec) => exec.call.name === "ask_user")
      .map((exec) => (
        <AskUserCard
          key={`${keyBase}-ask-user-${exec.call.id}`}
          chatId={chatId || undefined}
          toolCallId={exec.call.id}
          input={exec.call.input}
          finished={exec.result !== undefined}
        />
      ));
    // A drafted automation is a decision for the user too: its card
    // ("Review & create") shows once the draft is back and valid.
    const draftCards = executions
      .filter((exec) => isDraftAutomationTool(exec.call.name) && exec.result && !exec.result.is_error)
      .map((exec) => (
        <DraftAutomationCard
          key={`${keyBase}-draft-automation-${exec.call.id}`}
          chatId={chatId || undefined}
          metadata={exec.result?.metadata}
        />
      ));
    const userCards = [...machineRequestCards, ...askUserCards, ...draftCards];
    if (hideToolExecutions) {
      return userCards.length > 0 ? <Fragment key={keyBase}>{userCards}</Fragment> : null;
    }
    const runExecutions = executions.filter(
      (exec) => !isRequestMachineTool(exec.call.name) && exec.call.name !== "ask_user",
    );
    // Images the tools in this run generated. Rendered AFTER the
    // tool card and OUTSIDE it: the card explains how the image was
    // made (and is collapsed by default), the image is the result.
    // Emitting them per-segment rather than once at the end of the
    // message keeps text -> image -> text ordering intact when a
    // turn generates an image and then keeps talking.
    const runAttachments = runExecutions.flatMap((exec) => exec.result?.attachments ?? []);
    const runVideos = runAttachments.filter((a) => isVideoMimeType(a.mimeType));
    const runImages = runAttachments.filter((a) => !isVideoMimeType(a.mimeType));
    const media = (
      <>
        {runImages.length > 0 && <MessageGeneratedImages attachments={runImages} />}
        {runVideos.length > 0 && <MessageGeneratedVideos attachments={runVideos} />}
      </>
    );

    return (
      <Fragment key={keyBase}>
        {renderToolRun(runExecutions, keyBase)}
        {userCards}
        {media}
      </Fragment>
    );
  };

  // A slice that names calls renders only those of its tools segments.
  const sliceCalls = slice?.callIds ? new Set(slice.callIds) : null;

  // Tool-run segments show during streaming too (preparing state).
  const renderSegment = (segment: MessageSegment, segIdx: number): JSX.Element | null => {
    if (segment.kind === "text") {
      return sliceRole === "surfaced" ? null : renderTextSegment(segment.text, segIdx);
    }
    const executions = segment.executions
      .filter((exec) => !sliceCalls || sliceCalls.has(exec.call.id))
      .map((exec) => enhancedById.get(exec.call.id))
      .filter((e): e is EnhancedToolExecution => e !== undefined);
    return renderToolsSegment(executions, segIdx);
  };

  // A fold's work rows and a surfaced call's row carry none of a message's
  // chrome (avatar, timestamp, actions): they are part of the turn's work,
  // not a message of their own.
  if (sliceRole === "work" || sliceRole === "surfaced") {
    const rendered = visibleSegments.map(({ segment, segIdx }) => renderSegment(segment, segIdx));
    if (rendered.every((node) => node === null)) return null;
    return (
      <div
        className={cn(
          "space-y-1",
          // A surfaced row stands in the transcript: aligned with prose.
          sliceRole === "surfaced" && "message-content w-full",
          isMobile && "select-text",
        )}
        data-testid={`message-${message.id}-${sliceRole}`}
      >
        {rendered}
      </div>
    );
  }

  const isOptimistic = message.id.startsWith("optimistic-");
  // A send that failed for good: still local-only (no branching from it), but
  // shown at full strength with a "Not sent" line rather than as pending.
  const failedClientMessageId = clientIdOfFailedSend(message.id);
  const fullTimestampText = message.createdAt
    ? new Date(message.createdAt).toLocaleString()
    : "";
  // Mobile-only: the bottom sheet substitute for the actions row. Built from
  // the same handlers the desktop buttons call, so branch/copy behavior never
  // diverges between surfaces — only the affordance to reach them does.
  const mobileMessageActions: MessageActionsSheetAction[] = isMobile
    ? [
        {
          key: "copy",
          label: copied ? "Copied" : "Copy message",
          icon: copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />,
          onSelect: () => void handleCopy(),
        },
        ...(canBranch ? [{
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
        }] : []),
      ]
    : [];

  const copyButton = (
    <Tooltip content={copied ? "Copied" : "Copy message"} side="top" delay={300} triggerClassName="inline-flex">
      <button
        onClick={(e) => {
          e.stopPropagation();
          void handleCopy();
        }}
        aria-label={copied ? "Copied" : "Copy message"}
        className={cn(ACTION_ICON_BUTTON_CLASS, copied && "text-success-ink")}
        type="button"
      >
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </Tooltip>
  );

  const branchButton = branchAvailable && (
    <Tooltip
      content={
        failedClientMessageId
          ? "This message wasn't sent"
          : isOptimistic
            ? "Waiting for message to save"
            : "Branch from this message"
      }
      side="top"
      delay={300}
      triggerClassName="inline-flex"
    >
      <button
        onClick={(e) => {
          e.stopPropagation();
          openBranchMenu(e);
        }}
        disabled={isOptimistic}
        aria-label="Branch from message"
        data-contextual-tip="branch-button"
        className={cn(ACTION_ICON_BUTTON_CLASS, isOptimistic && "cursor-not-allowed opacity-50")}
        type="button"
      >
        <GitBranch className="h-3.5 w-3.5" />
      </button>
    </Tooltip>
  );

  const timestamp = message.createdAt ? (
    <MessageTimestamp at={message.createdAt} className="px-1 text-meta leading-none" />
  ) : null;

  // The pin is a breadcrumb of a message that already exists below; it gets no
  // actions at all. Nor does a prose slice without the chrome: the message's
  // last prose slice carries them. Mobile reaches them through the sheet.
  const hasToolbar = !pinned && hasChrome;
  const showActions = hasToolbar && !isMobile;
  // The turn the composer would reply to keeps its actions on screen, once its
  // answer has finished streaming (never under a streaming answer); older
  // turns reveal theirs on hover or focus. The row is in the flow, so
  // revealing it moves nothing.
  const actionsAtRest = isLatestMessage && !isStreaming;
  const hiddenUntilHover = !actionsAtRest && "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100";
  // One actions row per exchange, under its answer: a row that a later row of
  // the same turn follows carries none.
  const showAssistantActions = showActions && turnEnd && !(isLatestMessage && isStreaming);

  // §2.5: under the answer, copy and branch, then the time.
  const assistantActions = (
    <div
      className={cn(
        "message-actions mt-1 flex h-7 items-center gap-1 whitespace-nowrap text-meta text-muted-foreground transition-opacity duration-150",
        hiddenUntilHover,
      )}
    >
      {copyButton}
      {branchButton}
      {timestamp}
    </div>
  );

  // §2.3: under the bubble, copy then the time.
  const userActions = (
    <div
      className={cn(
        "message-actions flex h-7 items-center gap-1 whitespace-nowrap text-meta text-muted-foreground transition-opacity duration-150",
        hiddenUntilHover,
      )}
    >
      {copyButton}
      {timestamp}
    </div>
  );

  const proseTypeClass = isMobile ? MOBILE_PROSE_TYPE_CLASS : DESKTOP_PROSE_TYPE_CLASS;

  const userTurn = pinned ? (
    // The pinned breadcrumb: one line of the bubble, full width.
    <div className="user-message-content w-full cursor-default rounded-2xl bg-bubble px-3 py-1 text-ui text-foreground">
      {parsed.text && (
        <div className="line-clamp-1 whitespace-pre-wrap break-words">
          {renderTextWithContextPills(parsed.text, chatWorktreeId)}
        </div>
      )}
    </div>
  ) : (
    // A neutral bubble on the right: no border, shadow, tint or avatar.
    <div className="flex flex-col items-end gap-2">
      {hasAttachments && (
        <MessageAttachments attachments={message.attachments} isUser={isUser} className="justify-end" />
      )}
      {parsed.text && (
        <div
          className={cn(
            "user-message-content rounded-2xl bg-bubble px-4 py-2.5 text-foreground",
            // max-w-full on the narrowest column: a cross-axis item of an
            // items-end column sizes to its content, which a long URL makes
            // as wide as the URL; capped, the text wraps inside the bubble.
            isMobile ? "max-w-[90%] select-text" : "max-w-[85%]",
          )}
        >
          <div
            ref={contentRef}
            className={cn(
              proseTypeClass,
              "whitespace-pre-wrap break-words",
              !isMobile && (isExpanded ? USER_MESSAGE_EXPANDED_CLASS : USER_MESSAGE_CLAMP_CLASS),
            )}
          >
            {renderTextWithContextPills(parsed.text, chatWorktreeId)}
          </div>
          {!isMobile && (isOverflowing || isExpanded) && (
            <button
              type="button"
              aria-expanded={isExpanded}
              onClick={() => setIsExpanded((open) => !open)}
              className="mt-1 block text-meta text-muted-foreground hover:text-foreground"
            >
              {isExpanded ? "Show less" : "Show more"}
            </button>
          )}
        </div>
      )}
      {/* A failed send takes the actions row's place, always visible. */}
      {failedClientMessageId && chatId ? (
        <FailedSendStatus chatId={chatId} clientMessageId={failedClientMessageId} />
      ) : (
        showActions && userActions
      )}
    </div>
  );

  return (
    <div
      className={cn(
        "group copy-toast relative min-w-0",
        copied && "copied",
        isOptimistic && !failedClientMessageId && "opacity-60",
      )}
      data-testid={hasChrome ? `message-${message.id}` : `message-${message.id}-prose`}
    >
      {isUser ? (
        userTurn
      ) : (
        // The answer: unboxed prose at the full column width.
        <div className={cn("message-content group/assistant relative w-full", isMobile && "select-text")}>
          {/* Ordered content: text and tool runs interleaved exactly as
              they occurred in the message, so a mid-message tool call
              renders between paragraphs, not after all the prose. */}
          {visibleSegments.map(({ segment, segIdx }) => renderSegment(segment, segIdx))}

          {hasAttachments && hasChrome && (
            <MessageAttachments
              attachments={message.attachments}
              isUser={isUser}
              className={!parsed.text ? "pt-1" : ""}
            />
          )}

          {showAssistantActions && assistantActions}
        </div>
      )}

      {isMobile && hasToolbar && (
        <button type="button" aria-label="Message actions" aria-haspopup="dialog"
          aria-expanded={showActionsSheet}
          onClick={() => setShowActionsSheet(true)}
          className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <MoreHorizontal className="h-4 w-4" />
        </button>
      )}

      {/* Branch options menu and its modals */}
      {branchUi}

      {/* Explicit mobile actions leave native prose selection untouched. */}
      {isMobile && hasToolbar && (
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
      prev.onSelectThread === next.onSelectThread &&
      prev.approvals === next.approvals &&
      prev.turnEnd === next.turnEnd &&
      sameSlice(prev.slice, next.slice)
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
  if (prev.onSelectThread !== next.onSelectThread) return false;
  if (prev.turnEnd !== next.turnEnd) return false;
  if (!sameSlice(prev.slice, next.slice)) return false;

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