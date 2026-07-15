import { describe, expect, it } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";

// End-of-chat ordering/staleness bugs:
//
// The server streams ephemeral deltas and persisted chat updates over two
// independent channels (internal/grpc/services/streaming.go select loop), so
// a stale delta tail can arrive AFTER the finalized assistant message. The
// old code rebuilt a `streaming-temp-*` placeholder from that tail (ordinal
// 999999 → renders at the very end of the chat) and nothing ever removed it
// because no further complete message arrives at the end of a run — the
// phantom stuck around until a page refresh.
//
// The store must also keep messages in the canonical server order (per-thread
// ordinal), not createdAt — repair/attachment messages have shipped with
// wrong (local-vs-UTC) timestamps.

function seedChat(chatId: string) {
  useChatStore.setState({
    activeChatId: null,
    chats: new Map([
      [
        chatId,
        {
          id: chatId,
          userId: "user-1",
          title: "Chat",
          projectId: "p1",
          createdAt: "2026-01-01T00:00:00.000Z",
          updatedAt: "2026-01-01T00:00:00.000Z",
          lastActive: "2026-01-01T00:00:00.000Z",
        } as never,
      ],
    ]),
    messages: {},
    processedMessages: {},
    streamingMessages: {},
    approvals: {},
    pendingApprovals: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
    pendingQuestions: {},
  });
}

function completeAssistantMessage(
  chatId: string,
  id: string,
  ordinal: number,
  createdAt: string,
): ChatUpdate {
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
          content: "done",
        },
      ],
      createdAt,
      updatedAt: createdAt,
      streamingState: StreamingState.COMPLETE,
      ordinal: BigInt(ordinal),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

function streamingTempIds(chatId: string): string[] {
  const state = useChatStore.getState();
  const inMessages = (state.messages[chatId] || [])
    .filter((m) => m.id.startsWith("streaming-temp"))
    .map((m) => m.id);
  const inStreaming = Object.values(state.streamingMessages[chatId] || {})
    .filter((m): m is NonNullable<typeof m> => !!m)
    .map((m) => m.id);
  return [...inMessages, ...inStreaming];
}

describe("late delta tails after finalization", () => {
  it("does not fabricate an empty streaming message from a content-only tail", () => {
    const chatId = "chat-tail-content";
    seedChat(chatId);
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 2, "2026-01-01T00:00:10.000Z"),
    ]);

    // Stale tail: a content delta for the already-finalized stream. The
    // newline makes it bypass the flush buffer and hit the store directly.
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "content_block_delta",
        block_index: 0,
        delta: "stale tail\n",
      } as unknown as ChatUpdate,
    ]);

    expect(streamingTempIds(chatId)).toHaveLength(0);
  });

  it("clearStreamingState removes a phantom rebuilt from a late tool_use_start tail", () => {
    const chatId = "chat-tail-tool";
    seedChat(chatId);
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 2, "2026-01-01T00:00:10.000Z"),
    ]);

    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "tool_use_start",
        block_index: 0,
        tool_call: { id: "tool-9", name: "bash" },
      } as unknown as ChatUpdate,
    ]);

    // The tail rebuilds a placeholder (we can't distinguish it from a new
    // stream starting) — but the activity-IDLE signal must clean it up.
    expect(streamingTempIds(chatId).length).toBeGreaterThan(0);

    useChatStore.getState().clearStreamingState(chatId);

    expect(streamingTempIds(chatId)).toHaveLength(0);
    // The finalized message must survive the cleanup.
    expect(
      (useChatStore.getState().messages[chatId] || []).map((m) => m.id),
    ).toEqual(["m1"]);
  });
});

describe("canonical message order in the store", () => {
  it("orders same-thread messages by ordinal even when createdAt disagrees", () => {
    const chatId = "chat-order";
    seedChat(chatId);
    const store = useChatStore.getState();

    // m3 is a repair-style message persisted with a bogus (earlier) local
    // timestamp. Canonical order is per-thread ordinal: m1, m2, m3.
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 1, "2026-01-01T10:00:00.000Z"),
      completeAssistantMessage(chatId, "m2", 2, "2026-01-01T10:00:20.000Z"),
      completeAssistantMessage(chatId, "m3", 3, "2026-01-01T09:00:00.000Z"),
    ]);

    expect(
      (useChatStore.getState().messages[chatId] || []).map((m) => m.id),
    ).toEqual(["m1", "m2", "m3"]);
  });
});
