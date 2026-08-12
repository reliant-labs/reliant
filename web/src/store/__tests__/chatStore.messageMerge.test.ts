import { describe, expect, it, beforeEach } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import {
  getMessagesFromCache,
  setMessagesInCache,
  clearAllMessagesCache,
} from "../../hooks/message-queries";

// Characterization tests for mergeMessages, exercised through the public
// processChatStreamUpdates path. Locks the three behaviors the message merge
// must preserve now that messages live in the React Query cache:
//   1. incremental upsert by id (new appended, existing replaced in place)
//   2. snapshot REPLACES the whole list (reconnect/cross-chat contamination guard)
//   3. optimistic user placeholder (optimistic-user-*) is dropped once a real
//      user message arrives — no duplicate

const CHAT = "c-merge";

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

function assistantMessage(id: string, seq: number, text: string): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId: CHAT,
      role: MessageRole.ASSISTANT,
      contentBlocks: [
        { id: `${id}-b0`, type: ContentBlockType.TEXT, index: 0, content: text },
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

function userMessage(id: string, seq: number): ChatUpdate {
  return {
    update_type: "message",
    message: {
      id,
      chatId: CHAT,
      role: MessageRole.USER,
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

/** A bare Message value (not a ChatUpdate) for seeding the cache directly. */
function messageValue(id: string, seq: number) {
  return {
    id,
    chatId: CHAT,
    role: MessageRole.ASSISTANT,
    contentBlocks: [],
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    streamingState: StreamingState.COMPLETE,
    seq: BigInt(seq),
    thread: "",
    sequenceNumber: 0n,
    attachments: [],
  } as never;
}

function ids() {
  return getMessagesFromCache(CHAT).map((m) => m.id);
}

beforeEach(seedChat);

describe("message merge (incremental)", () => {
  it("appends new messages and replaces existing by id", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      assistantMessage("m1", 1, "first"),
      assistantMessage("m2", 2, "second"),
    ]);
    expect(ids()).toEqual(["m1", "m2"]);

    // Re-deliver m1 with new content → replaced in place, not duplicated.
    store.processChatStreamUpdates(CHAT, [assistantMessage("m1", 1, "first-edited")]);
    const msgs = getMessagesFromCache(CHAT);
    expect(msgs.map((m) => m.id)).toEqual(["m1", "m2"]);
    expect(msgs.find((m) => m.id === "m1")?.contentBlocks?.[0]?.content).toBe(
      "first-edited",
    );
  });
});

describe("message merge (snapshot)", () => {
  it("replaces the entire list on snapshot", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [assistantMessage("m1", 1, "live")]);
    // Snapshot carrying a different message set must REPLACE, not merge.
    store.processChatStreamUpdates(CHAT, [assistantMessage("m2", 2, "snapshot")], true);
    expect(ids()).toEqual(["m2"]);
  });

  // The snapshot is BOUNDED to the newest N messages, so a reconnect re-delivers
  // that window. Replacing then would throw away pages the user scrolled back to
  // load; overlap tells us the snapshot is continuous with what we already hold.
  it("preserves already-loaded older pages on a reconnect snapshot", () => {
    // Cache holds 4 messages (older m1/m2 were loaded via scroll-back).
    setMessagesInCache(CHAT, [
      messageValue("m1", 1),
      messageValue("m2", 2),
      messageValue("m3", 3),
      messageValue("m4", 4),
    ]);

    // Reconnect snapshot carries only the newest 2 — both already known.
    useChatStore
      .getState()
      .processChatStreamUpdates(
        CHAT,
        [assistantMessage("m3", 3, "snap"), assistantMessage("m4", 4, "snap")],
        true,
      );

    expect(ids()).toEqual(["m1", "m2", "m3", "m4"]);
  });

  it("upserts messages that arrived while the stream was down", () => {
    setMessagesInCache(CHAT, [
      messageValue("m1", 1),
      messageValue("m2", 2),
      messageValue("m3", 3),
    ]);

    // Overlaps on m3, and m4 is new (sent while disconnected).
    useChatStore
      .getState()
      .processChatStreamUpdates(
        CHAT,
        [assistantMessage("m3", 3, "snap"), assistantMessage("m4", 4, "new")],
        true,
      );

    expect(ids()).toEqual(["m1", "m2", "m3", "m4"]);
  });

  it("still replaces when the snapshot shares no ids (cross-chat guard)", () => {
    setMessagesInCache(CHAT, [
      messageValue("m1", 1),
      messageValue("m2", 2),
      messageValue("m3", 3),
    ]);

    // No overlap at all — stale/foreign snapshot. Replace, do not merge.
    useChatStore
      .getState()
      .processChatStreamUpdates(CHAT, [assistantMessage("x1", 1, "other")], true);

    expect(ids()).toEqual(["x1"]);
  });
});

describe("message merge (optimistic user replacement)", () => {
  it("drops the optimistic-user placeholder when a real user message arrives", () => {
    // Seed an optimistic user message directly into the cache (as createChat does).
    setMessagesInCache(CHAT, [
      {
        id: "optimistic-user-abc",
        chatId: CHAT,
        role: MessageRole.USER,
        contentBlocks: [],
        seq: 999998n,
        thread: "",
        sequenceNumber: 0n,
        attachments: [],
        streamingState: StreamingState.COMPLETE,
      } as never,
    ]);

    useChatStore.getState().processChatStreamUpdates(CHAT, [userMessage("real-1", 1)]);

    const list = ids();
    expect(list).toContain("real-1");
    expect(list.some((id) => id.startsWith("optimistic-user-"))).toBe(false);
  });
});
