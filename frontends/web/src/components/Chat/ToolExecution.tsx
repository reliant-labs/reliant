/**
 * One tool call in the transcript.
 *
 * Almost always a quiet one-line row (QuietRow): the tool's icon, a sans verb
 * and its target in mono — "Read src/cart/totals.ts", "Edited totals.ts
 * +4 −1" — collapsed by default, opened inline to show its input and output.
 * A spinner while it runs, a destructive mark when it failed; no card, no
 * stripe, no tint.
 *
 * Two calls are cards instead, because they are not records of work: a call
 * waiting on the user's approval (one primary action, Approve), and a spawned
 * sub-agent (its name, its state, and its thread).
 */

import Tooltip from "../forge-ui/tooltip";
import { useState, memo, useCallback, useMemo, useEffect } from "react";
import {
  ChevronRight,
  AlertCircle,
  Bot,
  ExternalLink,
  Shield,
  Loader2,
  Square,
  XCircle,
  Play,
  Maximize2,
} from "lucide-react";
import { QuietRow } from "./QuietRow";
import { editDiffStat, toolRowIcon } from "./toolRowParts";
import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";
import { GenericToolRenderer, ToolContentArea, type ToolRenderContext, type ToolResultData } from "./tool-renderers";
import { TruncatedOutputNotice } from "./tool-renderers/TruncatedOutputNotice";
import { ToolBodyError, ToolBodyPlaceholder, ToolResultWithheld } from "./tool-renderers/ToolBodyPending";
import { viewerSafeResult } from "../../lib/viewerResults";
import { useElidedToolBodies } from "./useElidedToolBodies";
import { fetchFullToolResult, useFullToolResult } from "../../hooks/tool-result-queries";
import { useChat, useChatMessages, useStreamingMessages, useToolCallStates } from "../../store/chatStoreHooks";
import {
  useApproveToolRequest,
  useDenyToolRequest,
} from "../../hooks/approval-queries";
import { FileLink } from "./FileLink";
import { formatToolParams, type ToolInput, isViewOnlyTool, isTaskTool, isReadToolWithResults, isSpawnTool } from "../../lib/toolFormatters";
import { formatToolDuration, toolActionVerb, toolDurationMs } from "../../lib/toolAction";
import { DeployedAppCard, deployOfToolCall } from "../Apps/DeployedAppCard";
import { parseFilePath } from "../../lib/filePath";
import { openFile, classifyPath } from "../../lib/fileOpener";
import { toast } from "../../lib/toast-manager";
import { useTasksForChat, type TaskItem } from "../../hooks/task-queries";
import { shouldToolBeCollapsed, TOOL_COLLAPSE_SETTINGS_EVENT } from "../Settings/ToolCallSettings";
import { ApprovalStatus, type Approval } from "../../gen/reliant/v1/approval_pb";
import { ConnectionProposalCard } from "./ConnectionProposalCard";
import { asConnectionProposal, proposalDenial } from "../../lib/connectionProposal";
import {
  ContentBlockType,
  ToolCallStatus,
  WorkflowState,
  WorkflowStopReason,
} from "../../gen/reliant/v1/chat_pb";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";
import { useActiveThreads } from "../../store/threadActivityStore";
import type { ContentBlock, Message, WorkflowExecutionData } from "../../types/chat";

export type { ToolResultData };

/** Component status strings, as accepted by ToolExecutionProps.status. */
type DisplayStatus = NonNullable<ToolExecutionProps["status"]>;

type ToolCallInput = Record<string, unknown> | string | undefined;

function baseToolName(rawName: string): string {
  const lower = rawName.toLowerCase();
  return lower.startsWith("mcp__") ? lower.split("__").pop() || lower : lower;
}

function inputObject(input: ToolCallInput): Record<string, unknown> | null {
  return typeof input === "object" && input !== null ? input : null;
}

function stringField(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function isGenericSpawnLabel(value: string): boolean {
  const normalized = value.trim().toLowerCase();
  return normalized === "agent" || normalized === "builtin://agent" || normalized === "spawn_tool";
}

function nonGenericStringField(value: unknown): string | undefined {
  const str = stringField(value);
  if (!str || isGenericSpawnLabel(str)) return undefined;
  return str;
}

function parseToolInput(raw?: string): Record<string, unknown> | string | undefined {
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return raw;
  }
}

function walkWorkflowExecutions(
  workflows: WorkflowExecutionData[],
  visit: (workflow: WorkflowExecutionData) => boolean,
): WorkflowExecutionData | undefined {
  for (const workflow of workflows) {
    if (visit(workflow)) return workflow;
    const found = walkWorkflowExecutions(workflow.children, visit);
    if (found) return found;
  }
  return undefined;
}

function workflowTitle(workflow: WorkflowExecutionData | undefined): string | undefined {
  if (!workflow) return undefined;
  return nonGenericStringField(workflow.threadTitle) || nonGenericStringField(workflow.spawnedByNodeId) || nonGenericStringField(workflow.workflowName);
}

function messageSeq(message: Message): number {
  const seq = (message as { seq?: unknown }).seq;
  if (typeof seq === "bigint") return Number(seq);
  if (typeof seq === "number") return seq;
  return 0;
}

function collectMessagesInRenderOrder(messages: Message[]): Message[] {
  return messages
    .slice()
    .sort((a, b) => {
      const seqA = messageSeq(a);
      const seqB = messageSeq(b);
      if (seqA !== seqB) return seqA - seqB;
      return new Date(a.createdAt || "").getTime() - new Date(b.createdAt || "").getTime();
    });
}

function toolResultCallIdsContaining(messages: Message[], needle: string): Set<string> {
  const ids = new Set<string>();
  for (const message of messages) {
    for (const block of message.contentBlocks || []) {
      if (
        block.type === ContentBlockType.TOOL_RESULT &&
        block.toolCallId &&
        block.content?.includes(needle)
      ) {
        ids.add(block.toolCallId);
      }
    }
  }
  return ids;
}

function findShellCallForProcess(messages: Message[], processId: string): ContentBlock | undefined {
  const resultCallIds = toolResultCallIdsContaining(messages, processId);
  let lastMatch: ContentBlock | undefined;
  for (const message of collectMessagesInRenderOrder(messages)) {
    const blocks = (message.contentBlocks || [])
      .slice()
      .sort((a, b) => (a.index ?? 0) - (b.index ?? 0));
    for (const block of blocks) {
      if (block.type !== ContentBlockType.TOOL_CALL || baseToolName(block.toolName || "") !== "shell") {
        continue;
      }
      if (block.toolCallId && resultCallIds.has(block.toolCallId)) {
        lastMatch = block;
      }
    }
  }
  return lastMatch;
}

function findSpawnCallForAgent(messages: Message[], agentId: string): ContentBlock | undefined {
  const resultCallIds = toolResultCallIdsContaining(messages, agentId);
  let fallback: ContentBlock | undefined;
  for (const message of collectMessagesInRenderOrder(messages)) {
    const blocks = (message.contentBlocks || [])
      .slice()
      .sort((a, b) => (a.index ?? 0) - (b.index ?? 0));
    for (const block of blocks) {
      if (block.type !== ContentBlockType.TOOL_CALL || baseToolName(block.toolName || "") !== "spawn") {
        continue;
      }
      if (block.childWorkflowId === agentId) {
        fallback = block;
        continue;
      }
      if (block.toolCallId && resultCallIds.has(block.toolCallId)) {
        fallback = block;
      }
    }
  }
  return fallback;
}

function spawnTitleFromInput(input: ToolCallInput): string | undefined {
  const obj = inputObject(input);
  if (!obj) return undefined;
  return nonGenericStringField(obj.title) || nonGenericStringField(obj.preset);
}

/** Maps the durable proto status onto the component's display status vocabulary. */
export function durableStatusToDisplayStatus(status: ToolCallStatus | undefined): DisplayStatus | undefined {
  switch (status) {
    case ToolCallStatus.PENDING:
      return "pending";
    case ToolCallStatus.EXECUTING:
      return "executing";
    case ToolCallStatus.COMPLETED:
      return "completed";
    case ToolCallStatus.FAILED:
      return "failed";
    case ToolCallStatus.CANCELLED:
      return "cancelled";
    case ToolCallStatus.BACKGROUNDED:
      return "backgrounded";
    default:
      return undefined;
  }
}

/**
 * Maps a spawned agent's workflow status onto the display status vocabulary.
 * Async spawn returns the tool result the moment the child is dispatched, so
 * for a spawn call the child workflow — not the tool call — is the authority
 * on whether the agent is still running.
 */
export function workflowLifecycleToDisplayStatus(
  state: WorkflowState | undefined,
  stopReason: WorkflowStopReason | undefined,
): DisplayStatus | undefined {
  if (state === WorkflowState.PENDING || state === WorkflowState.ACTIVE) {
    return "executing";
  }
  if (state !== WorkflowState.STOPPED) {
    return undefined;
  }

  switch (stopReason) {
    case WorkflowStopReason.COMPLETED:
      return "completed";
    case WorkflowStopReason.FAILED:
      return "failed";
    case WorkflowStopReason.CANCELLED:
      return "cancelled";
    case WorkflowStopReason.PAUSED:
      return "executing";
    default:
      return undefined;
  }
}

interface ToolExecutionProps {
  toolCall: {
    id: string;
    name: string;
    /** Undefined while the call is still streaming and its input has not arrived. */
    input?: Record<string, unknown> | string;
    finished?: boolean;
    content_block_id?: string;
    /** Durable status from the tool_calls table, as of the last message load. */
    durableStatus?: ToolCallStatus;
    /** When the server recorded the call starting and finishing (its row in tool_calls). */
    durableStartedAt?: string;
    durableCompletedAt?: string;
    /**
     * For a spawn call, the workflow it started (tool_calls.child_workflow_id).
     * A spawned sub-agent's thread id equals its workflow id, so this IS the
     * thread the call owns.
     */
    childWorkflowId?: string;
    /**
     * `input` is a skeleton: the transcript left the input out (a stub
     * read). It labels the row; the open row fetches the body.
     */
    inputElided?: boolean;
    /** Byte length of the stored input, when it was elided. */
    inputSize?: number;
  };
  toolResult?: ToolResultData;
  onApprove?: (id: string) => void;
  onDeny?: (id: string) => void;
  onCancel?: (id: string) => void;
  onConvertToBackground?: (id: string) => void;
  status?:
    | "pending"
    | "preparing"
    | "requested"
    | "writing_input"
    | "executing"
    | "cancelling"
    | "cancelled"
    | "completed"
    | "backgrounded"
    | "denied"
    | "failed";
  approval?: Approval;
  chatId?: string;
  showRichContent?: boolean;
  onSelectThread?: (threadId: string | null) => void;
}


function ToolExecutionComponent({
  toolCall,
  toolResult: sentResult,
  onApprove,
  onDeny,
  onCancel,
  onConvertToBackground,
  status,
  approval,
  chatId,
  showRichContent = false,
  onSelectThread,
}: ToolExecutionProps) {
  // A viewer of another member's shared chat never sees a tool's result,
  // however it arrived (the server elides them; this does not rely on it):
  // everything below reads the result through this.
  const currentChat = useChat(chatId || "");
  const isViewer = !!currentChat?.viewerAccess;
  const toolResult = useMemo(
    () => viewerSafeResult(sentResult, toolCall.name, isViewer),
    [sentResult, toolCall.name, isViewer],
  );
  const toolNameLower = (toolCall.name || '').toLowerCase();
  const toolBaseName = baseToolName(toolCall.name || '');
  const isViewOnlyToolFlag = isViewOnlyTool(toolNameLower);
  const isTaskToolFlag = isTaskTool(toolNameLower);
  const isReadToolFlag = isReadToolWithResults(toolNameLower);

  // Determine initial expanded state based on user settings
  // shouldToolBeCollapsed returns true if collapsed, so we invert it for isExpanded
  const isSpawnToolFlag = isSpawnTool(toolNameLower);
  // Narrow surfaces (mobile/embed) collapse every category by default — a full
  // diff is unreadable in a phone viewport and buries the conversation.
  const surface = useSurface();
  const [isExpanded, setIsExpanded] = useState(() => !shouldToolBeCollapsed(toolCall.name, surface));
  
  // Track if user has manually toggled - prevents auto-expand from overriding user choice
  const [userHasToggled, setUserHasToggled] = useState(false);

  // Re-apply the default when the preference changes in Settings. Rows the user
  // has already opened or closed by hand keep their state — an explicit choice
  // outranks the default.
  useEffect(() => {
    const applyNewDefault = () => {
      if (userHasToggled) return;
      setIsExpanded(!shouldToolBeCollapsed(toolCall.name, surface));
    };
    window.addEventListener(TOOL_COLLAPSE_SETTINGS_EVENT, applyNewDefault);
    return () => window.removeEventListener(TOOL_COLLAPSE_SETTINGS_EVENT, applyNewDefault);
  }, [toolCall.name, userHasToggled, surface]);

  // Auto-expand read tools when results arrive (only if user hasn't manually collapsed)
  // and only if the setting allows it (not collapsed by default)
  useEffect(() => {
    if (isReadToolFlag && toolResult?.content && !isExpanded && !userHasToggled) {
      // Only auto-expand if settings don't collapse this tool by default
      if (!shouldToolBeCollapsed(toolCall.name, surface)) {
        setIsExpanded(true);
      }
    }
  }, [isReadToolFlag, toolResult?.content, isExpanded, userHasToggled, toolCall.name, surface]);

  // The bodies a stub transcript read left out. Everything above the content
  // (the header, the collapsed row, the deploy card) reads `toolCall.input`
  // and `toolResult` as sent: a skeleton labels the call. The content area
  // renders `bodies.input` / `bodies.result`, and only once `bodies.ready`
  // (useElidedToolBodies) — the single place a skeleton could be mistaken
  // for an input.
  const needsApprovalNow = approval?.status === ApprovalStatus.PENDING;
  const bodies = useElidedToolBodies({
    chatId,
    toolName: toolCall.name,
    call: toolCall,
    result: toolResult,
    shown: isExpanded || (needsApprovalNow && showRichContent),
    expanded: isExpanded,
    viewer: isViewer,
  });
  const contentResult = bodies.result;
  const toggleExpanded = () => {
    if (!isExpanded) bodies.noteUserExpand();
    setIsExpanded(!isExpanded);
  };

  // A large result arrives as a preview (truncated). The full output is
  // fetched when the user asks for it, and then rendered in the preview's
  // place by the same renderer; a copy fetches it too (loadFullResult).
  const isTruncatedResult = !!contentResult?.truncated && !!chatId;
  const elidedResultBlockId = toolResult?.elided ? toolResult.block_id : undefined;
  const [wantFullResult, setWantFullResult] = useState(false);
  const fullResult = useFullToolResult(chatId, toolCall.id, isTruncatedResult && wantFullResult, elidedResultBlockId);
  const fullResultContent = isTruncatedResult ? fullResult.data?.content : undefined;
  const displayResult = useMemo<ToolResultData | undefined>(
    () =>
      contentResult && fullResultContent !== undefined
        ? { ...contentResult, content: fullResultContent, truncated: false }
        : contentResult,
    [contentResult, fullResultContent],
  );
  const loadFullResult = useMemo(
    () =>
      isTruncatedResult && chatId
        ? () => fetchFullToolResult(chatId, toolCall.id, elidedResultBlockId)
        : undefined,
    [isTruncatedResult, chatId, toolCall.id, elidedResultBlockId],
  );
  const showTruncatedNotice = isTruncatedResult && fullResultContent === undefined;

  const chatWorktreeId = currentChat?.worktreeId;
  const chatMessages = useChatMessages(chatId || undefined);
  const streamingMessages = useStreamingMessages(chatId || "");
  const allDisplayMessages = useMemo(
    () => [...chatMessages, ...streamingMessages],
    [chatMessages, streamingMessages],
  );
  const activeThreads = useActiveThreads(chatId || "");

  // Subscribe to live tool call state. Keyed by the LLM tool-call id, which is
  // what the backend addresses tool status by — it is the only id that exists
  // both while the call streams and after its message is persisted.
  const toolCallStates = useToolCallStates(chatId || "");
  const liveToolCallState = toolCallStates.get(toolCall.id) || null;

  const approveMutation = useApproveToolRequest(chatId || undefined);
  const denyMutation = useDenyToolRequest(chatId || undefined);

  // Async spawn returns the tool result the moment the child agent is
  // dispatched, so the tool call's own status reads "completed" while the
  // agent it started keeps running for minutes. For a spawn call, the child
  // workflow is the authority on whether the agent is still working.
  const spawnChildWorkflowId = isSpawnToolFlag ? toolCall.childWorkflowId : undefined;
  const { allWorkflows } = useWorkflowExecutions(chatId || null);
  const spawnChildWorkflow = useMemo(
    () => (spawnChildWorkflowId ? findWorkflowById(allWorkflows, spawnChildWorkflowId) : undefined),
    [allWorkflows, spawnChildWorkflowId],
  );

  // Determine current status
  const liveStatus = liveToolCallState?.status || status;
  const isInputPreparing = toolCall.input === undefined;
  const isDenied = approval?.status === ApprovalStatus.DENIED;

  // Derive display status. For a spawn call that has dispatched (has a
  // childWorkflowId), the child workflow is the sole authority: the spawn
  // tool call's own live/durable status reflects the DISPATCH, which
  // completes in milliseconds under async spawn, while the agent it started
  // keeps running for minutes. Checking that status first, before the child
  // workflow status, would show a finished spawn next to a running agent —
  // exactly the bug this branch exists to avoid. Before dispatch (no
  // childWorkflowId yet), fall through to the normal live/durable/inferred
  // flow so preparing/requested/denied states on the spawn call itself still
  // show.
  const currentStatus = (() => {
    if (spawnChildWorkflowId) {
      // Default to "executing" while the child workflow row doesn't exist
      // yet (the spawn was just dispatched) or reports an unmapped status.
      return workflowLifecycleToDisplayStatus(
        spawnChildWorkflow?.state,
        spawnChildWorkflow?.stopReason,
      ) ?? "executing";
    }

    if (liveStatus) {
      return liveStatus;
    }

    const durable = durableStatusToDisplayStatus(toolCall.durableStatus);
    if (durable) {
      return durable;
    }

    // No live or durable status, compute from tool state
    if (isDenied) return "denied";
    if (toolResult) return "completed";

    if (isInputPreparing) return "preparing";
    if (!toolCall.finished) return "pending";
    return "executing";
  })();

  const isPreparing = currentStatus === "preparing" || currentStatus === "writing_input";
  const isRequested = currentStatus === "requested";
  const isExecuting = currentStatus === "executing";
  const isCancelling = currentStatus === "cancelling";
  const isCancelled = currentStatus === "cancelled";
  const isBackgrounded = currentStatus === "backgrounded";
  // For a spawn call, the tool result (the dispatch handle, under async) says
  // nothing about whether the agent it started is done — only the child
  // workflow's status does. Skip the toolResult fallback in that case so a
  // still-running spawn cannot read as completed just because it has a
  // result.
  const isCompleted = currentStatus === "completed" || (!spawnChildWorkflowId && !!toolResult);
  const hasFailed = currentStatus === "failed" || (!spawnChildWorkflowId && (toolResult?.is_error ?? false));
  // A permission block is an error result, but it is a policy stop, not a
  // failure of the call: it keeps the warning treatment.
  const isBlocked = !!toolResult?.is_error && !!toolResult.content?.includes("blocked");
  // How long a finished call took, from the times the server recorded. A
  // spawn's own call is only its dispatch, so it shows none.
  const durationMs =
    (isCompleted || hasFailed || isCancelled) && !spawnChildWorkflowId
      ? toolDurationMs(toolCall.durableStartedAt, toolCall.durableCompletedAt)
      : null;
  const durationLabel = durationMs !== null ? formatToolDuration(durationMs) : null;
  const actionVerb = toolActionVerb(toolCall.name, isCompleted || hasFailed || isCancelled ? "done" : "live", toolCall.input);
  // A shell call that deployed a forge app links to what it deployed
  // (Apps/DeployedAppCard).
  const shellDeploy = useMemo(
    () => deployOfToolCall(toolNameLower, toolCall.input, toolResult),
    [toolNameLower, toolCall.input, toolResult]
  );

  // Approval logic - if backend created a pending approval, we need to show UI for it.
  const needsApproval = approval?.status === ApprovalStatus.PENDING;
  const shouldShowApprovalUI = needsApproval;

  // Task tool state
  const { data: chatTasks } = useTasksForChat(isTaskToolFlag ? chatId : null);
  const tasksById = useMemo(() => {
    if (!chatTasks) return undefined;
    const map: Record<string, TaskItem> = {};
    for (const t of chatTasks) map[t.id] = t;
    return map;
  }, [chatTasks]);

  const taskIdForStore = useMemo(() => {
    if (toolNameLower !== 'update_task' || !chatId) return null;
    const input = toolCall.input as Record<string, unknown> | undefined;
    return (input?.task_id as string) || null;
  }, [toolNameLower, chatId, toolCall.input]);

  const storedTask = taskIdForStore && tasksById ? tasksById[taskIdForStore] : null;

  const taskTitle = useMemo(() => {
    if (!isTaskToolFlag || !chatId) return null;
    const input = toolCall.input as Record<string, unknown> | undefined;
    if (!input) return null;
    
    if (toolNameLower === 'add_task') {
      return (input.title as string) || null;
    }
    
    const taskId = input.task_id as string | undefined;
    if (!taskId) return null;
    
    return tasksById?.[taskId]?.title || null;
  }, [isTaskToolFlag, chatId, toolCall.input, toolNameLower, tasksById]);

  const taskTargetStatus = useMemo(() => {
    if (toolNameLower !== 'update_task') return null;
    const input = toolCall.input as Record<string, unknown> | undefined;
    return (input?.status as string) || null;
  }, [toolNameLower, toolCall.input]);

  const taskDescription = useMemo(() => {
    if (!isTaskToolFlag) return null;
    const input = toolCall.input as Record<string, unknown> | undefined;
    if (!input) return null;
    
    const paramDescription = input.description as string | undefined;
    const paramNotes = (input.metadata as Record<string, unknown>)?.notes as string | undefined;
    
    if (toolNameLower === 'update_task' && storedTask) {
      return paramDescription || paramNotes || storedTask.description || null;
    }
    
    return paramDescription || paramNotes || null;
  }, [isTaskToolFlag, toolCall.input, toolNameLower, storedTask]);

  // What a wait or a message to an agent is about, resolved from the call it
  // names: an overlay of fields on its input, for the header (the skeleton)
  // and the content (the body) alike. The keys it reads (process_id,
  // agent_id) are kept whole in a skeleton.
  const resolvedWaitTarget = useMemo(() => {
    const none = { overlay: undefined as Record<string, unknown> | undefined, threadId: undefined as string | undefined };
    const input = inputObject(toolCall.input);
    if (!input) return none;

    if (toolBaseName === "shell_wait") {
      const processId = stringField(input.process_id);
      if (!processId) return none;
      const shellCall = findShellCallForProcess(allDisplayMessages, processId);
      const shellInput = parseToolInput(shellCall?.input);
      const shellObj = inputObject(shellInput);
      const description = stringField(shellObj?.description) || stringField(shellObj?.Description);
      const command = stringField(shellObj?.command) || stringField(shellObj?.Command);
      return {
        overlay: description || command ? { description, command } : undefined,
        threadId: undefined,
      };
    }

    // spawn_status and spawn_send both name an agent by thread id: resolve
    // its title for the row and make the thread openable.
    if (toolBaseName === "spawn_status" || toolBaseName === "spawn_send") {
      const agentId = stringField(input.agent_id);
      if (!agentId) return none;

      const activeThread = activeThreads.find((thread) => thread.thread === agentId || thread.workflow_id === agentId);
      const workflow = findWorkflowById(allWorkflows, agentId) ||
        walkWorkflowExecutions(allWorkflows, (candidate) => candidate.thread === agentId);
      const spawnCall = findSpawnCallForAgent(allDisplayMessages, agentId);
      const spawnInput = parseToolInput(spawnCall?.input);
      const title =
        nonGenericStringField(activeThread?.thread_title) ||
        nonGenericStringField(activeThread?.title) ||
        workflowTitle(workflow) ||
        spawnTitleFromInput(spawnInput) ||
        nonGenericStringField(activeThread?.agent_name);

      return {
        overlay: title ? { title } : undefined,
        threadId: agentId,
      };
    }

    return none;
  }, [toolCall.input, toolBaseName, allDisplayMessages, activeThreads, allWorkflows]);
  const waitOverlay = resolvedWaitTarget.overlay;
  const withWaitOverlay = useCallback(
    (input: ToolCallInput): ToolCallInput => {
      const obj = inputObject(input);
      return obj && waitOverlay ? { ...obj, ...waitOverlay } : input;
    },
    [waitOverlay],
  );
  const headerInput = useMemo(() => withWaitOverlay(toolCall.input), [withWaitOverlay, toolCall.input]);
  const contentInput = useMemo(() => withWaitOverlay(bodies.input), [withWaitOverlay, bodies.input]);

  // The thread this tool can open, as recorded by the code that created it or
  // directly named by spawn_status/spawn_send. For spawn calls,
  // tool_calls.child_workflow_id is the child thread. For spawn_status and
  // spawn_send, agent_id already IS that thread id.
  const spawnThreadId = isSpawnToolFlag ? toolCall.childWorkflowId : resolvedWaitTarget.threadId;

  // Handlers
  const handleApprove = useCallback(() => {
    if (approval && chatId) {
      approveMutation.mutate({ requestId: approval.id });
    } else if (onApprove) {
      onApprove(toolCall.id);
    }
  }, [approval, chatId, onApprove, toolCall.id, approveMutation]);

  const handleDeny = useCallback(() => {
    if (approval && chatId) {
      denyMutation.mutate({ requestId: approval.id });
    } else if (onDeny) {
      onDeny(toolCall.id);
    }
  }, [approval, chatId, onDeny, toolCall.id, denyMutation]);

  // Extract file paths for view tools
  // `input` is optional on ToolCallData: it is undefined while the call is
  // still streaming and its arguments have not arrived. The body already
  // returns [] for anything that is not an object, so undefined needs no new
  // branch — only the signature has to admit it.
  const extractFilePaths = (input: Record<string, unknown> | string | undefined): string[] => {
    if (typeof input !== "object" || input === null) return [];
    const filePaths: string[] = [];
    if (input.file_path && typeof input.file_path === "string") {
      filePaths.push(input.file_path);
    } else if (input.edits && Array.isArray(input.edits)) {
      input.edits.forEach((edit) => {
        const editObj = edit as Record<string, unknown>;
        if (editObj?.file_path && typeof editObj.file_path === "string") {
          filePaths.push(editObj.file_path);
        }
      });
    }
    return filePaths;
  };

  // The row's line: a sans verb, then the target in mono (a path, a command,
  // a pattern). A tool with no plain-words verb leads with its name.
  const formatToolCallDisplay = (rawName: string, input: Record<string, unknown> | string | undefined): React.ReactNode => {
    // Strip MCP prefix for display: mcp__reliant__spawn -> spawn
    const name = rawName.startsWith('mcp__')
      ? rawName.split('__').pop() || rawName
      : rawName;
    const verb = actionVerb;
    const lead = <span className="shrink-0" title={verb ? name : undefined}>{verb ?? name}</span>;

    // The arguments have not streamed in yet. Formatting nothing yields an
    // empty argument list, which reads as "called with no arguments" rather
    // than "still arriving" — so name the state instead of faking the shape.
    if (input === undefined) {
      return (
        <>
          {lead}
          <span className="italic">preparing…</span>
        </>
      );
    }

    const formatted = formatToolParams(rawName, input as ToolInput, { inputSize: toolCall.inputSize });
    if (formatted) {
      const filePaths = formatted.structured?.filePaths ?? [];
      if (filePaths.length > 0) {
        return (
          <>
            {lead}
            <span className="min-w-0 truncate">
              {filePaths.slice(0, 3).map((fp, idx) => (
                <span key={`${fp}-${idx}`}>
                  <FileLink path={fp} inline prose showIcon={false} worktreeId={chatWorktreeId} className="font-mono text-code">
                    {isViewOnlyToolFlag && filePaths.length === 1 ? formatted.summary : undefined}
                  </FileLink>
                  {idx < Math.min(filePaths.length - 1, 2) && <span>, </span>}
                </span>
              ))}
              {filePaths.length > 3 && <span> +{filePaths.length - 3} more</span>}
            </span>
          </>
        );
      }
      // A shell call's model-written description is already an action
      // ("Run cart tests"): it leads on its own, in sans, without a second verb.
      if (verb && typeof formatted.structured?.description === "string") {
        return <span className="truncate" title={name}>{formatted.summary}</span>;
      }
      return (
        <>
          {lead}
          {formatted.summary && <span className="min-w-0 truncate font-mono text-code">{formatted.summary}</span>}
        </>
      );
    }

    return lead;
  };

  // Get status text: what a state is called, for the row and for assistive tech.
  const getStatusText = () => {
    if (isCancelled) return "Cancelled";
    if (isCancelling) return "Cancelling...";
    if (toolResult?.is_error) {
      if (toolResult.content?.includes("blocked")) return "Blocked";
      // An error result is a failure (a test run that failed, a command that
      // exited non-zero), not a warning.
      return "Failed";
    }
    // A spawned agent whose run failed stopped on an error: a message to it
    // resumes it where it stopped, so it is not shown as a dead end.
    if (hasFailed) return spawnChildWorkflowId ? "Stopped" : "Failed";
    if (isBackgrounded) return "Background";
    if (isCompleted) {
      if (isTaskToolFlag && taskTargetStatus) {
        const words = taskTargetStatus.split('_').join(' ');
        return words.charAt(0).toUpperCase() + words.slice(1);
      }
      return "Completed";
    }
    if (needsApproval) return "Awaiting approval";
    if (isDenied) return "Denied";
    if (isExecuting) return "Executing...";
    if (isPreparing) return "Preparing...";
    if (isRequested) return "Requested";
    return "Pending";
  };

  // The row's leading mark: the tool's icon at rest and when done; a spinner
  // while it runs; a destructive mark when it failed.
  const ToolIcon = toolRowIcon(toolCall.name);
  const isLive = isExecuting || isPreparing || isCancelling;
  const statusMark = (() => {
    if (isBlocked) return <Shield className="size-4 text-warning-ink" />;
    if (hasFailed) {
      return spawnChildWorkflowId
        ? <AlertCircle className="size-4 text-warning-ink" />
        : <XCircle className="size-4 text-destructive-ink" />;
    }
    if (isLive) return <Loader2 className={cn("size-3.5 animate-spin", isExecuting ? "text-primary-ink" : "text-muted-foreground")} />;
    if (isBackgrounded) return <Play className="size-3.5 text-primary-ink" />;
    return <ToolIcon className="size-4" />;
  })();

  // Success and live work need no word — the icon and the spinner carry them —
  // so a long run is not a column of identical labels. Every other state is
  // named, quietly, after the line.
  const statusText = getStatusText();
  const quietStatus = (isCompleted && !hasFailed) || isLive;
  const statusWord = (
    <span
      className={cn(
        quietStatus ? "sr-only" : "text-meta",
        !quietStatus && (hasFailed && !isBlocked && !spawnChildWorkflowId ? "text-destructive-ink" : "text-muted-foreground"),
      )}
    >
      {statusText}
    </span>
  );

  // Determine if expandable. Even compact/read-only/view-only tools need an inspector for input/output.
  const hasInput = toolCall.input !== undefined;
  const hasResult = !!toolResult;
  const hasContent = hasInput || hasResult;
  const isExpandable = hasContent && (!isTaskToolFlag || !!taskDescription || hasResult);
  const hasFileOpenAffordance = isViewOnlyToolFlag && extractFilePaths(toolCall.input).length > 0;
  const diffStat = useMemo(
    () => editDiffStat(toolCall.name, toolCall.input, toolCall.inputElided),
    [toolCall.name, toolCall.input, toolCall.inputElided],
  );

  // Build render context for content area
  const renderContext: ToolRenderContext = {
    toolName: toolCall.name,
    toolCallId: toolCall.id,
    childWorkflowId: spawnThreadId,
    // Normalize the streaming-window undefined to {} HERE, once, rather than
    // in every renderer: ToolCallData.input is absent until the call's
    // arguments arrive, but a renderer's contract is that `input` is present.
    // The body, never the skeleton: content renders only once bodies.ready.
    input: contentInput ?? {},
    result: displayResult,
    loadFullResult,
    worktreeId: chatWorktreeId,
    chatId,
    isExpanded,
    isCompleted,
    isExecuting,
    isPreparing,
    hasFailed,
    onSelectThread,
  };

  // The content area's body: the renderer once every elided body is here,
  // else a placeholder sized from the body, or the error with Retry.
  const renderToolContent = (content: React.ReactNode): React.ReactNode =>
    bodies.ready ? (
      <>
        {content}
        {bodies.withheld && <ToolResultWithheld />}
      </>
    ) : bodies.failed ? (
      <ToolBodyError onRetry={bodies.retry} />
    ) : (
      <ToolBodyPlaceholder bytes={bodies.bytes} />
    );

  const openPrimaryFile = (e: React.MouseEvent) => {
    e.stopPropagation();
    const filePaths = extractFilePaths(toolCall.input);
    if (filePaths.length === 0) return;

    const parsed = parseFilePath(filePaths[0]);
    if (!parsed) return;

    // Check if path is external before opening
    const classification = classifyPath(parsed, chatWorktreeId);
    if (!classification.isClickable) {
      toast.error(classification.tooltipMessage);
      return;
    }

    if (typeof toolCall.input === 'object') {
      const input = toolCall.input as Record<string, unknown>;
      if (typeof input.offset === 'number' && input.offset > 0) {
        parsed.line = input.offset;
        if (typeof input.limit === 'number' && input.limit > 1) {
          parsed.lineEnd = input.offset + input.limit - 1;
        }
      }
    }

    // Use targetWorktreeId from classification for correct worktree context
    openFile(parsed, classification.targetWorktreeId || chatWorktreeId);
  };

  const onToggleRow = () => {
    setUserHasToggled(true);
    toggleExpanded();
  };

  // The open row's detail: the renderer's view of the input and output, with
  // the full-output affordance for a preview.
  const detail = (
    <div className="max-h-[600px] overflow-y-auto overflow-x-hidden">
      {isTaskToolFlag && taskDescription && (
        <p className="pb-2 text-ui text-muted-foreground">{taskDescription}</p>
      )}
      {renderToolContent(
        isTaskToolFlag ? (
          <GenericToolRenderer ctx={renderContext} />
        ) : (
          <>
            <ToolContentArea ctx={renderContext} />
            {showTruncatedNotice && (
              <TruncatedOutputNotice
                originalSize={contentResult?.original_size}
                isLoading={fullResult.isFetching}
                error={fullResult.error ?? undefined}
                onShowFull={() => {
                  if (wantFullResult) void fullResult.refetch();
                  else setWantFullResult(true);
                }}
              />
            )}
          </>
        ),
      )}
    </div>
  );

  const canOpenThread =
    (isSpawnToolFlag || toolBaseName === "spawn_status" || toolBaseName === "spawn_send") && !!spawnThreadId && !!onSelectThread;

  // Hover-revealed on a pointer; always there on touch, which has no hover.
  const ROW_ACTION_CLASS =
    "touch-target inline-flex size-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
  const revealOnHover = "opacity-0 group-hover/row:opacity-100 group-focus-within/row:opacity-100 touch:opacity-100";

  const rowTrailing = (
    <>
      {diffStat && (
        <span className="text-meta tabular-nums" aria-label={`${diffStat.added} lines added, ${diffStat.removed} removed`}>
          <span className="text-success-ink">+{diffStat.added}</span>{" "}
          <span className="text-destructive-ink">−{diffStat.removed}</span>
        </span>
      )}
      {statusWord}
      {durationLabel && (
        <span className={cn("text-meta tabular-nums text-muted-foreground", revealOnHover)} title={`Took ${durationLabel}`}>
          {durationLabel}
        </span>
      )}
      {hasFileOpenAffordance && (
        <Tooltip content="Open file" side="bottom" delay={300} triggerClassName="inline-flex">
          <button onClick={openPrimaryFile} className={cn(ROW_ACTION_CLASS, revealOnHover)} aria-label="Open file" type="button">
            <ExternalLink className="size-3.5" />
          </button>
        </Tooltip>
      )}
      {canOpenThread && !isSpawnToolFlag && (
        <Tooltip content="Open full thread view" side="bottom" delay={300} triggerClassName="inline-flex">
          <button
            onClick={(e) => { e.stopPropagation(); onSelectThread?.(spawnThreadId ?? null); }}
            className={cn(ROW_ACTION_CLASS, revealOnHover)}
            aria-label="Open full thread view"
            type="button"
          >
            <Maximize2 className="size-3.5" />
          </button>
        </Tooltip>
      )}
      {isExecuting && !isCancelling && !isSpawnToolFlag && onConvertToBackground && (
        <Tooltip content="Push to background" side="bottom" delay={300} triggerClassName="inline-flex">
          <button
            onClick={(e) => { e.stopPropagation(); onConvertToBackground(toolCall.id); }}
            className={ROW_ACTION_CLASS}
            aria-label="Push tool execution to background"
            type="button"
          >
            <Play className="size-3.5" />
          </button>
        </Tooltip>
      )}
      {(isExecuting || isCancelling) && onCancel && (
        <Tooltip content="Cancel" side="bottom" delay={300} triggerClassName="inline-flex">
          <button
            onClick={(e) => { e.stopPropagation(); onCancel(toolCall.id); }}
            className={ROW_ACTION_CLASS}
            aria-label="Cancel tool execution"
            disabled={isCancelling}
            type="button"
          >
            <Square className={cn("size-3", isCancelling && "animate-pulse")} />
          </button>
        </Tooltip>
      )}
    </>
  );

  const rowLine = isTaskToolFlag ? (
    <>
      <span className="shrink-0">{toolNameLower === "add_task" ? "Task" : "Updated task"}</span>
      <span className="min-w-0 truncate text-foreground/80">
        {taskTitle || (toolNameLower === 'add_task' ? 'Adding task...' : 'Updating task...')}
      </span>
    </>
  ) : (
    formatToolCallDisplay(toolCall.name, headerInput)
  );

  // A call waiting on the user is a decision, not a record of work: it is one
  // of the transcript's few cards, with its one primary action.
  if (shouldShowApprovalUI && approval) {
    const proposal = chatId ? asConnectionProposal(approval) : null;
    return (
      <div className="rounded-xl border border-border p-4 sm:p-5" data-testid="tool-approval-card">
        <div className="flex items-center gap-2 text-ui font-medium text-foreground">
          <Shield className="size-4 shrink-0 text-warning-ink" aria-hidden="true" />
          <span>Approval required</span>
        </div>
        <div className="mt-1 flex min-w-0 items-center gap-2 text-ui text-muted-foreground">
          {rowLine}
        </div>
        {proposal ? (
          <div className="mt-4">
            <ConnectionProposalCard
              approval={proposal}
              onConnected={(connectionId) => approveMutation.mutate({ requestId: approval.id, connectionId })}
              onDecline={(notCompleted) => denyMutation.mutate(proposalDenial(approval.id, notCompleted))}
            />
          </div>
        ) : (
          <>
            {showRichContent && toolCall.input && (
              <div className="mt-3 max-h-[320px] overflow-y-auto overflow-x-hidden rounded-lg border border-border-subtle">
                {renderToolContent(<ToolContentArea ctx={renderContext} />)}
              </div>
            )}
            <div className={cn("mt-4 flex gap-2", surface !== "desktop" && "flex-col")}>
              <button
                type="button"
                onClick={handleApprove}
                aria-label="Approve tool execution"
                className={cn(
                  "inline-flex items-center justify-center gap-1.5 rounded-lg bg-primary px-4 text-ui font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
                  surface === "desktop" ? "h-9" : "min-h-[44px]",
                )}
              >
                Approve
              </button>
              <button
                type="button"
                onClick={handleDeny}
                aria-label="Deny tool execution"
                className={cn(
                  "inline-flex items-center justify-center gap-1.5 rounded-lg px-4 text-ui font-medium text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  surface === "desktop" ? "h-9" : "min-h-[44px]",
                )}
              >
                Deny
              </button>
            </div>
          </>
        )}
      </div>
    );
  }

  // A sub-agent is the third card: who it is, whether it is still working,
  // and the one way in — its thread. Its live progress opens beneath.
  if (isSpawnToolFlag && !isInputPreparing) {
    const formatted = formatToolParams(toolCall.name, headerInput as ToolInput);
    const agentTitle = spawnTitleFromInput(headerInput) || formatted?.summary || "Sub-agent";
    const agentState = hasFailed
      ? { label: statusText === "Failed" ? "Failed" : "Stopped", dot: "bg-warning" }
      : isCancelled
        ? { label: "Cancelled", dot: "bg-muted-foreground" }
        : isCompleted
          ? { label: "Done", dot: "bg-success" }
          : { label: "Working", dot: "bg-primary motion-safe:animate-pulse" };
    return (
      <div className="rounded-xl border border-border p-4 sm:p-5" data-testid="sub-agent-card">
        <div className="flex min-w-0 items-center gap-3">
          <Bot className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <div className="min-w-0 flex-1">
            <div className="truncate text-ui font-medium text-foreground">{agentTitle}</div>
            <div className="flex items-center gap-1.5 text-meta text-muted-foreground">
              <span className={cn("size-1.5 rounded-full", agentState.dot)} aria-hidden="true" />
              <span>{actionVerb ?? "Sub-agent"}</span>
              <span aria-hidden="true">·</span>
              <span>{agentState.label}</span>
              <span className="sr-only">{statusText}</span>
            </div>
          </div>
          {canOpenThread && (
            <button
              type="button"
              onClick={() => onSelectThread?.(spawnThreadId ?? null)}
              aria-label="Open full thread view"
              className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg border border-border px-3 text-ui font-medium text-foreground transition-colors hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              Open thread
            </button>
          )}
        </div>
        {isExpandable && (
          <div className="mt-3">
            <button
              type="button"
              onClick={onToggleRow}
              aria-expanded={isExpanded}
              aria-label={`Toggle tool details for ${toolCall.name}`}
              className="inline-flex items-center gap-1 rounded-md text-ui text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <ChevronRight className={cn("size-3.5 transition-transform", isExpanded && "rotate-90")} aria-hidden="true" />
              {isExpanded ? "Hide progress" : "Show progress"}
            </button>
            {isExpanded && <div className="mt-2">{detail}</div>}
          </div>
        )}
      </div>
    );
  }

  return (
    <div data-testid="tool-row">
      <QuietRow
        icon={statusMark}
        expanded={isExpandable && isExpanded}
        onToggle={isExpandable ? onToggleRow : undefined}
        ariaLabel={`Toggle tool details for ${toolCall.name}`}
        trailing={rowTrailing}
        detail={isExpandable ? detail : undefined}
      >
        {rowLine}
      </QuietRow>

      {/* Denial reason */}
      {approval?.status === ApprovalStatus.DENIED && approval.denialReason && (
        <p className="ml-6 text-meta text-destructive-ink">Denied: {approval.denialReason}</p>
      )}

      {/* The deploy's outcome stays visible collapsed: the live URL is what the user asked for. */}
      {shellDeploy && <div className="mt-2"><DeployedAppCard deploy={shellDeploy} /></div>}
    </div>
  );
}

export const ToolExecution = memo(ToolExecutionComponent);
