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

// The tool-status identity contract: a tool_call update must land in
// toolCallStates under the SAME key the tool card looks itself up by.
//
// This is the test that was missing when live tool status silently broke. The
// backend emitted every status promptly and correctly, and the store happily
// stored it — just under an id nothing ever queried. Every existing tool-status
// test either passed `status` straight in as a component prop (bypassing the
// store lookup entirely) or asserted on the store Map using whatever key the
// store had just written, which is self-fulfilling: it can never catch the
// writer and the reader disagreeing.
//
// So these tests deliberately assert across the seam. They derive the lookup
// key the way the RENDER path derives it — processMessage → toolExecutions →
// call.id, exactly what ToolExecution.tsx and ChatMessage.tsx pass to
// toolCallStates.get() — and check the status update reached that key.
//
// The timing that made the original bug bite is reproduced in
// "survives message persistence" below: `call_llm` persists the assistant
// message BEFORE `execute_tools` runs, and persistence mints fresh block
// UUIDs. Any key derived from the content block is therefore a different
// string before and after persistence; only the LLM tool-call id is stable.

const CHAT = "c-tool-identity";
const TOOL_CALL_ID = "toolu_01ABCDEFGH";
const PERSISTED_BLOCK_ID = "9f1c2e40-0000-4000-8000-000000000001";

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

/**
 * An assistant message as it exists AFTER persistence: the content block
 * carries a freshly minted UUID (`block.id`) that has no relationship to the
 * LLM-issued `toolCallId`. This divergence is the whole point — a lookup key
 * taken from the block cannot match a status keyed on the tool call.
 */
function persistedAssistantWithToolCall(
  messageId: string,
  toolCallId: string,
  blockId: string,
  toolName = "bash",
): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id: messageId,
      chatId: CHAT,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        {
          id: blockId,
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolCallId,
          toolName,
          input: "{}",
        },
      ],
      createdAt: "2026-01-01T00:00:00.000Z",
      updatedAt: "2026-01-01T00:00:00.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: 1n,
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

/** A tool_call status update in the shape the backend now puts on the wire. */
function toolStatus(
  toolCallId: string,
  status: string,
  toolName = "bash",
): ChatUpdate {
  return {
    update_type: "tool_call",
    tool_call_id: toolCallId,
    tool_name: toolName,
    status,
    node_id: "",
    sequence_number: 0,
  } as unknown as ChatUpdate;
}

/**
 * Resolve the live status the way a rendered tool card does:
 * processMessage gives the card its ToolCallData, the card calls
 * toolCallStates.get(toolCall.id). If the writer and the reader ever disagree
 * on the key again, this returns undefined and the tests below fail.
 */
function statusAsCardWouldSee(messageId: string, toolCallId: string) {
  const state = useChatStore.getState();
  const message = getMessagesFromCache(CHAT).find((m) => m.id === messageId);
  if (!message) return undefined;

  const processed = getProcessedMessage(
    message,
    state.toolResultsByCallId[CHAT] || {},
  );
  const execution = processed.toolExecutions?.find(
    (e) => e.call.id === toolCallId,
  );
  if (!execution) return undefined;

  // The exact lookup expression used by ToolExecution.tsx and ChatMessage.tsx.
  return (state.toolCallStates[CHAT] || new Map()).get(execution.call.id)
    ?.status;
}

beforeEach(seedChat);

describe("tool status identity", () => {
  it("delivers a status update to the card it describes", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      persistedAssistantWithToolCall("m1", TOOL_CALL_ID, PERSISTED_BLOCK_ID),
      toolStatus(TOOL_CALL_ID, "executing"),
    ]);

    expect(statusAsCardWouldSee("m1", TOOL_CALL_ID)).toBe("executing");
  });

  it("survives message persistence, when the block UUID and the tool-call id diverge", () => {
    const store = useChatStore.getState();

    // call_llm persists the assistant message first — fresh block UUID minted.
    store.processChatStreamUpdates(CHAT, [
      persistedAssistantWithToolCall("m1", TOOL_CALL_ID, PERSISTED_BLOCK_ID),
    ]);

    // Only THEN does execute_tools run and report status. This is the ordering
    // under which live status used to be lost for every card in the chat.
    store.processChatStreamUpdates(CHAT, [
      toolStatus(TOOL_CALL_ID, "executing"),
    ]);

    expect(statusAsCardWouldSee("m1", TOOL_CALL_ID)).toBe("executing");

    // Guard the specific regression: keying on the block id must not be what
    // makes this pass. The two ids are genuinely different strings.
    expect(PERSISTED_BLOCK_ID).not.toBe(TOOL_CALL_ID);
    expect(
      useChatStore.getState().toolCallStates[CHAT]?.get(PERSISTED_BLOCK_ID),
    ).toBeUndefined();
  });

  it("tracks a card through its full status lifecycle", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      persistedAssistantWithToolCall("m1", TOOL_CALL_ID, PERSISTED_BLOCK_ID),
    ]);

    store.processChatStreamUpdates(CHAT, [
      toolStatus(TOOL_CALL_ID, "executing"),
    ]);
    expect(statusAsCardWouldSee("m1", TOOL_CALL_ID)).toBe("executing");

    store.processChatStreamUpdates(CHAT, [
      toolStatus(TOOL_CALL_ID, "completed"),
    ]);
    expect(statusAsCardWouldSee("m1", TOOL_CALL_ID)).toBe("completed");
  });

  it("routes concurrent tool calls to their own cards, not to each other", () => {
    // The parallel-spawn symptom: several tools execute at once and each
    // reports independently. If statuses collapse onto a shared or missing
    // key, the cards appear to finish in lockstep instead of individually.
    const callA = "toolu_01AAAA";
    const callB = "toolu_01BBBB";

    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      {
        update_type: "message",
        message: {
          id: "m1",
          chatId: CHAT,
          role: MessageRole.ASSISTANT,
          contentBlocks: [
            {
              id: "block-uuid-a",
              type: ContentBlockType.TOOL_CALL,
              index: 0,
              toolCallId: callA,
              toolName: "spawn",
              input: "{}",
            },
            {
              id: "block-uuid-b",
              type: ContentBlockType.TOOL_CALL,
              index: 1,
              toolCallId: callB,
              toolName: "spawn",
              input: "{}",
            },
          ],
          createdAt: "2026-01-01T00:00:00.000Z",
          updatedAt: "2026-01-01T00:00:00.000Z",
          streamingState: StreamingState.COMPLETE,
          seq: 1n,
          thread: "",
          sequenceNumber: 0n,
          attachments: [],
        },
      } as unknown as ChatUpdate,
    ]);

    // Both start; only A finishes.
    store.processChatStreamUpdates(CHAT, [
      toolStatus(callA, "executing", "spawn"),
      toolStatus(callB, "executing", "spawn"),
      toolStatus(callA, "completed", "spawn"),
    ]);

    expect(statusAsCardWouldSee("m1", callA)).toBe("completed");
    expect(statusAsCardWouldSee("m1", callB)).toBe("executing");
  });

  it("keeps tool status and approvals in separate identifier spaces", () => {
    // Approvals legitimately key on content_block_id; tool status keys on the
    // tool-call id. Both used to travel in a field named content_block_id,
    // which is how they got conflated. Pin that they no longer collide: a
    // status update must not register itself under the approval's block id.
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      persistedAssistantWithToolCall("m1", TOOL_CALL_ID, PERSISTED_BLOCK_ID),
      toolStatus(TOOL_CALL_ID, "executing"),
    ]);

    const states = useChatStore.getState().toolCallStates[CHAT] || new Map();
    expect(states.get(TOOL_CALL_ID)?.status).toBe("executing");
    expect(states.has(PERSISTED_BLOCK_ID)).toBe(false);
  });
});
