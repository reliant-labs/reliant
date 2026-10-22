import { beforeEach, describe, expect, it } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";
import { getProcessedMessage } from "../../lib/messageProcessor";
import {
  getMessagesFromCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// A LIVE spawn must tell its card which thread it owns, exactly as a reload
// does.
//
// The spawn preview reads ToolCallData.childWorkflowId and nothing else (a
// spawned sub-agent's thread id equals its workflow id). On reload that field
// comes off the content block, which the snapshot joins from the durable
// tool_calls row. Live, it cannot: call_llm persists the assistant message —
// spawn tool-call block included — BEFORE the spawn runs, so the block that
// streams in never has a child workflow id, and nothing re-sends it. The only
// live carrier is the spawn's own tool_call status event.
//
// When that fact was dropped on the floor, an open client rendered "Starting…"
// for the whole run of a background spawn while a refresh showed its
// transcript. These tests replay the real wire order observed for one such
// spawn (chat_updates seq 5 → 13) and assert on what the CARD would see.

const CHAT = "c-live-spawn";
const PARENT_THREAD = CHAT;
const SPAWN_CALL = "toolu_01XAu3FjYyvuBXaTRHcDYLGT";
const CHILD_WORKFLOW = "06e44fed-e531-58bf-bc38-163dfa73f440";

function seedChat() {
  clearAllMessagesCache();
  useChatStore.setState({
    activeChatId: null,
    toolResultsByCallId: {},
    streamingMessages: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
  } as never);
  useThreadActivityStore.setState({ threads: {} } as never);
}

/** seq 5: the assistant message, persisted before the spawn dispatched. */
function persistedSpawnCall(
  messageId: string,
  extra: { childWorkflowId?: string } = {},
): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id: messageId,
      chatId: CHAT,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        {
          id: "ea63e9ef-49e1-4f23-a985-013634cf647e",
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolCallId: SPAWN_CALL,
          toolName: "spawn",
          input: '{"preset":"general","title":"sleep 100","prompt":"sleep 100"}',
          ...extra,
        },
      ],
      createdAt: "2026-10-04T17:00:04.112Z",
      updatedAt: "2026-10-04T17:00:04.112Z",
      streamingState: StreamingState.COMPLETE,
      seq: 1n,
      thread: PARENT_THREAD,
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

/** seq 13 and later: the spawn's own status event, as the backend emits it. */
function spawnStatus(
  status: string,
  childWorkflowId: string | undefined,
): ChatUpdate {
  return {
    update_type: "tool_call",
    tool_call_id: SPAWN_CALL,
    tool_name: "spawn",
    status,
    sequence_number: 0,
    ...(childWorkflowId ? { child_workflow_id: childWorkflowId } : {}),
  } as unknown as ChatUpdate;
}

/**
 * The childWorkflowId a rendered spawn card would hand SpawnPreview, derived
 * the way the render path derives it: processMessage → toolExecutions →
 * call, then the live tool-call state for that call id layered on top
 * (ChatMessage.tsx's enhancedToolExecutions).
 */
function childWorkflowAsCardWouldSee(messageId: string): string | undefined {
  const state = useChatStore.getState();
  const message = getMessagesFromCache(CHAT).find((m) => m.id === messageId);
  if (!message) return undefined;
  const processed = getProcessedMessage(
    message,
    state.toolResultsByCallId[CHAT] || {},
  );
  const execution = processed.toolExecutions?.find(
    (e) => e.call.id === SPAWN_CALL,
  );
  if (!execution) return undefined;
  const live = (state.toolCallStates[CHAT] || new Map()).get(execution.call.id);
  return execution.call.childWorkflowId ?? live?.childWorkflowId;
}

beforeEach(seedChat);

describe("live spawn card learns its child thread", () => {
  it("picks up child_workflow_id from the spawn's live status event", () => {
    const store = useChatStore.getState();

    // seq 5 — the block has no child workflow id; the spawn has not run.
    store.processChatStreamUpdates(CHAT, [persistedSpawnCall("m1")]);
    expect(childWorkflowAsCardWouldSee("m1")).toBeUndefined();

    // seq 13 — the dispatch reports "backgrounded" and names the child.
    store.processChatStreamUpdates(CHAT, [
      spawnStatus("backgrounded", CHILD_WORKFLOW),
    ]);

    expect(childWorkflowAsCardWouldSee("m1")).toBe(CHILD_WORKFLOW);
  });

  it("keeps the child workflow id when a later status for the call omits it", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [persistedSpawnCall("m1")]);
    store.processChatStreamUpdates(CHAT, [
      spawnStatus("backgrounded", CHILD_WORKFLOW),
    ]);

    // A status emitted by a path that does not know the child (e.g. a
    // cancellation from call_llm) must not erase a fact already established.
    store.processChatStreamUpdates(CHAT, [spawnStatus("cancelled", undefined)]);

    expect(childWorkflowAsCardWouldSee("m1")).toBe(CHILD_WORKFLOW);
  });

  it("still reads the durable id off the block on reload", () => {
    // The snapshot path: the block itself carries the joined id and no live
    // status ever arrives. The live fix must not be the only way in.
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      persistedSpawnCall("m1", { childWorkflowId: CHILD_WORKFLOW }),
    ]);

    expect(childWorkflowAsCardWouldSee("m1")).toBe(CHILD_WORKFLOW);
  });
});
