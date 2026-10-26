// Regression tests for the "stuck chat" bug: a chat stays on screen but stops
// receiving live updates, and only opening a *different* chat unsticks it.
//
// The chat stream is the sole push path for chat content, so a chat whose
// subscription is silently dropped looks alive — the user stream stays
// connected and heartbeating — while no messages arrive. Reselecting the same chat does not help, because selectChat
// early-returns on an unchanged activeChatId.
import { beforeEach, describe, expect, it, vi } from "vitest";

const serviceInstances: MockService[] = [];

type MockService = {
  isConnected: () => boolean;
  getChatId: () => string | undefined;
  subscribeToChat: ReturnType<typeof vi.fn>;
  unsubscribe: ReturnType<typeof vi.fn>;
  setProjectScope: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
  connected: boolean;
  subscribed: string | undefined;
};

function makeService(): MockService {
  const svc: MockService = {
    connected: true,
    subscribed: undefined,
    isConnected: () => svc.connected,
    getChatId: () => svc.subscribed,
    // Mirror the real service: subscribing records the chat locally.
    subscribeToChat: vi.fn((chatId: string) => {
      svc.subscribed = chatId;
    }),
    unsubscribe: vi.fn(() => {
      svc.subscribed = undefined;
    }),
    setProjectScope: vi.fn(),
    stop: vi.fn(),
  };
  return svc;
}

vi.mock("../../api/chat-streaming-grpc", () => ({
  ChatStreamingService: vi.fn().mockImplementation(() => {
    const svc = makeService();
    serviceInstances.push(svc);
    return svc;
  }),
}));
vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({ isConnected: () => false, start: vi.fn(), stop: vi.fn() })),
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

import { useGlobalUpdatesStore } from "../globalUpdatesStore";

const CHAT_A = "aaaaaaaa-1111-2222-3333-444444444444";
const CHAT_B = "bbbbbbbb-5555-6666-7777-888888888888";

/** Put the store in the steady state of "viewing CHAT_A over a live stream". */
function subscribedToChatA(): MockService {
  const svc = makeService();
  serviceInstances.push(svc);
  useGlobalUpdatesStore.setState({
    chatStream: svc as never,
    subscribedChatId: null,
    connectionStatus: "connected",
  });
  useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_A);
  svc.subscribeToChat.mockClear();
  return svc;
}

beforeEach(() => {
  serviceInstances.length = 0;
  useGlobalUpdatesStore.setState({
    wsService: null,
    chatStream: null,
    subscribedChatId: null,
    connectionStatus: "disconnected",
    lastSequence: 0,
  });
});

describe("unsubscribeFromChatDetails ownership", () => {
  it("ignores an unsubscribe naming a chat that is no longer subscribed", () => {
    const svc = subscribedToChatA();

    // The user navigated A -> B; B is now the live subscription.
    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_B);
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_B);

    // A's effect cleanup lands late. It must not tear down B's subscription.
    useGlobalUpdatesStore.getState().unsubscribeFromChatDetails(CHAT_A);

    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_B);
    expect(svc.unsubscribe).not.toHaveBeenCalled();
    expect(svc.getChatId()).toBe(CHAT_B);
  });

  it("still unsubscribes when the caller owns the current subscription", () => {
    const svc = subscribedToChatA();

    useGlobalUpdatesStore.getState().unsubscribeFromChatDetails(CHAT_A);

    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBeNull();
    expect(svc.unsubscribe).toHaveBeenCalledTimes(1);
  });

  it("keeps the argument-less form working for deselect and logout", () => {
    const svc = subscribedToChatA();

    useGlobalUpdatesStore.getState().unsubscribeFromChatDetails();

    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBeNull();
    expect(svc.unsubscribe).toHaveBeenCalledTimes(1);
  });

  it("survives a remount cycle that unsubscribes after the resubscribe", () => {
    // React can run the previous effect's cleanup after the next effect body.
    // Re-entering the same chat must not leave it unsubscribed.
    const svc = subscribedToChatA();

    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_A);
    useGlobalUpdatesStore.getState().unsubscribeFromChatDetails(CHAT_A);
    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_A);

    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_A);
    expect(svc.getChatId()).toBe(CHAT_A);
  });
});

describe("subscribeToChatDetails recovery", () => {
  it("retries when the store thinks it is subscribed but the service is not", () => {
    // The precise stuck state: an optimistic subscribedChatId with no
    // corresponding subscription on the service. Retrying must not no-op.
    const svc = makeService();
    serviceInstances.push(svc);
    svc.subscribed = undefined;
    useGlobalUpdatesStore.setState({
      chatStream: svc as never,
      subscribedChatId: CHAT_A,
      connectionStatus: "connected",
    });

    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_A);

    expect(svc.subscribeToChat).toHaveBeenCalledWith(CHAT_A);
    expect(svc.getChatId()).toBe(CHAT_A);
  });

  it("resubscribes when the stream is subscribed but disconnected", () => {
    const svc = subscribedToChatA();
    svc.connected = false;

    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_A);

    expect(svc.subscribeToChat).toHaveBeenCalledWith(CHAT_A);
  });

  it("no-ops when the service genuinely holds the subscription", () => {
    const svc = subscribedToChatA();

    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT_A);

    expect(svc.subscribeToChat).not.toHaveBeenCalled();
  });
});
