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

function assistantMessage(id: string, ordinal: number, text: string): ChatUpdate {
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
      ordinal: BigInt(ordinal),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

function userMessage(id: string, ordinal: number): ChatUpdate {
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
      ordinal: BigInt(ordinal),
      thread: "",
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
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
        ordinal: 999998n,
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
