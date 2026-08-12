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
  seq: number,
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
      seq: BigInt(seq),
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

// The sentinel-seq contract: a client placeholder carries seq 999999 so it
// sorts after every real message. When the server's persisted message for the
// SAME id arrives with a real (small) seq, the placeholder must be retired —
// leaving exactly one entry, at its real seq, with no stale sentinel behind.
describe("delta identity: sentinel placeholder → real server message", () => {
  it("replaces the sentinel-seq placeholder with the real message (same id, real seq, no duplicate)", () => {
    const chatId = "chat-sentinel-replace";
    const store = useChatStore.getState();

    // A stream starts for a pre-allocated id. The placeholder lives only in
    // the streamingMessages slice and carries the sentinel seq.
    store.processChatStreamUpdates(chatId, [
      contentStart("assistant-sentinel"),
      contentDelta("assistant-sentinel", "partial\n"),
    ]);

    const placeholders = slicePlaceholders(chatId);
    expect(placeholders).toHaveLength(1);
    expect(placeholders[0].id).toBe("assistant-sentinel");
    expect(Number(placeholders[0].seq)).toBe(999999);
    // Never co-mingled into the persisted list while in flight.
    expect(getMessagesFromCache(chatId)).toHaveLength(0);

    // The real persisted message arrives under the same id with a real seq.
    store.processChatStreamUpdates(chatId, [
      completeAssistantMessage(chatId, "assistant-sentinel", 3),
    ]);

    // Exactly one entry, at the real seq — no duplicate.
    const persisted = getMessagesFromCache(chatId);
    expect(persisted.map((m) => m.id)).toEqual(["assistant-sentinel"]);
    expect(Number(persisted[0].seq)).toBe(3);

    // And no stale sentinel placeholder left behind.
    expect(slicePlaceholders(chatId)).toHaveLength(0);
    const anySentinel = [...persisted, ...slicePlaceholders(chatId)].some(
      (m) => Number(m.seq) === 999999,
    );
    expect(anySentinel).toBe(false);
  });
});

// Two threads streaming at once must each own their own placeholder: neither
// blocks nor overwrites the other, and finalizing one leaves the other in
// flight. Placeholders are keyed per thread in the streamingMessages slice.
describe("delta identity: concurrent streams on two threads", () => {
  function threadedDelta(
    messageId: string,
    thread: string,
    text: string,
  ): ChatUpdate {
    return {
      update_type: "streaming_delta",
      delta_type: "content_block_delta",
      block_index: 0,
      delta: text,
      message_id: messageId,
      thread,
    } as unknown as ChatUpdate;
  }

  function threadedStart(messageId: string, thread: string): ChatUpdate {
    return {
      update_type: "streaming_delta",
      delta_type: "content_block_start",
      block_index: 0,
      message_id: messageId,
      thread,
    } as unknown as ChatUpdate;
  }

  it("interleaves two threads' streams without one blocking or clobbering the other", () => {
    const chatId = "chat-concurrent-threads";
    const store = useChatStore.getState();

    // Interleaved deltas from the main thread and a spawn thread.
    store.processChatStreamUpdates(chatId, [
      threadedStart("assistant-main", ""),
      threadedDelta("assistant-main", "", "main one\n"),
      threadedStart("assistant-spawn", "spawn"),
      threadedDelta("assistant-spawn", "spawn", "spawn one\n"),
      threadedDelta("assistant-main", "", "main two\n"),
      threadedDelta("assistant-spawn", "spawn", "spawn two\n"),
    ]);

    const placeholders = slicePlaceholders(chatId);
    expect(placeholders.map((m) => m.id).sort()).toEqual([
      "assistant-main",
      "assistant-spawn",
    ]);

    // Each thread accumulated only its own text — no cross-thread bleed.
    const textOf = (id: string) =>
      (placeholders.find((m) => m.id === id)?.contentBlocks || [])
        .map((b) => b.content || "")
        .join("");
    expect(textOf("assistant-main")).toBe("main one\nmain two\n");
    expect(textOf("assistant-spawn")).toBe("spawn one\nspawn two\n");

    // Finalizing the main thread must not disturb the spawn thread's stream.
    store.processChatStreamUpdates(chatId, [
      streamFinalized("assistant-main", "completed"),
    ]);

    const afterFinalize = slicePlaceholders(chatId);
    expect(afterFinalize.map((m) => m.id)).toEqual(["assistant-spawn"]);
    expect(
      (afterFinalize[0].contentBlocks || []).map((b) => b.content || "").join(""),
    ).toBe("spawn one\nspawn two\n");
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
