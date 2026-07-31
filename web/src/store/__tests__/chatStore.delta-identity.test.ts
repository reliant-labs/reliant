import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

// Delta identity protocol (client half).
//
// Phase 1 (Go) pre-allocates the assistant message id, stamps every streaming
// delta with it (message_id + stream_seq), and ALWAYS emits a persisted
// stream_finalized chat update when a stream ends. This suite pins the client
// contract that makes the phantom-at-end-of-chat impossible: once a message id
// is finalized, any delta carrying that id is dropped BEFORE a placeholder is
// built.

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
}

function completeAssistantMessage(
  chatId: string,
  id: string,
  ordinal: number,
  createdAt = "2026-01-01T00:00:10.000Z",
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

function contentStart(messageId: string | undefined, blockIndex = 0): ChatUpdate {
  return {
    update_type: "streaming_delta",
    delta_type: "content_block_start",
    block_index: blockIndex,
    ...(messageId ? { message_id: messageId } : {}),
  } as unknown as ChatUpdate;
}

function contentDelta(
  messageId: string | undefined,
  text: string,
  blockIndex = 0,
): ChatUpdate {
  return {
    update_type: "streaming_delta",
    delta_type: "content_block_delta",
    block_index: blockIndex,
    delta: text,
    ...(messageId ? { message_id: messageId } : {}),
  } as unknown as ChatUpdate;
}

function streamFinalized(
  messageId: string,
  reason: "completed" | "aborted" | "cancelled" = "aborted",
): ChatUpdate {
  return {
    update_type: "stream_finalized",
    message_id: messageId,
    reason,
    thread: "",
  } as unknown as ChatUpdate;
}

function slicePlaceholders(chatId: string) {
  return Object.values(
    useChatStore.getState().streamingMessages[chatId] || {},
  ).filter((m): m is NonNullable<typeof m> => !!m);
}

beforeEach(seedChat);

describe("delta identity: placeholder identity", () => {
  it("uses the real message_id as the placeholder id when present", () => {
    const chatId = "chat-id-identity";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-42"),
      contentDelta("assistant-42", "Hello\n"),
    ]);

    const placeholders = slicePlaceholders(chatId);
    expect(placeholders).toHaveLength(1);
    expect(placeholders[0].id).toBe("assistant-42");
    // No fabricated streaming-temp id when the server provides identity.
    expect(placeholders[0].id.startsWith("streaming-temp")).toBe(false);
  });

  it("falls back to the streaming-temp id when message_id is absent (old server)", () => {
    const chatId = "chat-id-fallback";
    const store = useChatStore.getState();

    store.processChatStreamUpdates(chatId, [
      contentStart(undefined),
      contentDelta(undefined, "Hello\n"),
    ]);

    const placeholders = slicePlaceholders(chatId);
    expect(placeholders).toHaveLength(1);
    expect(placeholders[0].id.startsWith("streaming-temp")).toBe(true);
  });
});

describe("delta identity: the drop rule", () => {
  it("drops deltas whose message_id was finalized by a live stream_finalized marker", () => {
    const chatId = "chat-live-finalize";
    const store = useChatStore.getState();

    // Live finalize marker for a message that never persisted (aborted mid-stream).
    store.processChatStreamUpdates(chatId, [streamFinalized("assistant-99", "aborted")]);

    // A tail delta carrying that id must be dropped before any placeholder.
    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-99"),
      contentDelta("assistant-99", "ghost\n"),
    ]);

    expect(slicePlaceholders(chatId)).toHaveLength(0);
  });

  it("removes an existing placeholder when its id is finalized live", () => {
    const chatId = "chat-finalize-removes";
    const store = useChatStore.getState();

    // Build a live placeholder.
    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-7"),
      contentDelta("assistant-7", "partial\n"),
    ]);
    expect(slicePlaceholders(chatId).map((m) => m.id)).toEqual(["assistant-7"]);

    // The stream finalizes (aborted, no persisted message) — the placeholder
    // for that id must be retired even without a complete message arriving.
    store.processChatStreamUpdates(chatId, [streamFinalized("assistant-7", "aborted")]);

    expect(slicePlaceholders(chatId)).toHaveLength(0);
  });

  it("finalizes a message id via its persisted assistant message; a later delta is dropped", () => {
    const chatId = "chat-persist-finalize";
    const store = useChatStore.getState();

    // The persisted assistant message arrives (normal completion).
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "m1", 2),
    ]);

    // A late delta stamped with the persisted id is stale → dropped.
    store.processChatStreamUpdates(chatId, [
      contentStart("m1"),
      contentDelta("m1", "late\n"),
    ]);

    expect(slicePlaceholders(chatId)).toHaveLength(0);
    expect(getMessagesFromCache(chatId).map((m) => m.id)).toEqual(["m1"]);
  });

  it("seeds the finalized set from a snapshot replay (replace semantics)", () => {
    const chatId = "chat-snapshot-seed";
    const store = useChatStore.getState();

    // Snapshot carries the persisted stream_finalized marker.
    store.processChatStreamUpdates(
      chatId,
      [streamFinalized("assistant-snap", "aborted")],
      true,
    );

    // A tail for that id after the snapshot is dropped.
    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-snap"),
      contentDelta("assistant-snap", "ghost\n"),
    ]);

    expect(slicePlaceholders(chatId)).toHaveLength(0);
  });
});

describe("delta identity: buffered flush after finalize", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("drops a buffered flush when finalization lands between buffer and flush", async () => {
    const chatId = "chat-buffered-drop";
    const store = useChatStore.getState();

    // A content delta WITHOUT a trailing newline stays in the flush buffer.
    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-buf"),
      contentDelta("assistant-buf", "buffered text"),
    ]);

    // Finalization lands before the buffer timer fires.
    store.processChatStreamUpdates(chatId, [
      streamFinalized("assistant-buf", "aborted"),
    ]);

    // Fire the buffer flush timer + its requestAnimationFrame.
    await vi.runAllTimersAsync();

    // The flush must be dropped: the id was finalized, so no placeholder.
    expect(slicePlaceholders(chatId)).toHaveLength(0);
  });
});

describe("delta identity: message_start resets blocks", () => {
  it("resets the thread placeholder's blocks on message_start (retry re-stream)", () => {
    const chatId = "chat-message-start";
    const store = useChatStore.getState();

    // Attempt 1 streams a tool call into block 0, then stalls (retryable error).
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "tool_use_start",
        block_index: 0,
        tool_call: { id: "tool-a1", name: "bash" },
        message_id: "assistant-retry",
      } as unknown as ChatUpdate,
    ]);
    expect(
      (slicePlaceholders(chatId)[0]?.contentBlocks || []).length,
    ).toBeGreaterThan(0);

    // Attempt 2 re-streams from block 0. message_start must clear the stale
    // attempt-1 blocks so they don't linger alongside the new content.
    store.processChatStreamUpdates(chatId, [
      {
        update_type: "streaming_delta",
        delta_type: "message_start",
        block_index: 0,
        message_id: "assistant-retry",
      } as unknown as ChatUpdate,
    ]);

    const placeholders = slicePlaceholders(chatId);
    // Either the placeholder is reset to empty blocks or removed entirely;
    // in both cases no stale attempt-1 tool block survives.
    const staleBlocks = placeholders.flatMap((m) => m.contentBlocks || []);
    expect(staleBlocks).toHaveLength(0);
  });
});
