import { describe, expect, it, beforeEach } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";
import {
  hasMessagesCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// evictChat(chatId) is the memory-release path for deleted/archived chats.
// The messages cache uses gcTime: Infinity, so without explicit eviction a
// deleted chat's messages (and its per-chat Zustand slices) are retained
// until logout. These tests pin: populate via processChatStreamUpdates,
// evict, everything for that chat is gone — and other chats are untouched.

const CHAT = "c-evict";
const OTHER = "c-keep";

function reset() {
  clearAllMessagesCache();
  useChatStore.setState({
    activeChatId: null,
    discussMode: {},
    toolResultsByCallId: {},
    streamingMessages: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
    contextUsage: {},
  } as never);
  useThreadActivityStore.setState({ threads: {} } as never);
}

function completeMessage(chatId: string, id: string, seq: number): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        {
          id: `${id}-b0`,
          type: ContentBlockType.TEXT,
          index: 0,
          content: "hello",
        },
      ],
      createdAt: "2026-01-01T00:00:00.000Z",
      updatedAt: "2026-01-01T00:00:00.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: BigInt(seq),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

function errorUpdate(chatId: string, id: string): ChatUpdate {
  return {
    update_type: "error",
    id,
    chat_id: chatId,
    activity_id: "act",
    error_message: "boom",
    timestamp: "2026-01-01T00:00:01.000Z",
    sequence_number: 0,
  } as unknown as ChatUpdate;
}

function infoUpdate(chatId: string, id: string): ChatUpdate {
  return {
    update_type: "info",
    id,
    chat_id: chatId,
    message: "fyi",
    timestamp: "2026-01-01T00:00:01.000Z",
  } as unknown as ChatUpdate;
}

function toolCall(toolCallId: string): ChatUpdate {
  return {
    update_type: "tool_call",
    tool_call_id: toolCallId,
    tool_name: "bash",
    status: "executing",
    node_id: "",
    sequence_number: 0,
  } as unknown as ChatUpdate;
}

function populate(chatId: string) {
  const store = useChatStore.getState();
  store.processChatStreamUpdates(chatId, [
    completeMessage(chatId, `${chatId}-m1`, 1),
    errorUpdate(chatId, `${chatId}-e1`),
    infoUpdate(chatId, `${chatId}-i1`),
    toolCall(`${chatId}-tc1`),
  ]);
  useChatStore.setState((state) => ({
    contextUsage: {
      ...state.contextUsage,
      [chatId]: { [chatId]: { threadTokenCount: 10, compactionThreshold: 100 } },
    },
    discussMode: { ...state.discussMode, [chatId]: true },
  }));
}

beforeEach(reset);

describe("evictChat", () => {
  it("drops the messages cache and every per-chat slice for the evicted chat", () => {
    populate(CHAT);

    // Preconditions: everything populated
    expect(hasMessagesCache(CHAT)).toBe(true);
    let state = useChatStore.getState();
    expect(state.errorEvents[CHAT]).toBeDefined();
    expect(state.infoEvents[CHAT]).toBeDefined();
    expect(state.toolCallStates[CHAT]).toBeDefined();
    expect(state.contextUsage[CHAT]).toBeDefined();
    expect(state.discussMode[CHAT]).toBeDefined();

    useChatStore.getState().evictChat(CHAT);

    expect(hasMessagesCache(CHAT)).toBe(false);
    state = useChatStore.getState();
    expect(state.errorEvents[CHAT]).toBeUndefined();
    expect(state.infoEvents[CHAT]).toBeUndefined();
    expect(state.runOutputs[CHAT]).toBeUndefined();
    expect(state.nodeExecutions[CHAT]).toBeUndefined();
    expect(state.toolCallStates[CHAT]).toBeUndefined();
    expect(state.streamingMessages[CHAT]).toBeUndefined();
    expect(state.toolResultsByCallId[CHAT]).toBeUndefined();
    expect(state.contextUsage[CHAT]).toBeUndefined();
    expect(state.discussMode[CHAT]).toBeUndefined();
  });

  it("leaves other chats' state untouched", () => {
    populate(CHAT);
    populate(OTHER);

    useChatStore.getState().evictChat(CHAT);

    expect(hasMessagesCache(CHAT)).toBe(false);
    expect(hasMessagesCache(OTHER)).toBe(true);
    const state = useChatStore.getState();
    expect(state.errorEvents[OTHER]).toBeDefined();
    expect(state.toolCallStates[OTHER]).toBeDefined();
    expect(state.contextUsage[OTHER]).toBeDefined();
  });

  it("is a no-op for a chat with no state", () => {
    populate(OTHER);
    expect(() => useChatStore.getState().evictChat("nonexistent")).not.toThrow();
    expect(hasMessagesCache(OTHER)).toBe(true);
  });
});
