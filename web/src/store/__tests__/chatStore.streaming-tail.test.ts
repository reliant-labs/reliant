import { describe, expect, it } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import {
  getMessagesFromCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// End-of-chat ordering/staleness bugs:
//
// The server streams ephemeral deltas and persisted chat updates over two
// independent channels (internal/grpc/services/streaming.go select loop), so
// a stale delta tail can arrive AFTER the finalized assistant message. The
// old code rebuilt a `streaming-temp-*` placeholder from that tail (seq
// 999999 → renders at the very end of the chat) and nothing ever removed it
// because no further complete message arrives at the end of a run — the
// phantom stuck around until a page refresh.
//
// The store must also keep messages in the canonical server order (chat-global
// seq), not createdAt — repair/attachment messages have shipped with wrong
// (local-vs-UTC) timestamps.

function seedChat(_chatId: string) {
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
}

function completeAssistantMessage(
  chatId: string,
  id: string,
  seq: number,
  createdAt: string,
  thread = "",
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
      seq: BigInt(seq),
      thread,
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

function streamingTempIds(chatId: string): string[] {
  const state = useChatStore.getState();
  const inMessages = getMessagesFromCache(chatId)
    .filter((m) => m.id.startsWith("streaming-temp"))
    .map((m) => m.id);
  const inStreaming = Object.values(state.streamingMessages[chatId] || {})
    .filter((m): m is NonNullable<typeof m> => !!m)
    .map((m) => m.id);
  return [...inMessages, ...inStreaming];
}

describe("late delta tails after finalization", () => {
  it("does not fabricate an empty streaming message from a content-only tail (no message_id / old server)", () => {
    const chatId = "chat-tail-content";
    seedChat(chatId);
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 2, "2026-01-01T00:00:10.000Z"),
    ]);

    // Stale tail: a content delta for the already-finalized stream. The
    // newline makes it bypass the flush buffer and hit the store directly.
    // Old-server deltas carry no message_id — the empty-tail guard is what
    // protects the content case.
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

  it("drops an id-carrying content tail for an already-finalized message", () => {
    const chatId = "chat-tail-content-id";
    seedChat(chatId);
    const store = useChatStore.getState();

    // The finalized message pre-allocated id "m1"; a tail carrying that id is
    // stale and must be dropped by the finalized-id drop rule.
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 2, "2026-01-01T00:00:10.000Z"),
    ]);

    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "content_block_start",
        block_index: 0,
        message_id: "m1",
        stream_seq: 5,
      } as unknown as ChatUpdate,
      {
        update_type: "streaming_delta",
        delta_type: "content_block_delta",
        block_index: 0,
        delta: "stale tail\n",
        message_id: "m1",
        stream_seq: 6,
      } as unknown as ChatUpdate,
    ]);

    expect(streamingTempIds(chatId)).toHaveLength(0);
    expect(getMessagesFromCache(chatId).map((m) => m.id)).toEqual(["m1"]);
  });

  it("clearStreamingState removes a phantom rebuilt from a late tool_use_start tail (no message_id / old server)", () => {
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

    // Old-server tail (no message_id): we can't distinguish it from a new
    // stream starting, so a placeholder IS rebuilt — the activity-IDLE signal
    // is the backstop that cleans it up.
    expect(streamingTempIds(chatId).length).toBeGreaterThan(0);

    useChatStore.getState().clearStreamingState(chatId);

    expect(streamingTempIds(chatId)).toHaveLength(0);
    // The finalized message must survive the cleanup.
    expect(
      getMessagesFromCache(chatId).map((m) => m.id),
    ).toEqual(["m1"]);
  });

  it("builds NO placeholder for an id-carrying tool_use_start tail after finalization", () => {
    const chatId = "chat-tail-tool-id";
    seedChat(chatId);
    const store = useChatStore.getState();

    // The finalized assistant message pre-allocated id "m1".
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 2, "2026-01-01T00:00:10.000Z"),
    ]);

    // A late tool_use_start tail stamped with the same message_id. With delta
    // identity, this is provably stale: it is dropped BEFORE any placeholder is
    // built — the phantom-at-end-of-chat is now impossible (no reliance on the
    // clearStreamingState janitor).
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "tool_use_start",
        block_index: 0,
        tool_call: { id: "tool-9", name: "bash" },
        message_id: "m1",
        stream_seq: 7,
      } as unknown as ChatUpdate,
    ]);

    expect(streamingTempIds(chatId)).toHaveLength(0);
    expect(getMessagesFromCache(chatId).map((m) => m.id)).toEqual(["m1"]);
  });
});

describe("incremental streaming placeholder updates", () => {
  it("updates the in-flight placeholder in place across delta batches (no duplicate)", () => {
    const chatId = "chat-incremental";
    seedChat(chatId);
    const store = useChatStore.getState();

    // The in-flight placeholder lives ONLY in the streamingMessages slice, not
    // the persisted messages array — the render layer composes them.
    const slicePlaceholders = () =>
      Object.values(useChatStore.getState().streamingMessages[chatId] || {}).filter(
        (m): m is NonNullable<typeof m> => !!m,
      );
    const persistedPlaceholders = () =>
      getMessagesFromCache(chatId).filter((m) =>
        m.id.startsWith("streaming-temp"),
      );

    // First batch: start a block and stream some text. The newline forces the
    // batch past the flush buffer so it lands in the store synchronously.
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "content_block_start",
        block_index: 0,
      } as unknown as ChatUpdate,
      {
        update_type: "streaming_delta",
        delta_type: "content_block_delta",
        block_index: 0,
        delta: "Hello\n",
      } as unknown as ChatUpdate,
    ]);

    expect(slicePlaceholders()).toHaveLength(1);
    // Never co-mingled into the persisted messages array.
    expect(persistedPlaceholders()).toHaveLength(0);

    // Second batch: more text for the same thread/block. It must update the
    // same placeholder in place, not append a second one.
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "content_block_delta",
        block_index: 0,
        delta: " world\n",
      } as unknown as ChatUpdate,
    ]);

    const placeholders = slicePlaceholders();
    expect(placeholders).toHaveLength(1);
    expect(persistedPlaceholders()).toHaveLength(0);
    const text = (placeholders[0].contentBlocks || [])
      .map((b) => b.content || "")
      .join("");
    expect(text).toBe("Hello\n world\n");
  });
});

describe("canonical message order in the store", () => {
  it("orders same-thread messages by seq even when createdAt disagrees", () => {
    const chatId = "chat-order";
    seedChat(chatId);
    const store = useChatStore.getState();

    // m3 is a repair-style message persisted with a bogus (earlier) local
    // timestamp. Canonical order is seq: m1, m2, m3.
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 1, "2026-01-01T10:00:00.000Z"),
      completeAssistantMessage(chatId, "m2", 2, "2026-01-01T10:00:20.000Z"),
      completeAssistantMessage(chatId, "m3", 3, "2026-01-01T09:00:00.000Z"),
    ]);

    expect(
      getMessagesFromCache(chatId).map((m) => m.id),
    ).toEqual(["m1", "m2", "m3"]);
  });

  // The end-to-end version of the phase-3 proof: two threads interleaving,
  // rendered through the real store path. Under per-thread ordinals both
  // threads counted 1,2,3 and the client had to infer the interleaving from
  // (clamped) timestamps; here the timestamps are deliberately WRONG — every
  // spawn message claims an hour earlier than it happened — and the order is
  // still exact, because seq is a chat-global total order.
  it("renders the true cross-thread interleaving from chat-global seq", () => {
    const chatId = "chat-interleave";
    seedChat(chatId);
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "main-1", 1, "2026-01-01T10:00:00.000Z"),
      completeAssistantMessage(chatId, "spawn-1", 2, "2026-01-01T09:00:01.000Z", "spawn"),
      completeAssistantMessage(chatId, "main-2", 3, "2026-01-01T10:00:02.000Z"),
      completeAssistantMessage(chatId, "spawn-2", 4, "2026-01-01T09:00:03.000Z", "spawn"),
      completeAssistantMessage(chatId, "main-3", 5, "2026-01-01T10:00:04.000Z"),
    ]);

    expect(getMessagesFromCache(chatId).map((m) => m.id)).toEqual([
      "main-1",
      "spawn-1",
      "main-2",
      "spawn-2",
      "main-3",
    ]);
  });
});