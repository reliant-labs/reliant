import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { UserUpdateType, EntityType } from "../../gen/reliant/v1/streaming_pb";
import type { Chat } from "../../api/client";
import type { UserUpdate } from "../../types/streaming";

// globalUpdatesStore pulls in the streaming service and OS-notification libs at
// module scope — stub the pieces that would touch the network or Electron.
vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

vi.mock("../../lib/notifications", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/notifications")>();
  return {
    ...actual,
    showWorkflowCompletionNotification: vi.fn(),
    showApprovalRequiredNotification: vi.fn(),
    getNotificationPermission: () => "denied" as const,
  };
});

import { chatKeys } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";
import { useChatStore } from "../chatStore";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";
import { useProjectStore } from "../projectStore";

const now = "2026-01-01T00:00:00.000Z";

type ChatListEnvelope = {
  chats: Chat[];
  total?: number;
  lastUserUpdateSequence: number;
};

function buildChat(overrides: Partial<Chat>): Chat {
  return {
    id: "c1",
    userId: "user-1",
    title: "Old",
    projectId: "p1",
    worktreeId: "wt-1",
    state: ChatState.IDLE,
    createdAt: now,
    updatedAt: now,
    lastActive: now,
    selectedPresets: {},
    needsRecovery: false,
    activity: 0,
    unread: false,
    ...overrides,
  } as Chat;
}

function buildUpdate(overrides: Partial<UserUpdate>): UserUpdate {
  return {
    id: "update-1",
    user_id: "user-1",
    sequence_number: 1,
    chat_id: "c1",
    // proto3 sends "" for unset — exercises the `||` fallback to the
    // projectStore's currentProject.
    project_id: "",
    update_type: UserUpdateType.CHAT_STATE_CHANGE,
    entity_type: EntityType.CHAT,
    entity_id: "c1",
    data: {},
    created_at: now,
    ...overrides,
  };
}

function seedCaches() {
  useProjectStore.setState({
    currentProject: { id: "p1", name: "Project One", path: "/tmp/p1" } as never,
  });

  const chat = buildChat({});
  useChatStore.setState({
    activeChatId: null,
    chats: new Map([["c1", chat]]),
  });

  queryClient.setQueryData(chatKeys.list("p1"), {
    chats: [chat],
    total: 1,
    lastUserUpdateSequence: 7,
  } satisfies ChatListEnvelope);
  queryClient.setQueryData(chatKeys.detail("c1"), chat);
}

function getListEnvelope(): ChatListEnvelope | undefined {
  return queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("p1"));
}

beforeEach(() => {
  queryClient.clear();
  seedCaches();
});

afterEach(() => {
  queryClient.clear();
  useChatStore.setState({ activeChatId: null, chats: new Map() });
  useProjectStore.setState({ currentProject: null });
});

describe("globalUpdatesStore cache patching", () => {
  it("CHAT_STATE_CHANGE patches list + detail caches and the Zustand map without a refetch", () => {
    // parseChatState only recognizes "idle"/"archived" strings (or numeric
    // enum values) — send the numeric enum so a real state change is visible.
    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        update_type: UserUpdateType.CHAT_STATE_CHANGE,
        data: {
          state: ChatState.IDLE,
          previous_state: ChatState.IDLE,
          reason: "workflow_completed",
          unread: true,
        },
      }),
    ]);

    const envelope = getListEnvelope()!;
    const listChat = envelope.chats.find((c) => c.id === "c1")!;
    expect(listChat.state).toBe(ChatState.IDLE);
    expect(listChat.unread).toBe(true);
    expect(envelope.total).toBe(1);
    expect(envelope.lastUserUpdateSequence).toBe(7);

    const detail = queryClient.getQueryData<Chat>(chatKeys.detail("c1"))!;
    expect(detail.unread).toBe(true);

    const mapChat = useChatStore.getState().chats.get("c1")!;
    expect(mapChat.unread).toBe(true);
    expect(mapChat.state).toBe(ChatState.IDLE);
  });

  it("CHAT_STATE_CHANGE resolves projectId from the store when project_id is empty", () => {
    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        project_id: "",
        data: {
          state: "idle",
          previous_state: "idle",
          reason: "test",
          unread: true,
        },
      }),
    ]);

    // The list patch only fires when a projectId resolved — unread landing in
    // the "p1" envelope proves the `||` fallback to currentProject worked.
    expect(getListEnvelope()!.chats[0].unread).toBe(true);
  });

  it("CHAT_TITLE_CHANGED patches the title in list and detail caches", () => {
    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        update_type: UserUpdateType.CHAT_TITLE_CHANGED,
        data: { title: "New Title", previous_title: "Old" },
      }),
    ]);

    expect(getListEnvelope()!.chats[0].title).toBe("New Title");
    expect(
      queryClient.getQueryData<Chat>(chatKeys.detail("c1"))!.title
    ).toBe("New Title");
  });

  it("CHAT_TITLE_CHANGED also updates the Zustand chats map", () => {
    // The Zustand map is read by ChatInput/useActiveChat — leaving it stale
    // while patching only the React Query caches splits the source of truth.
    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        update_type: UserUpdateType.CHAT_TITLE_CHANGED,
        data: { title: "New Title", previous_title: "Old" },
      }),
    ]);

    expect(useChatStore.getState().chats.get("c1")!.title).toBe("New Title");
  });

  it("CHAT_ACTIVITY_CHANGED → IDLE clears leftover streaming state for the chat", () => {
    // The activity-IDLE event is persisted and gap-protected, making it the
    // authoritative "nothing is streaming anymore" signal. Any streaming
    // placeholder still around at that point is a stale tail (deltas race
    // message finalization on a separate channel server-side) and would
    // otherwise render at the end of the chat until a page refresh.
    const phantom = {
      id: "streaming-temp-c1",
      chatId: "c1",
      role: 2,
      contentBlocks: [],
      createdAt: now,
      updatedAt: now,
      thread: "",
      ordinal: BigInt(999999),
      sequenceNumber: BigInt(0),
      attachments: [],
    } as never;
    useChatStore.setState({
      messages: { c1: [phantom] },
      streamingMessages: { c1: { c1: phantom } },
    });

    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        update_type: UserUpdateType.CHAT_ACTIVITY_CHANGED,
        data: { chat_id: "c1", activity: 0, timestamp: now },
      }),
    ]);

    const state = useChatStore.getState();
    expect(state.messages["c1"] || []).toHaveLength(0);
    expect(state.streamingMessages["c1"]).toBeUndefined();
  });

  it("CHAT_DELETED removes the chat from the list envelope, detail cache, and Zustand map", () => {
    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        update_type: UserUpdateType.CHAT_DELETED,
        data: {},
      }),
    ]);

    const envelope = getListEnvelope()!;
    expect(envelope.chats).toHaveLength(0);
    expect(envelope.total).toBe(0);
    expect(queryClient.getQueryData(chatKeys.detail("c1"))).toBeUndefined();
    expect(useChatStore.getState().chats.has("c1")).toBe(false);
  });

  it("CHAT_STATE_CHANGE is a no-op on the list when the chat is not cached", () => {
    const envelopeBefore = getListEnvelope();

    useGlobalUpdatesStore.getState().handleUpdate([
      buildUpdate({
        chat_id: "unknown-chat",
        entity_id: "unknown-chat",
        data: { state: "idle", previous_state: "idle", reason: "test" },
      }),
    ]);

    // Never fabricates entries — envelope is the same reference.
    expect(getListEnvelope()).toBe(envelopeBefore);
  });
});