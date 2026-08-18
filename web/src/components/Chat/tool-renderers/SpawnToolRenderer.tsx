/**
 * Renderer for spawn tool calls.
 *
 * In "preview" mode: shows a compact vertical list of the spawned thread's
 * text snippets and tool call rows (status dot + name + detail), growing
 * downward inline with the chat (no nested scroll).
 * In "inline" mode: falls through to the generic renderer.
 */

import { memo, useMemo, useState } from "react";
import {
  AlertCircle,
  Check,
  Loader2,
  Send,
} from "lucide-react";
import type { ToolContentProps } from "./types";
import { GenericToolRenderer } from "./GenericToolRenderer";
import { useToolResultsByCallId } from "../../../store/chatStoreHooks";
import { useThreadMessages } from "../../../hooks/message-queries";
import { MessageRole, ContentBlockType } from "../../../types/chat";
import type { ContentBlock, WorkflowExecutionData } from "../../../types/chat";

import { useWorkflowExecutions } from "../../../hooks/useWorkflowExecutions";
import { WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";
import {
  isWorkflowLive,
  workflowStoppedBecause,
} from "../../../lib/workflowLifecycle";
import { getSpawnDisplayMode } from "../../Settings/SpawnDisplaySettings";
import { cn } from "../../../lib/utils";
import { logger } from "../../../lib/logger";
import { compareMessagesWithinThread } from "../../../lib/messageOrder";
import { chatGrpc } from "../../../api/chat-grpc";
import { toast } from "../../../lib/toast-manager";

const MAX_PREVIEW_MESSAGES = 10;
const MAX_TEXT_LENGTH = 150;
const MAX_DETAIL_LENGTH = 40;
const FIXED_HEIGHT = 150;

/** Tool call info with optional detail extracted from input */
interface ToolCallInfo {
  name: string;
  displayName: string;
  completed: boolean;
  failed: boolean;
  detail: string;
}

/** Compact summary of a single message for the preview */
interface MessageSummary {
  id: string;
  textSnippet: string;
  toolCalls: ToolCallInfo[];
}

/** Extract a brief detail string from a tool call's input JSON */
function extractToolDetail(toolName: string, inputJson?: string): string {
  if (!inputJson) return "";
  try {
    const input = JSON.parse(inputJson);
    const baseName = toolName.startsWith("mcp__")
      ? toolName.split("__").pop() || toolName
      : toolName;

    switch (baseName) {
      case "edit":
      case "write":
      case "view": {
        const filePath = input.file_path || input.edits?.[0]?.file_path || "";
        if (filePath) {
          const parts = filePath.split("/");
          return parts.length > 2 ? `…/${parts.slice(-2).join("/")}` : filePath;
        }
        return "";
      }
      case "bash": {
        const cmd = input.command || "";
        return cmd.length > 50 ? cmd.slice(0, 50) + "…" : cmd;
      }
      case "grep":
        return input.pattern ? `/${input.pattern}/` : "";
      case "glob":
        return input.pattern || "";
      case "find_replace":
        return input.find_pattern ? `"${input.find_pattern}"` : "";
      case "spawn":
        return input.title || input.preset || "";
      case "fetch": {
        const url = input.url || "";
        try { return new URL(url).hostname; } catch { return url.slice(0, 40); }
      }
      case "websearch":
        return input.query ? `"${input.query}"` : "";
      case "load_tool":
        return input.name || (input.query ? `"${input.query}"` : "");
      case "skill": {
        const action = input.action || "";
        if (action === "load") return input.path || "";
        if (action === "search") return input.query ? `"${input.query}"` : "";
        return action;
      }
      case "create_plan":
        return input.title || "";
      case "update_task":
        return input.status || "";
      case "add_task":
        return input.title || "";
      case "list_tasks":
        return "";
      case "update_plan":
        return input.title || "";
      default:
        return "";
    }
  } catch {
    return "";
  }
}

function SpawnToolRendererComponent({ ctx }: ToolContentProps) {
  const mode = getSpawnDisplayMode();
  if (mode === "inline") return <GenericToolRenderer ctx={ctx} />;
  return <SpawnPreview ctx={ctx} />;
}

/** Find a workflow by id anywhere in the execution tree. */
function findWorkflowById(
  roots: WorkflowExecutionData[],
  id: string,
): WorkflowExecutionData | undefined {
  for (const wf of roots) {
    if (wf.id === id) return wf;
    const found = findWorkflowById(wf.children, id);
    if (found) return found;
  }
  return undefined;
}

function SpawnPreview({ ctx }: ToolContentProps) {
  const { chatId, childWorkflowId, isCompleted, hasFailed } = ctx;

  // The child workflow is the authority on whether the spawn finished. Look it
  // up by id — the id the spawn itself recorded — rather than searching for
  // something that looks like it.
  const { allWorkflows } = useWorkflowExecutions(chatId || null);
  const spawnWorkflow = useMemo(
    () => (childWorkflowId ? findWorkflowById(allWorkflows, childWorkflowId) : undefined),
    [allWorkflows, childWorkflowId],
  );

  // The thread this spawn owns, stated by the code that created it: the spawn
  // path writes tool_calls.child_workflow_id, and a spawned sub-agent's thread
  // id equals its workflow id.
  //
  // This used to be searched for instead — by scanning live streaming state,
  // then by walking the workflow tree matching spawnedByNodeId. Both are
  // derivations of a fact we already store, and both miss: streaming state is
  // empty for anything not currently running, and spawnedByNodeId is empty on
  // some spawn workflows. When both missed, the preview rendered "Starting..."
  // over a child thread that already had hundreds of messages.
  const spawnThreadId = childWorkflowId;

  // Read the child thread directly. This used to filter the chat-wide message
  // list by thread, which silently depended on the child's messages surviving
  // a window sized for the MAIN transcript — a spawn out-writes its parent by
  // an order of magnitude, so they often did not, and the preview showed
  // "Starting…" over a thread with hundreds of messages.
  const { data: threadMessages, isPending } = useThreadMessages(
    chatId,
    spawnThreadId,
  );
  const toolResultsByCallId = useToolResultsByCallId(chatId || "");

  const workflowFailed =
    !!spawnWorkflow &&
    (workflowStoppedBecause(
      spawnWorkflow.state,
      spawnWorkflow.stopReason,
      WorkflowStopReason.FAILED,
    ) ||
      workflowStoppedBecause(
        spawnWorkflow.state,
        spawnWorkflow.stopReason,
        WorkflowStopReason.CANCELLED,
      ));
  const isCancelledChild = spawnWorkflow
    ? workflowStoppedBecause(
        spawnWorkflow.state,
        spawnWorkflow.stopReason,
        WorkflowStopReason.CANCELLED,
      )
    : false;

  // Once dispatched (a child_workflow_id exists), the child workflow is the
  // SOLE authority on whether this spawn is done — ctx.isCompleted/hasFailed
  // already reflect that same fact via ToolExecution's own child-workflow
  // lookup, so re-checking them here would just be a second hop onto the
  // same data. Pre-dispatch (an early failure before any child thread was
  // created, e.g. thread validation failed) there IS no child workflow to
  // consult, so ctx's own verdict is what we have.
  const isDone = spawnThreadId
    ? spawnWorkflow?.state === WorkflowState.STOPPED
    : isCompleted || hasFailed;

  // The "message this agent" affordance only makes sense while the agent is
  // actually running. A failed or cancelled spawn is just as "not done" as
  // a running one, but offering to message a dead agent is a lie the UI
  // shouldn't tell, even though the backend safely rejects it.
  const spawnRunning =
    !!spawnThreadId &&
    (!spawnWorkflow ||
      isWorkflowLive(spawnWorkflow.state, spawnWorkflow.stopReason));

  const summaries = useMemo((): MessageSummary[] => {
    if (!spawnThreadId) {
      logger.error(
        "[SpawnPreview] No child_workflow_id on this spawn call; cannot find its thread",
        { chatId, toolCallId: ctx.toolCallId },
      );
      return [];
    }
    // Canonical per-thread order (ordinal) so the preview reads
    // top-to-bottom in conversation order.
    const threadMsgs = (threadMessages ?? [])
      .filter((msg) => msg.role === MessageRole.ASSISTANT)
      .slice()
      .sort(compareMessagesWithinThread);

    // A spawn's completion is delivered to the PARENT thread's mailbox, never
    // written into the child thread, so the tool result never duplicates the
    // child's last message here — nothing to pop.
    // Keep the most recent N so the preview tracks live activity.
    return threadMsgs.slice(-MAX_PREVIEW_MESSAGES).map((msg) => {
      const blocks = (msg.contentBlocks || []) as ContentBlock[];
      let textSnippet = "";
      const toolCalls: ToolCallInfo[] = [];

      for (const block of blocks) {
        if (block.type === ContentBlockType.TEXT && block.content) {
          if (!textSnippet) {
            const trimmed = block.content.trim();
            textSnippet = trimmed.length > MAX_TEXT_LENGTH
              ? trimmed.slice(0, MAX_TEXT_LENGTH) + "\u2026"
              : trimmed;
          }
        } else if (block.type === ContentBlockType.TOOL_CALL && block.toolName) {
          // Resolve the result by tool_call_id from the normalized store index
          // (falling back to a backend-embedded matchedResult on loaded chats),
          // mirroring processMessage — no longer read off the block.
          const indexed = block.toolCallId
            ? toolResultsByCallId[block.toolCallId]
            : undefined;
          const isError = indexed
            ? indexed.is_error === true
            : block.matchedResult?.isError === true;
          const hasResult = indexed != null || block.matchedResult != null;
          let detail = extractToolDetail(block.toolName, block.input);
          if (detail.length > MAX_DETAIL_LENGTH) detail = detail.slice(0, MAX_DETAIL_LENGTH) + "\u2026";
          toolCalls.push({
            name: block.toolName,
            displayName: formatToolName(block.toolName),
            completed: hasResult && !isError,
            failed: isError,
            detail,
          });
        }
      }
      return { id: msg.id, textSnippet, toolCalls };
    });
  }, [threadMessages, spawnThreadId, toolResultsByCallId, chatId, ctx.toolCallId]);

  const hasContent = summaries.some((s) => s.textSnippet || s.toolCalls.length > 0);

  return (
    <div className="tool-content-spawn w-full">
      {spawnRunning && chatId && spawnThreadId && (
        <SendToAgentForm chatId={chatId} threadId={spawnThreadId} />
      )}
      {workflowFailed && (
        <div
          className={cn(
            "flex items-center gap-1.5 px-2 py-1 border-b border-border/10 text-[10px]",
            isCancelledChild ? "text-muted-foreground" : "text-warning",
          )}
        >
          <AlertCircle className="w-2.5 h-2.5 shrink-0" />
          <span>{isCancelledChild ? "Agent cancelled" : "Agent failed"}</span>
        </div>
      )}
      {hasContent ? (
        <div
          style={{ height: FIXED_HEIGHT }}
          className="overflow-hidden flex flex-col justify-end"
        >
          {summaries.map((s) => (
            <div key={s.id} className="px-2 py-1 border-b border-border/10 last:border-0">
              {s.textSnippet && (
                <p className="text-[11px] text-foreground/70 leading-snug break-words whitespace-pre-wrap mb-0.5">
                  {s.textSnippet}
                </p>
              )}
              {s.toolCalls.length > 0 && (
                <div className="space-y-px">
                  {s.toolCalls.map((tc, i) => (
                    <div
                      key={i}
                      className={cn(
                        "flex items-center gap-1.5 text-[10px] font-mono py-0.5",
                        tc.failed ? "text-warning" : tc.completed ? "text-muted-foreground" : "text-primary",
                      )}
                    >
                      {tc.failed ? (
                        <AlertCircle className="w-2.5 h-2.5 shrink-0" />
                      ) : tc.completed ? (
                        <Check className="w-2.5 h-2.5 shrink-0" />
                      ) : (
                        <Loader2 className="w-2.5 h-2.5 shrink-0 animate-spin" />
                      )}
                      <span className="shrink-0 font-medium">{tc.displayName}</span>
                      {tc.detail && (
                        <span className="text-foreground/40 truncate">{tc.detail}</span>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      ) : (
        // "Starting…" now means what it says: the child thread is still being
        // fetched, or it genuinely has nothing on it yet while the spawn runs.
        // It is no longer reachable with a loaded-but-windowed-out thread,
        // which is what made it a lie. A background spawn's child workflow
        // row may not exist yet the instant it's dispatched — isDone falls
        // back to ctx.isCompleted/hasFailed in that case, so this still
        // resolves to "Starting…" rather than getting stuck.
        (isPending || !isDone) && (
          <div className="px-2 py-1.5 text-[10px] text-muted-foreground italic">
            Starting…
          </div>
        )
      )}
    </div>
  );
}

/**
 * Minimal affordance to steer a running sub-agent without pausing the whole
 * chat: queues into the agent's mailbox (agent_messages), delivered at its
 * next loop step boundary -- same mechanism spawn_send uses for agent-to-
 * agent messages.
 */
function SendToAgentForm({ chatId, threadId }: { chatId: string; threadId: string }) {
  const [value, setValue] = useState("");
  const [sending, setSending] = useState(false);

  const send = async () => {
    const message = value.trim();
    if (!message || sending) return;
    setSending(true);
    try {
      await chatGrpc.sendAgentMessage(chatId, threadId, message);
      setValue("");
    } catch (error) {
      logger.error("[SendToAgentForm] Failed to send agent message", { error, chatId, threadId });
      toast.error(error);
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="flex items-center gap-1 px-2 py-1 border-b border-border/10">
      <input
        type="text"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            void send();
          }
        }}
        onClick={(e) => e.stopPropagation()}
        placeholder="Message this agent…"
        disabled={sending}
        className="flex-1 min-w-0 bg-transparent text-[11px] text-foreground placeholder:text-muted-foreground/60 outline-none disabled:opacity-50"
      />
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          void send();
        }}
        disabled={sending || !value.trim()}
        className="shrink-0 text-foreground/50 hover:text-foreground disabled:opacity-30 disabled:cursor-not-allowed"
        aria-label="Send message to agent"
      >
        {sending ? (
          <Loader2 className="w-3 h-3 animate-spin" />
        ) : (
          <Send className="w-3 h-3" />
        )}
      </button>
    </div>
  );
}

/** Shorten tool name for display: mcp__foo__bar → bar */
function formatToolName(name: string): string {
  if (name.startsWith("mcp__")) {
    const parts = name.split("__");
    return parts[parts.length - 1];
  }
  return name;
}

export const SpawnToolRenderer = memo(SpawnToolRendererComponent);
