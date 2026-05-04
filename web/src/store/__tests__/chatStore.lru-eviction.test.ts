import { beforeEach, describe, expect, it, vi } from "vitest";

import { useChatStore } from "../chatStore";
import { useActivityStore } from "../activityStore";
import { useThreadActivityStore } from "../threadActivityStore";
import type { Chat, Message } from "../../types/chat";
import { ContentBlockType, MessageRole, StreamingState } from "../../types/chat";
import type { ChatUpdate } from "../../types/streaming";

vi.mock("../../api/client", () => ({
  api: {
    chatsV2: {
      list: vi.fn(async () => []),
      listMessages: vi.fn(async () => ({ messages: [], total: 0, hasMore: false, oldestOrdinal: 0 })),
      get: vi.fn(async () => ({})),
      sendMessage: vi.fn(async () => ({})),
      cancel: vi.fn(async () => ({ success: true })),
      pause: vi.fn(async () => ({ success: true })),
      resume: vi.fn(async () => ({ success: true })),
      delete: vi.fn(async () => ({ permanently_deleted: true })),
      update: vi.fn(async () => ({})),
      create: vi.fn(async () => ({})),
      branch: vi.fn(async () => ({ chat: {} })),
      listArchived: vi.fn(async () => []),
      dismiss: vi.fn(async () => ({ changed: false })),
      markUnread: vi.fn(async () => ({ changed: false })),
      compact: vi.fn(async () => ({})),
      updateWorkflowParams: vi.fn(async () => ({})),
    },
    approvals: {
      listByChat: vi.fn(async () => []),
      approve: vi.fn(async () => ({})),
      deny: vi.fn(async () => ({})),
      batchApprove: vi.fn(async () => ({})),
      batchDeny: vi.fn(async () => ({})),
    },
    toolCalls: {
      cancel: vi.fn(async () => ({})),
      convertToBackground: vi.fn(async () => ({ process_id: "proc-1" })),
    },
  },
}));

function buildTestChat(chatId: string, title = "Chat"): Chat {
  return {
    id: chatId,
    userId: "user-1",
    projectId: "project-1",
    title,
    state: 1,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    lastActive: new Date().toISOString(),
  } as Chat;
}

function buildTestMessage(msgId: string, chatId: string): Message {
  return {
    id: msgId,
    chatId,
    role: MessageRole.USER,
    contentBlocks: [],
    ordinal: BigInt(1),
    createdAt: new Date().toISOString(),
    threadId: chatId,
  } as Message;
}

/** Select a chat and populate it with test messages so we can verify eviction. */
function selectAndPopulate(chatId: string, messageCount = 3): void {
  const store = useChatStore.getState();
  const chat = buildTestChat(chatId);

  // Initialize the chat in the store
  store.selectChat(chat);

  // Populate with messages (simulates data loaded via subscription)
  const messages = Array.from({ length: messageCount }, (_, i) =>
    buildTestMessage(`${chatId}-msg-${i}`, chatId),
  );
  useChatStore.setState((state) => ({
    messages: { ...state.messages, [chatId]: messages },
    processedMessages: {
      ...state.processedMessages,
      [chatId]: new Map(messages.map((m) => [m.id, { text: "test", toolExecutions: [] }])),
    },
    errorEvents: { ...state.errorEvents, [chatId]: [{ id: "err-1", message: "test error" }] },
    runOutputs: { ...state.runOutputs, [chatId]: [{ unique_activity_id: "run-1", stdout: "hello" }] },
  }));
}

describe("chatStore LRU eviction", () => {
  beforeEach(() => {
    useChatStore.getState().reset();
    useActivityStore.setState({ activities: new Map() });
    useThreadActivityStore.setState({ threads: {} });
  });

  it("keeps data for the last 5 chats", () => {
    // Select 5 chats — all should retain their data
    for (let i = 1; i <= 5; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    const state = useChatStore.getState();
    for (let i = 1; i <= 5; i++) {
      expect(state.messages[`chat-${i}`]).toBeDefined();
      expect(state.messages[`chat-${i}`]!.length).toBe(3);
    }
  });

  it("evicts the oldest chat when opening a 6th", () => {
    // Select 6 chats — chat-1 should be evicted
    for (let i = 1; i <= 6; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    const state = useChatStore.getState();

    // chat-1 data should be evicted
    expect(state.messages["chat-1"]).toBeUndefined();
    expect(state.processedMessages["chat-1"]).toBeUndefined();
    expect(state.errorEvents["chat-1"]).toBeUndefined();
    expect(state.runOutputs["chat-1"]).toBeUndefined();

    // chat-1 metadata should still exist (for sidebar)
    expect(state.chats.has("chat-1")).toBe(true);

    // chats 2-6 should still have data
    for (let i = 2; i <= 6; i++) {
      expect(state.messages[`chat-${i}`]).toBeDefined();
    }
  });

  it("re-accessing a chat moves it to front and evicts the new oldest", () => {
    // Select chats 1-5
    for (let i = 1; i <= 5; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    // Re-select chat-1 (moves it to front)
    useChatStore.getState().selectChat(buildTestChat("chat-1"));

    // Now select chat-6 — chat-2 should be evicted (it's now the oldest)
    selectAndPopulate("chat-6");

    const state = useChatStore.getState();
    expect(state.messages["chat-2"]).toBeUndefined();
    // chat-1 should still have data (it was re-accessed)
    expect(state.messages["chat-1"]).toBeDefined();
  });

  it("active chat is never evicted", () => {
    // Select 5 chats — chat-5 is active
    for (let i = 1; i <= 5; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    // Verify chat-5 is active
    expect(useChatStore.getState().activeChatId).toBe("chat-5");

    // Select chat-6, chat-7, chat-8, chat-9, chat-10
    for (let i = 6; i <= 10; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    // The active chat at each step should always have data
    const state = useChatStore.getState();
    expect(state.activeChatId).toBe("chat-10");
    expect(state.messages["chat-10"]).toBeDefined();
  });

  it("re-selecting an evicted chat initializes empty state for reload", () => {
    // Select 6 chats — chat-1 is evicted
    for (let i = 1; i <= 6; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    expect(useChatStore.getState().messages["chat-1"]).toBeUndefined();

    // Re-select chat-1
    useChatStore.getState().selectChat(buildTestChat("chat-1"));

    // Should have empty messages array (waiting for subscription to populate)
    const state = useChatStore.getState();
    expect(state.messages["chat-1"]).toBeDefined();
    expect(state.messages["chat-1"]!.length).toBe(0);
  });

  it("deleting a chat removes it from LRU", () => {
    // Select 3 chats
    for (let i = 1; i <= 3; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    // Clean up chat-2 (simulates deletion)
    useChatStore.getState().cleanupChatState("chat-2");

    // Now select chats 4, 5, 6 — total unique chats in LRU: 1, 3, 4, 5, 6 (5 total)
    // chat-1 should NOT be evicted because chat-2 was removed from LRU
    for (let i = 4; i <= 6; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    const state = useChatStore.getState();
    // chat-1 should still have data (only 5 in LRU: 1, 3, 4, 5, 6)
    expect(state.messages["chat-1"]).toBeDefined();
  });

  it("reset() clears LRU so no eviction references remain", () => {
    for (let i = 1; i <= 5; i++) {
      selectAndPopulate(`chat-${i}`);
    }

    useChatStore.getState().reset();

    const state = useChatStore.getState();
    // All data should be gone
    expect(state.messages).toEqual({});
    expect(state.processedMessages).toEqual({});
    expect(state.chats.size).toBe(0);

    // Selecting a new chat after reset should work fine (no stale LRU)
    selectAndPopulate("chat-new");
    expect(useChatStore.getState().messages["chat-new"]).toBeDefined();
  });

  it("preserves message attachments from incremental stream updates", () => {
    const chatId = "chat-attachments-incremental";
    const messageId = "msg-with-attachment";

    useChatStore.setState((state) => ({
      chats: new Map(state.chats).set(chatId, buildTestChat(chatId)),
      chatOrder: [chatId],
      activeChatId: chatId,
      messages: { ...state.messages, [chatId]: [] },
    }));

    const updates: ChatUpdate[] = [
      {
        update_type: "message",
        message: {
          id: messageId,
          chatId,
          role: MessageRole.USER,
          ordinal: BigInt(1),
          thread: chatId,
          streamingState: StreamingState.COMPLETE,
          contentBlocks: [
            {
              id: "block-1",
              index: 0,
              type: ContentBlockType.TEXT,
              content: "see attached image",
            },
          ],
          attachments: [
            {
              id: "att-1",
              filename: "screenshot.png",
              size: "123",
              mime_type: "image/png",
              url: "/api/attachments/att-1",
            },
          ],
          createdAt: "2024-01-01T00:00:00.000Z",
          updatedAt: "2024-01-01T00:00:00.000Z",
          sequenceNumber: BigInt(1),
        },
      },
    ] as ChatUpdate[];

    useChatStore.getState().processChatStreamUpdates(chatId, updates, false);

    const storedMessage = useChatStore
      .getState()
      .messages[chatId]?.find((message) => message.id === messageId);

    expect(storedMessage?.attachments).toHaveLength(1);
    expect(storedMessage?.attachments?.[0]).toEqual(
      expect.objectContaining({
        id: "att-1",
        filename: "screenshot.png",
        size: BigInt(123),
        mimeType: "image/png",
      }),
    );
  });

  it("preserves message attachments from chat snapshots", () => {
    const chatId = "chat-attachments-snapshot";
    const messageId = "msg-with-snapshot-attachment";

    useChatStore.setState((state) => ({
      chats: new Map(state.chats).set(chatId, buildTestChat(chatId)),
      chatOrder: [chatId],
      activeChatId: chatId,
      messages: { ...state.messages, [chatId]: [] },
    }));

    const updates: ChatUpdate[] = [
      {
        update_type: "message",
        message: {
          id: messageId,
          chatId,
          role: MessageRole.USER,
          ordinal: BigInt(1),
          thread: chatId,
          streamingState: StreamingState.COMPLETE,
          contentBlocks: [
            {
              id: "block-1",
              index: 0,
              type: ContentBlockType.TEXT,
              content: "see attached image after reload",
            },
          ],
          attachments: [
            {
              id: "att-1",
              filename: "screenshot.png",
              size: BigInt(123),
              mimeType: "image/png",
              url: "/api/attachments/att-1",
            },
          ],
          createdAt: "2024-01-01T00:00:00.000Z",
          updatedAt: "2024-01-01T00:00:00.000Z",
          sequenceNumber: BigInt(1),
        },
      },
    ] as ChatUpdate[];

    useChatStore.getState().processChatStreamUpdates(chatId, updates, true);

    const storedMessage = useChatStore.getState().messages[chatId]?.[0];

    expect(storedMessage?.id).toBe(messageId);
    expect(storedMessage?.attachments).toHaveLength(1);
    expect(storedMessage?.attachments?.[0]).toEqual(
      expect.objectContaining({
        id: "att-1",
        filename: "screenshot.png",
        size: BigInt(123),
        mimeType: "image/png",
      }),
    );
  });

  it("snapshot resets event arrays instead of accumulating", () => {
    const chatId = "chat-snapshot";

    // Set up chat with some existing events
    useChatStore.setState((state) => ({
      chats: new Map(state.chats).set(chatId, buildTestChat(chatId)),
      chatOrder: [chatId],
      activeChatId: chatId,
      messages: { ...state.messages, [chatId]: [] },
      errorEvents: {
        ...state.errorEvents,
        [chatId]: [
          { id: "old-err-1", message: "old error 1" } as any,
          { id: "old-err-2", message: "old error 2" } as any,
        ],
      },
      infoEvents: {
        ...state.infoEvents,
        [chatId]: [{ id: "old-info-1", message: "old info" } as any],
      },
      runOutputs: {
        ...state.runOutputs,
        [chatId]: [{ unique_activity_id: "old-run-1", stdout: "old output" } as any],
      },
      nodeExecutions: {
        ...state.nodeExecutions,
        [chatId]: [{ node_id: "old-node-1", event_type: "started" } as any],
      },
    }));

    // Process a snapshot with new events
    const updates: ChatUpdate[] = [
      {
        update_type: "error",
        id: "new-err-1",
        message: "new error",
        timestamp: new Date().toISOString(),
      } as any,
    ];

    useChatStore.getState().processChatStreamUpdates(chatId, updates, true);

    const state = useChatStore.getState();

    // Should only have the new error, not the old ones
    expect(state.errorEvents[chatId]?.length).toBe(1);
    expect(state.errorEvents[chatId]?.[0]?.id).toBe("new-err-1");

    // Other event arrays should be empty (snapshot with no new events of those types)
    expect(state.infoEvents[chatId]?.length).toBe(0);
    expect(state.runOutputs[chatId]?.length).toBe(0);
    expect(state.nodeExecutions[chatId]?.length).toBe(0);
  });
});
