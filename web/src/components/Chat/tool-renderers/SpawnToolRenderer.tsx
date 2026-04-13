/**
 * Renderer for spawn tool calls.
 *
 * In "preview" mode: shows a compact timeline of the spawned thread's messages
 * with text snippets and tool call summaries, plus an "Open Thread" button.
 * In "inline" mode: falls through to the generic renderer.
 */

import { memo, useMemo } from "react";
import {
  Wrench,
  Maximize2,
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

const MAX_PREVIEW_MESSAGES = 8;
const MAX_TEXT_LENGTH = 150;

/** Compact summary of a single message for the preview */
interface MessageSummary {
  id: string;
  textSnippet: string;
  toolCalls: { name: string; completed: boolean; failed: boolean }[];
}

function SpawnToolRendererComponent({ ctx }: ToolContentProps) {
  const mode = getSpawnDisplayMode();

  if (mode === "inline") {
    return <GenericToolRenderer ctx={ctx} />;
  }

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
  const { chatId, toolCallId, isCompleted, hasFailed, onSelectThread } = ctx;

  // Source 1: active thread updates (populated during live streaming)
  const activeThreads = useActiveThreads(chatId || "");
  const spawnNodeId = `spawn-${toolCallId}`;
  const spawnThread = useMemo(
    () =>
      activeThreads.find(
        (t) =>
          t.spawned_by_tool_call_id === toolCallId ||
          t.spawned_by_node_id === spawnNodeId,
      ),
    [activeThreads, toolCallId, spawnNodeId],
  );

  // Source 2: workflow execution tree (persisted, works for historical chats)
  const { allWorkflows } = useWorkflowExecutions(chatId || null);
  const spawnWorkflow = useMemo(() => {
    if (!toolCallId) return undefined;
    for (const wf of allWorkflows) {
      const found = findSpawnWorkflow(wf, spawnNodeId);
      if (found) return found;
    }
    return undefined;
  }, [allWorkflows, toolCallId, spawnNodeId]);

  // Prefer activeThread (has live status), fall back to workflow data
  const spawnThreadId = spawnThread?.thread || spawnWorkflow?.thread;

  const allMessages = useChatMessages(chatId);
  const summaries = useMemo((): MessageSummary[] => {
    if (!spawnThreadId) return [];

    const threadMsgs = allMessages.filter(
      (msg) =>
        msg.thread === spawnThreadId && msg.role === MessageRole.ASSISTANT,
    );

    return threadMsgs.slice(-MAX_PREVIEW_MESSAGES).map((msg) => {
      const blocks = (msg.contentBlocks || []) as ContentBlock[];
      let textSnippet = "";
      const toolCalls: MessageSummary["toolCalls"] = [];

      for (const block of blocks) {
        if (block.type === ContentBlockType.TEXT && block.content) {
          if (!textSnippet) {
            const trimmed = block.content.trim();
            textSnippet =
              trimmed.length > MAX_TEXT_LENGTH
                ? trimmed.slice(0, MAX_TEXT_LENGTH) + "\u2026"
                : trimmed;
          }
        } else if (block.type === ContentBlockType.TOOL_CALL && block.toolName) {
          const result = block.matchedResult;
          toolCalls.push({
            name: formatToolName(block.toolName),
            completed: result != null && !result.isError,
            failed: result?.isError === true,
          });
        }
      }

      return { id: msg.id, textSnippet, toolCalls };
    });
  }, [allMessages, spawnThreadId]);

  const workflowCompleted = spawnWorkflow?.status === ChatWorkflowStatus.COMPLETED;
  const workflowFailed =
    spawnWorkflow?.status === ChatWorkflowStatus.FAILED ||
    spawnWorkflow?.status === ChatWorkflowStatus.CANCELLED;

  return (
    <div className="tool-content-spawn">
      {/* Open button */}
      {spawnThreadId && onSelectThread && (
        <div className="flex items-center justify-end px-2 py-1 bg-muted/30 border-b border-border/30">
          <button
            onClick={() => onSelectThread(spawnThreadId)}
            className="flex items-center gap-1 px-1.5 py-0.5 text-[10px] text-muted-foreground hover:text-foreground hover:bg-muted rounded transition-colors shrink-0"
            title="Open full thread view"
          >
            <Maximize2 className="w-3 h-3" />
            Open
          </button>
        </div>
      )}

      {/* Message list */}
      {summaries.length > 0 ? (
        <div className="max-h-[200px] overflow-y-auto divide-y divide-border/20">
          {summaries.map((s) => (
            <div key={s.id} className="px-2 py-1.5 space-y-1">
              {/* Text snippet */}
              {s.textSnippet && (
                <p className="text-[11px] text-foreground/80 leading-relaxed break-words whitespace-pre-wrap">
                  {s.textSnippet}
                </p>
              )}
              {/* Tool call chips */}
              {s.toolCalls.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {s.toolCalls.map((tc, i) => (
                    <span
                      key={i}
                      className={cn(
                        "inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-[10px] font-mono",
                        tc.failed
                          ? "bg-destructive/10 text-destructive"
                          : tc.completed
                            ? "bg-muted text-muted-foreground"
                            : "bg-primary/10 text-primary",
                      )}
                    >
                      <Wrench className="w-2.5 h-2.5" />
                      {tc.name}
                    </span>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      ) : (
        !isCompleted &&
        !hasFailed &&
        !workflowCompleted &&
        !workflowFailed && (
          <div className="px-2 py-2 text-[10px] text-muted-foreground italic">
            Waiting for messages\u2026
          </div>
        )
      )}
    </div>
  );
}

/** Shorten tool name for chip display: mcp__foo__bar → bar */
function formatToolName(name: string): string {
  if (name.startsWith("mcp__")) {
    const parts = name.split("__");
    return parts[parts.length - 1];
  }
  return name;
}

export const SpawnToolRenderer = memo(SpawnToolRendererComponent);