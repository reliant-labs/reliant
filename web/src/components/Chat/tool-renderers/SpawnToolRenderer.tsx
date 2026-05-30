/**
 * Renderer for spawn tool calls.
 *
 * In "preview" mode: shows a compact vertical list of the spawned thread's
 * text snippets and tool call rows (status dot + name + detail), growing
 * downward with scroll.
 * In "inline" mode: falls through to the generic renderer.
 */

import { memo, useMemo, useRef, useEffect } from "react";
import {
  AlertCircle,
  Check,
  Loader2,
} from "lucide-react";
import type { ToolContentProps } from "./types";
import { GenericToolRenderer } from "./GenericToolRenderer";
import { useActiveThreads } from "../../../store/threadActivityStore";
import { useChatMessages } from "../../../store/chatStoreHooks";
import { MessageRole, ContentBlockType } from "../../../types/chat";
import type { ContentBlock, WorkflowExecutionData } from "../../../types/chat";

import { useWorkflowExecutions } from "../../../hooks/useWorkflowExecutions";
import { ChatWorkflowStatus } from "../../../gen/reliant/v1/chat_pb";
import { getSpawnDisplayMode } from "../../Settings/SpawnDisplaySettings";
import { cn } from "../../../lib/utils";

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

/** Walk the workflow execution tree to find the child spawned by a given tool call ID. */
function findSpawnWorkflow(
  wf: WorkflowExecutionData,
  spawnNodeId: string,
): WorkflowExecutionData | undefined {
  if (wf.spawnedByNodeId === spawnNodeId) return wf;
  for (const child of wf.children) {
    const found = findSpawnWorkflow(child, spawnNodeId);
    if (found) return found;
  }
  return undefined;
}

function SpawnPreview({ ctx }: ToolContentProps) {
  const { chatId, toolCallId, isCompleted, hasFailed } = ctx;
  const scrollRef = useRef<HTMLDivElement>(null);

  const activeThreads = useActiveThreads(chatId || "");
  const spawnNodeId = `spawn-${toolCallId}`;
  const spawnThread = useMemo(
    () => activeThreads.find(
      (t) => t.spawned_by_tool_call_id === toolCallId || t.spawned_by_node_id === spawnNodeId,
    ),
    [activeThreads, toolCallId, spawnNodeId],
  );

  const { allWorkflows } = useWorkflowExecutions(chatId || null);
  const spawnWorkflow = useMemo(() => {
    if (!toolCallId) return undefined;
    for (const wf of allWorkflows) {
      const found = findSpawnWorkflow(wf, spawnNodeId);
      if (found) return found;
    }
    return undefined;
  }, [allWorkflows, toolCallId, spawnNodeId]);

  const spawnThreadId = spawnThread?.thread || spawnWorkflow?.thread;
  const allMessages = useChatMessages(chatId);

  const workflowCompleted = spawnWorkflow?.status === ChatWorkflowStatus.COMPLETED;
  const workflowFailed =
    spawnWorkflow?.status === ChatWorkflowStatus.FAILED ||
    spawnWorkflow?.status === ChatWorkflowStatus.CANCELLED;

  const isDone = isCompleted || workflowCompleted;

  const summaries = useMemo((): MessageSummary[] => {
    if (!spawnThreadId) return [];
    const threadMsgs = allMessages.filter(
      (msg) => msg.thread === spawnThreadId && msg.role === MessageRole.ASSISTANT,
    );

    // When complete, pop the last message - it's the result shown in tool output
    const msgs = isDone && threadMsgs.length > 1
      ? threadMsgs.slice(0, -1)
      : threadMsgs;

    return msgs.slice(-MAX_PREVIEW_MESSAGES).map((msg) => {
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
          const result = block.matchedResult;
          let detail = extractToolDetail(block.toolName, block.input);
          if (detail.length > MAX_DETAIL_LENGTH) detail = detail.slice(0, MAX_DETAIL_LENGTH) + "\u2026";
          toolCalls.push({
            name: block.toolName,
            displayName: formatToolName(block.toolName),
            completed: result != null && !result.isError,
            failed: result?.isError === true,
            detail,
          });
        }
      }
      return { id: msg.id, textSnippet, toolCalls };
    });
  }, [allMessages, spawnThreadId, isDone]);

  // Auto-scroll to bottom
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [summaries]);

  const hasContent = summaries.some((s) => s.textSnippet || s.toolCalls.length > 0);

  return (
    <div className="tool-content-spawn w-full">
      {hasContent ? (
        <div ref={scrollRef} style={{ height: FIXED_HEIGHT }} className="overflow-y-auto">
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
        !isCompleted && !hasFailed && !workflowCompleted && !workflowFailed && (
          <div className="px-2 py-1.5 text-[10px] text-muted-foreground italic">
            Starting…
          </div>
        )
      )}
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