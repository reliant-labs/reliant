/**
 * ToolExecutionV2 - Refactored tool execution component
 * 
 * Changes from original:
 * - Content extends full width to outer border (no inner padding creating double borders)
 * - Expand/collapse integrated into bottom of card (no separate button)
 * - Per-tool-type renderers for cleaner code
 * - Simpler, cleaner UI with less visual noise
 */

import { useState, memo, useCallback, useMemo, useEffect } from "react";
import {
  ChevronDown,
  ChevronRight,
  CheckCircle,
  AlertCircle,
  Clock,
  Shield,
  X,
  Loader2,
  Square,
  XCircle,
  Play,
  Zap,
  Maximize2,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";
import { GenericToolRenderer, ToolContentArea, type ToolRenderContext, type ToolResultData } from "./tool-renderers";
import { useChat, useToolCallStates } from "../../store/chatStoreHooks";
import {
  useApproveToolRequest,
  useDenyToolRequest,
} from "../../hooks/approval-queries";
import type { ToolApprovalRequest } from "../../api/client";
import { FileLink } from "./FileLink";
import { formatToolParams, type ToolInput, isViewOnlyTool, isTaskTool, isReadToolWithResults, isSpawnTool } from "../../lib/toolFormatters";
import { parseFilePath } from "../../lib/filePath";
import { openFile, classifyPath } from "../../lib/fileOpener";
import { toast } from "../../lib/toast-manager";
import { useTasksForChat, type TaskItem } from "../../hooks/task-queries";
import { shouldToolBeCollapsed, TOOL_COLLAPSE_SETTINGS_EVENT } from "../Settings/ToolCallSettings";
import { ApprovalStatus } from "../../gen/reliant/v1/approval_pb";
import { ToolCallStatus, ChatWorkflowStatus } from "../../gen/reliant/v1/chat_pb";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";

export type { ToolResultData };

/** Component status strings, as accepted by ToolExecutionProps.status. */
type DisplayStatus = NonNullable<ToolExecutionProps["status"]>;

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
export function workflowStatusToDisplayStatus(status: ChatWorkflowStatus | undefined): DisplayStatus | undefined {
  switch (status) {
    case ChatWorkflowStatus.COMPLETED:
      return "completed";
    case ChatWorkflowStatus.FAILED:
      return "failed";
    case ChatWorkflowStatus.CANCELLED:
      return "cancelled";
    case ChatWorkflowStatus.RUNNING:
    case ChatWorkflowStatus.PENDING:
    case ChatWorkflowStatus.PAUSED:
    case ChatWorkflowStatus.EXPIRED:
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
    /**
     * For a spawn call, the workflow it started (tool_calls.child_workflow_id).
     * A spawned sub-agent's thread id equals its workflow id, so this IS the
     * thread the call owns.
     */
    childWorkflowId?: string;
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
  approval?: ToolApprovalRequest;
  chatId?: string;
  showRichContent?: boolean;
  onSelectThread?: (threadId: string | null) => void;
  density?: "compact" | "card" | "minimal";
}


function ToolExecutionComponent({
  toolCall,
  toolResult,
  onApprove,
  onDeny,
  onCancel,
  onConvertToBackground,
  status,
  approval,
  chatId,
  showRichContent = false,
  onSelectThread,
  density = "compact",
}: ToolExecutionProps) {
  const toolNameLower = (toolCall.name || '').toLowerCase();
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

  const currentChat = useChat(chatId || "");
  const chatWorktreeId = currentChat?.worktreeId;

  // Subscribe to live tool call state. Keyed by the LLM tool-call id, which is
  // what the backend addresses tool status by — it is the only id that exists
  // both while the call streams and after its message is persisted.
  const toolCallStates = useToolCallStates(chatId || "");
  const liveToolCallState = toolCallStates.get(toolCall.id) || null;

  const approveMutation = useApproveToolRequest();
  const denyMutation = useDenyToolRequest();

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
      return workflowStatusToDisplayStatus(spawnChildWorkflow?.status) ?? "executing";
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

  // Approval logic - if backend created a pending approval, we need to show UI for it
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

  // The thread this spawn owns, as recorded by the code that created it:
  // tool_calls.child_workflow_id, and a spawned sub-agent's thread id equals
  // its workflow id.
  //
  // This used to search for it — first scanning live thread-activity updates,
  // then walking the workflow tree for a workflow whose spawnedByNodeId
  // matched `spawn-${toolCall.id}`. Both are derivations of a fact already
  // stored on this very tool call, and both miss: measured on live data,
  // child_workflow_id is present on 394/394 spawn calls while
  // spawned_by_node_id is present on only 368 of them — and absent on ALL
  // THREE currently-executing spawns. Since this value gates the "open full
  // thread view" control, a running spawn simply had no expand button, and
  // finished ones mostly did. That is the same failure the preview had, from
  // the same cause.
  const spawnThreadId = isSpawnToolFlag ? toolCall.childWorkflowId : undefined;

  // Handlers
  const handleApprove = useCallback(() => {
    if (approval && chatId) {
      approveMutation.mutate({ chatId, requestId: approval.id });
    } else if (onApprove) {
      onApprove(toolCall.id);
    }
  }, [approval, chatId, onApprove, toolCall.id, approveMutation]);

  const handleDeny = useCallback(() => {
    if (approval && chatId) {
      denyMutation.mutate({ chatId, requestId: approval.id });
    } else if (onDeny) {
      onDeny(toolCall.id);
    }
  }, [approval, chatId, onDeny, toolCall.id, denyMutation]);

  // Extract file paths for view tools
  const extractFilePaths = (input: Record<string, unknown> | string): string[] => {
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

  // Format tool call display
  const formatToolCallDisplay = (rawName: string, input: Record<string, unknown> | string | undefined): React.ReactNode => {
    // Strip MCP prefix for display: mcp__reliant__spawn -> spawn
    const name = rawName.startsWith('mcp__')
      ? rawName.split('__').pop() || rawName
      : rawName;

    // Task tools show task title
    if (isTaskToolFlag && taskTitle) {
      return taskTitle;
    }

    // The arguments have not streamed in yet. Formatting nothing yields an
    // empty argument list, which reads as "called with no arguments" rather
    // than "still arriving" — so name the state instead of faking the shape.
    if (input === undefined) {
      return (
        <span className="inline-flex items-center gap-1">
          <span className="text-foreground font-medium">{name}</span>
          <span className="text-muted-foreground italic">preparing…</span>
        </span>
      );
    }

    const formatted = formatToolParams(rawName, input as ToolInput);
    if (formatted) {
      // File paths with links
      if (formatted.structured?.filePaths && formatted.structured.filePaths.length > 0) {
        const filePaths = formatted.structured.filePaths;
        if (isViewOnlyToolFlag && filePaths.length === 1) {
          return (
            <span className="inline-flex items-center gap-1">
              <span className="text-foreground font-medium">{name}</span>
              <span className="text-muted-foreground">(</span>
              <FileLink path={filePaths[0]} inline showIcon={false} worktreeId={chatWorktreeId}>
                {formatted.summary}
              </FileLink>
              <span className="text-muted-foreground">)</span>
            </span>
          );
        }
        return (
          <span className="inline-flex items-center gap-1 flex-wrap">
            <span className="text-foreground font-medium">{name}</span>
            <span className="text-muted-foreground">(</span>
            {filePaths.slice(0, 3).map((fp, idx) => (
              <span key={`${fp}-${idx}`} className="inline-flex items-center">
                <FileLink path={fp} inline showIcon={false} worktreeId={chatWorktreeId} />
                {idx < Math.min(filePaths.length - 1, 2) && <span>,</span>}
              </span>
            ))}
            {filePaths.length > 3 && (
              <span className="text-muted-foreground">+{filePaths.length - 3} more</span>
            )}
            <span className="text-muted-foreground">)</span>
          </span>
        );
      }
      return (
        <span className="inline-flex items-center">
          <span className="text-foreground font-medium">{name}</span>
          <span className="text-muted-foreground">({formatted.summary})</span>
        </span>
      );
    }

    return (
      <span className="inline-flex items-center">
        <span className="text-foreground font-medium">{name}</span>
        <span className="text-muted-foreground">()</span>
      </span>
    );
  };

  // Get status icon
  const getStatusIcon = () => {
    if (toolResult?.is_error && toolResult?.content?.includes("blocked")) {
      return <Shield className="w-3.5 h-3.5 text-warning" />;
    }
    if (isCancelled) return <X className="w-3.5 h-3.5 text-destructive" />;
    if (isCancelling) return <Square className="w-3.5 h-3.5 text-warning animate-pulse" />;
    if (hasFailed) return <AlertCircle className="w-3.5 h-3.5 text-warning" />;
    if (isBackgrounded) return <Play className="w-3.5 h-3.5 text-primary" />;
    if (isCompleted) {
      if (isTaskToolFlag && taskTargetStatus) {
        const statusIcons: Record<string, React.ReactNode> = {
          completed: <CheckCircle className="w-3.5 h-3.5 text-success" />,
          in_progress: <Loader2 className="w-3.5 h-3.5 text-info" />,
          failed: <XCircle className="w-3.5 h-3.5 text-destructive" />,
          blocked: <AlertCircle className="w-3.5 h-3.5 text-warning" />,
        };
        return statusIcons[taskTargetStatus] || <CheckCircle className="w-3.5 h-3.5 text-muted-foreground" />;
      }
      return <CheckCircle className="w-3.5 h-3.5 text-success" />;
    }
    if (needsApproval) return <Shield className="w-3.5 h-3.5 text-info" />;
    if (isDenied) return <X className="w-3.5 h-3.5 text-destructive" />;
    if (isExecuting) return <Loader2 className="w-3.5 h-3.5 text-primary animate-spin" />;
    if (isPreparing) return <Loader2 className="w-3.5 h-3.5 text-muted-foreground animate-spin" />;
    if (isRequested) return <Clock className="w-3.5 h-3.5 text-info" />;
    return <Clock className="w-3.5 h-3.5 text-muted-foreground" />;
  };

  // Get status text
  const getStatusText = () => {
    if (isCancelled) return "Cancelled";
    if (isCancelling) return "Cancelling...";
    if (toolResult?.is_error) {
      if (toolResult.content?.includes("blocked")) return "Blocked";
      return "Warning";
    }
    if (hasFailed) return "Warning";
    if (isBackgrounded) return "Background";
    if (isCompleted) {
      if (isTaskToolFlag && taskTargetStatus) {
        return taskTargetStatus.split('_').map(w => w.charAt(0).toUpperCase() + w.slice(1)).join(' ');
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

  // Determine if expandable. Even compact/read-only/view-only tools need an inspector for input/output.
  const hasInput = toolCall.input !== undefined;
  const hasResult = !!toolResult;
  const hasContent = hasInput || hasResult;
  const isExpandable = hasContent && (!isTaskToolFlag || !!taskDescription || hasResult);
  const hasFileOpenAffordance = isViewOnlyToolFlag && extractFilePaths(toolCall.input).length > 0;

  // Left border color strip for status indication. Success is the overwhelming
  // majority of calls, so it stays faint — a transcript of solid green bars
  // reads as decoration and drowns out the states that actually want attention.
  // Warnings and in-flight work keep full strength.
  const leftBorderColor = isCancelled
    ? "border-l-2 border-l-muted-foreground/40"
    : hasFailed || toolResult?.is_error
    ? "border-l-2 border-l-warning"
    : isCompleted || toolResult
    ? "border-l-2 border-l-success/30"
    : isExecuting
    ? "border-l-2 border-l-primary"
    : isPreparing || isCancelling
    ? "border-l-2 border-l-warning"
    : needsApproval
    ? "border-l-2 border-l-warning"
    : "border-l-2 border-l-border/50";

  // Build render context for content area
  const renderContext: ToolRenderContext = {
    toolName: toolCall.name,
    toolCallId: toolCall.id,
    childWorkflowId: toolCall.childWorkflowId,
    input: toolCall.input,
    result: toolResult,
    worktreeId: chatWorktreeId,
    chatId,
    isExpanded,
    isCompleted,
    isExecuting,
    isPreparing,
    hasFailed,
    onSelectThread,
  };

  // Expanding is an act of attention, so the open card is drawn more strongly
  // than the collapsed rows around it: brighter border, real elevation. Idle
  // collapsed rows recede.
  const rootClassName = cn(
    "overflow-hidden transition-colors",
    isExpanded ? "border border-border shadow-md" : "border border-border/30 shadow-sm",
    density === "card" ? "rounded-xl bg-card" : density === "minimal" ? "rounded-md bg-transparent shadow-none" : "rounded-md",
    leftBorderColor
  );
  const headerClassName = cn(
    "flex items-center justify-between",
    isExpanded ? "bg-muted/50" : "bg-muted/20",
    density === "card" ? "px-3 py-2" : density === "minimal" ? "px-2 py-0.5" : "px-2 py-1",
    isExpandable && "cursor-pointer hover:bg-muted/50"
  );
  const contentClassName = cn(
    "border-t border-border/30 overflow-hidden overflow-y-auto bg-muted/15 py-1",
    density === "card" ? "max-h-[720px]" : "max-h-[600px]"
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

  // Task tool special rendering
  if (isTaskToolFlag) {
    const displayTitle = taskTitle || (toolNameLower === 'add_task' ? 'Adding task...' : 'Updating task...');
    const currentTaskStatus = storedTask?.status || taskTargetStatus || 'pending';
    const hasExpandedContent = isExpandable;

    const statusStyles: Record<string, { icon: typeof CheckCircle; color: string }> = {
      pending: { icon: Clock, color: "text-muted-foreground" },
      in_progress: { icon: Zap, color: "text-primary" },
      completed: { icon: CheckCircle, color: "text-success" },
      failed: { icon: XCircle, color: "text-destructive" },
      blocked: { icon: AlertCircle, color: "text-warning" },
    };
    const style = statusStyles[currentTaskStatus] || statusStyles.pending;

    return (
      <div className={rootClassName}>
        {/* Header */}
        <div
          className={cn(headerClassName, "gap-2")}
          onClick={() => hasExpandedContent && setIsExpanded(!isExpanded)} role={hasExpandedContent ? "button" : undefined} tabIndex={hasExpandedContent ? 0 : undefined} onKeyDown={(e) => hasExpandedContent && (e.key === "Enter" || e.key === " ") && (e.preventDefault(), setIsExpanded(!isExpanded))} aria-expanded={hasExpandedContent ? isExpanded : undefined} aria-label={hasExpandedContent ? `Toggle task details for ${displayTitle}` : undefined}
        >
          <span className="inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center">
            {isPreparing ? (
              <Loader2 className="w-3.5 h-3.5 text-muted-foreground animate-spin" />
            ) : isExecuting ? (
              <Loader2 className="w-3.5 h-3.5 text-primary animate-spin" />
            ) : hasFailed ? (
              <AlertCircle className="w-3.5 h-3.5 text-warning" />
            ) : (
              <style.icon className={cn("w-3.5 h-3.5", style.color)} />
            )}
          </span>
          
          <span className="shrink-0 text-xs text-muted-foreground font-mono">task()</span>
          <span className="min-w-0 flex-1 text-xs font-medium truncate">{displayTitle}</span>
          
          {hasExpandedContent && (
            isExpanded ? <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" /> : <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          )}
        </div>

        {/* Expanded content */}
        {isExpanded && hasExpandedContent && (
          <div className={contentClassName}>
            {taskDescription && (
              <div className="px-2 py-1.5 text-xs text-muted-foreground">
                <p>{taskDescription}</p>
              </div>
            )}
            <GenericToolRenderer ctx={renderContext} />
          </div>
        )}
      </div>
    );
  }

  // Standard tool rendering
  return (
    <div className={rootClassName}>
      {/* Header row */}
      <div
        className={headerClassName}
        onClick={() => {
          if (isExpandable) {
            setUserHasToggled(true);
            setIsExpanded(!isExpanded);
          }
        }}
        role={isExpandable ? "button" : undefined}
        tabIndex={isExpandable ? 0 : undefined}
        onKeyDown={(e) => {
          if (isExpandable && (e.key === "Enter" || e.key === " ")) {
            e.preventDefault();
            setUserHasToggled(true);
            setIsExpanded(!isExpanded);
          }
        }}
        aria-expanded={isExpandable ? isExpanded : undefined}
        aria-label={isExpandable ? `Toggle tool details for ${toolCall.name}` : undefined}
      >
          <div className="flex items-center gap-2 flex-1 min-w-0">
            <span className="inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center">
              {getStatusIcon()}
            </span>
            <span className="min-w-0 text-xs truncate">
              {formatToolCallDisplay(toolCall.name, toolCall.input)}
            </span>
            <span className={cn(
              "px-1.5 py-0.5 rounded text-[11px] font-medium shrink-0",
              toolResult?.is_error ? "bg-warning/10 text-warning"
                : hasFailed ? "bg-warning/10 text-warning"
                : isCancelled ? "bg-muted text-muted-foreground"
                : isCompleted ? "bg-success/5 text-success/70"
                : needsApproval ? "bg-warning/10 text-warning"
                : isExecuting ? "bg-primary/10 text-primary"
                : "bg-muted text-muted-foreground"
            )}>
              {getStatusText()}
            </span>
          </div>

          <div className="flex items-center gap-1 shrink-0">
            {hasFileOpenAffordance && (
              <button
                onClick={openPrimaryFile}
                className="rounded px-1.5 py-0.5 text-[11px] font-medium text-primary transition-colors hover:bg-primary/10"
                title="Open file"
                aria-label="Open file"
                type="button"
              >
                Open
              </button>
            )}
            {isSpawnToolFlag && spawnThreadId && onSelectThread && (
              <button
                onClick={(e) => { e.stopPropagation(); onSelectThread(spawnThreadId); }}
                className="p-0.5 hover:bg-muted rounded transition-colors"
                title="Open full thread view" aria-label="Open full thread view"
              >
                <Maximize2 className="w-3.5 h-3.5 text-muted-foreground" />
              </button>
            )}
            {isExecuting && !isCancelling && !isSpawnToolFlag && onConvertToBackground && (
              <button
                onClick={(e) => { e.stopPropagation(); onConvertToBackground(toolCall.id); }}
                className="p-0.5 hover:bg-muted rounded transition-colors"
                title="Push to background" aria-label="Push tool execution to background"
              >
                <Play className="w-3.5 h-3.5 text-info" />
              </button>
            )}
            {(isExecuting || isCancelling) && onCancel && (
              <button
                onClick={(e) => { e.stopPropagation(); onCancel(toolCall.id); }}
                className="p-0.5 hover:bg-muted rounded transition-colors"
                title="Cancel" aria-label="Cancel tool execution"
                disabled={isCancelling}
              >
                <Square className={cn("w-3.5 h-3.5", isCancelling ? "text-warning animate-pulse" : "text-destructive")} />
              </button>
            )}
            {isExpandable && (
              isExpanded 
                ? <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
                : <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
            )}
          </div>
        </div>

        {/* Approval UI — this is the decision screen mobile approvals exist
            for, so Approve/Deny get real touch targets there instead of the
            desktop's compact row. */}
        {shouldShowApprovalUI && (
          <div className="px-2 py-2 border-t border-warning/20 bg-warning/5">
            {showRichContent && toolCall.input && <ToolContentArea ctx={renderContext} />}
            <p className="text-[11px] font-medium text-warning mb-1.5">Approval required</p>
            <div className={cn("flex gap-2", surface !== "desktop" && "flex-col")}>
              <button
                onClick={handleApprove} aria-label="Approve tool execution"
                className={cn(
                  "flex items-center justify-center gap-1.5 rounded font-medium bg-success hover:bg-success/90 text-success-foreground",
                  surface === "desktop" ? "px-3 py-1.5 text-xs" : "min-h-[44px] px-3 text-sm"
                )}
              >
                <CheckCircle className="w-3.5 h-3.5" /> Approve
              </button>
              <button
                onClick={handleDeny} aria-label="Deny tool execution"
                className={cn(
                  "flex items-center justify-center gap-1.5 rounded font-medium bg-destructive hover:bg-destructive/90 text-destructive-foreground",
                  surface === "desktop" ? "px-3 py-1.5 text-xs" : "min-h-[44px] px-3 text-sm"
                )}
              >
                <XCircle className="w-3.5 h-3.5" /> Deny
              </button>
            </div>
          </div>
        )}

        {/* Denial reason */}
        {approval?.status === ApprovalStatus.DENIED && approval.denial_reason && (
          <div className="px-2 py-1 border-t border-destructive/20 bg-destructive/5 text-xs text-destructive">
            Denied: {approval.denial_reason}
          </div>
        )}

        {/* Content area - only shown when expanded */}
        {isExpandable && !shouldShowApprovalUI && isExpanded && (
          <div className={contentClassName}>
            <ToolContentArea ctx={renderContext} />
          </div>
        )}
      </div>
  );
}

export const ToolExecution = memo(ToolExecutionComponent);