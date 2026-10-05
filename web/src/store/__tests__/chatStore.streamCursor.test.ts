import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";
import {
  clearAllMessagesCache,
  clearMessagesCache,
  patchMessagesCache,
} from "../../hooks/message-queries";

// The stream resumes a reopened chat from a cursor instead of a snapshot.
// That is only correct while this store's cached state for the chat is
// exactly the server's state as of the cursor — a cursor that outlives its
// state replays a delta onto nothing and renders a fragment as the whole chat.
// These pin that the cursor lives and dies with the state it describes.

const CHAT = "c-cursor";
const OTHER = "c-other";

function reset() {
  useChatStore.getState().reset();
  clearAllMessagesCache();
  useThreadActivityStore.setState({ threads: {} } as never);
}

function message(chatId: string, id: string, seq: number): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        { id: `${id}-b0`, type: ContentBlockType.TEXT, index: 0, content: "hi" },
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

/** Cache a chat's state as a snapshot would, so the store holds it. */
function cacheChat(chatId: string) {
  useChatStore
    .getState()
    .processChatStreamUpdates(chatId, [message(chatId, `${chatId}-m1`, 1)], true);
}

beforeEach(reset);

describe("chat stream cursor", () => {
  it("resumes from the recorded cursor while the chat's state is cached", () => {
    cacheChat(CHAT);
    const store = useChatStore.getState();

    store.recordChatStreamCursor(CHAT, 500n);

    expect(store.resumeChatStreamFrom(CHAT)).toBe(500n);
  });

  it("is consumed on resume, so a later reopen cannot rewind to it", () => {
    cacheChat(CHAT);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);

    store.resumeChatStreamFrom(CHAT);

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("returns 0 for a chat that was never left", () => {
    cacheChat(CHAT);
    expect(useChatStore.getState().resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("refuses to record a cursor for a chat with no cached state", () => {
    const store = useChatStore.getState();

    store.recordChatStreamCursor(CHAT, 500n);
    cacheChat(CHAT); // state arrives later — the cursor still must not apply

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("drops the cursor when the chat is evicted", () => {
    cacheChat(CHAT);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);

    store.evictChat(CHAT);
    cacheChat(CHAT); // reopened and re-seeded: a fresh snapshot, not a replay

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("drops every cursor on reset", () => {
    cacheChat(CHAT);
    cacheChat(OTHER);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);
    store.recordChatStreamCursor(OTHER, 600n);

    store.reset();
    cacheChat(CHAT);
    cacheChat(OTHER);

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
    expect(store.resumeChatStreamFrom(OTHER)).toBe(0n);
  });

  it("drops the cursor when forceResetChatToIdle diverges local state", () => {
    cacheChat(CHAT);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);

    store.forceResetChatToIdle(CHAT);

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("refuses a cursor whose message cache was cleared out from under it", () => {
    cacheChat(CHAT);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);

    clearMessagesCache(CHAT);

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("refuses the cursor while a local-only optimistic message is cached", () => {
    // A failed send strands its optimistic row; only a snapshot removes it.
    cacheChat(CHAT);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);
    patchMessagesCache(CHAT, (msgs) => [
      ...msgs,
      { ...msgs[0], id: "optimistic-user-123", role: MessageRole.USER },
    ]);

    expect(store.resumeChatStreamFrom(CHAT)).toBe(0n);
  });

  it("evicting one chat leaves another chat's cursor intact", () => {
    cacheChat(CHAT);
    cacheChat(OTHER);
    const store = useChatStore.getState();
    store.recordChatStreamCursor(CHAT, 500n);
    store.recordChatStreamCursor(OTHER, 600n);

    store.evictChat(CHAT);

    expect(store.resumeChatStreamFrom(OTHER)).toBe(600n);
  });
});

describe("end to end through globalUpdatesStore", () => {
  it("records the left chat's cursor and resumes the reopened chat from it", async () => {
    // The real wiring: UserStreamingService ↔ globalUpdatesStore ↔ chatStore.
    // Only the network (establishConnection) is stubbed.
    const { useGlobalUpdatesStore } = await import("../globalUpdatesStore");
    const { UserStreamingService } = await import("../../api/streaming-grpc");
    const requested: Array<[string | undefined, bigint]> = [];
    const connect = vi
      .spyOn(UserStreamingService.prototype as never, "establishConnection")
      .mockImplementation(async function (this: unknown) {
        const svc = this as { subscribedChatId?: string; lastChatSequence: bigint };
        requested.push([svc.subscribedChatId, svc.lastChatSequence]);
      });
    try {
      useChatStore.setState({ hasLoaded: true });
      useGlobalUpdatesStore.setState({
        wsService: null,
        subscribedChatId: null,
        lastSequence: 1,
        connectionStatus: "disconnected",
      });
      const store = useGlobalUpdatesStore.getState();

      // Open CHAT: first visit, nothing cached → snapshot.
      store.subscribeToChatDetails(CHAT);
      const svc = useGlobalUpdatesStore.getState().wsService as unknown as {
        handleEvent(e: unknown): void;
        isConnected_: boolean;
        connectAttemptInFlight: boolean;
      };
      svc.isConnected_ = true;
      svc.connectAttemptInFlight = false;
      svc.handleEvent({
        event: {
          case: "chatSyncSnapshot",
          value: {
            messages: [(message(CHAT, `${CHAT}-m1`, 1) as { message: unknown }).message],
            otherUpdates: [],
            latestSequence: 700n,
            total: 1,
            hasMore: false,
            oldestSeq: 1n,
            threadTokenCount: 0n,
            compactionThreshold: 0n,
          },
        },
      });

      // Switch away, then back.
      useGlobalUpdatesStore.getState().subscribeToChatDetails(OTHER);
      svc.isConnected_ = true;
      svc.connectAttemptInFlight = false;
      useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT);

      expect(requested).toEqual([
        [CHAT, 0n], // first open: snapshot
        [OTHER, 0n], // never opened: snapshot
        [CHAT, 700n], // reopen: replay from the cached cursor
      ]);
      // Cached content is on screen while the replay is outstanding.
      expect(useChatStore.getState().chatSyncPendingId).toBe(CHAT);
    } finally {
      connect.mockRestore();
      useGlobalUpdatesStore.getState().disconnect();
    }
  });

  it("keeps thread activity across a deselect, so a resumed replay has a base", async () => {
    // A replay re-delivers only threads that changed since the cursor. If a
    // deselect wiped the slice, every unchanged thread would vanish on reopen.
    const { useGlobalUpdatesStore } = await import("../globalUpdatesStore");
    useThreadActivityStore
      .getState()
      .setThreads(CHAT, [{ id: "t1", status: "running" } as never]);
    useGlobalUpdatesStore.setState({ wsService: null, subscribedChatId: CHAT });

    useGlobalUpdatesStore.getState().unsubscribeFromChatDetails(CHAT);

    expect(useThreadActivityStore.getState().threads[CHAT]).toHaveLength(1);
  });
});

describe("chat sync pending (transcript syncing indicator)", () => {
  it("is set for a chat with cached content", () => {
    cacheChat(CHAT);

    useChatStore.getState().setChatSyncPending(CHAT);

    expect(useChatStore.getState().chatSyncPendingId).toBe(CHAT);
  });

  it("is NOT set for a chat with nothing cached — the loading state covers that", () => {
    useChatStore.getState().setChatSyncPending(CHAT);

    expect(useChatStore.getState().chatSyncPendingId).toBeNull();
  });

  it("clears on null", () => {
    cacheChat(CHAT);
    useChatStore.getState().setChatSyncPending(CHAT);

    useChatStore.getState().setChatSyncPending(null);

    expect(useChatStore.getState().chatSyncPendingId).toBeNull();
  });
});
