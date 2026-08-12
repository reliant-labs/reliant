// Regression test for the "slot theft" variant of the stuck-chat bug: the
// user has chat A open, some other component (WorkflowBuilderChat, a
// reconnect that forwards a stale id) steals the single subscription slot,
// and nothing re-asserts A afterward. The rendered chat is the source of
// truth; reconcileChatSubscription is the mechanism that keeps the
// subscription honest without polling — callers invoke it on every render of
// the active chat and on connection-state changes.
import { beforeEach, describe, expect, it, vi } from "vitest";

type MockService = {
  isConnected: () => boolean;
  getSubscribedChatId: () => string | undefined;
  subscribeToChatDetails: ReturnType<typeof vi.fn>;
  unsubscribeFromChatDetails: ReturnType<typeof vi.fn>;
  start: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
  connected: boolean;
  subscribed: string | undefined;
};

function makeService(): MockService {
  const svc: MockService = {
    connected: true,
    subscribed: undefined,
    isConnected: () => svc.connected,
    getSubscribedChatId: () => svc.subscribed,
    subscribeToChatDetails: vi.fn((chatId: string) => {
      svc.subscribed = chatId;
    }),
    unsubscribeFromChatDetails: vi.fn(() => {
      svc.subscribed = undefined;
    }),
    start: vi.fn((_seq: number, chatId?: string) => {
      svc.subscribed = chatId;
    }),
    stop: vi.fn(),
  };
  return svc;
}

vi.mock("../../api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => makeService()),
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

beforeEach(() => {
  useGlobalUpdatesStore.setState({
    wsService: null,
    subscribedChatId: null,
    connectionStatus: "disconnected",
    lastSequence: 0,
  });
});

describe("reconcileChatSubscription", () => {
  it("re-subscribes to the rendered chat when another chat holds the slot", () => {
    const svc = makeService();
    // Steady state: A is rendered and was subscribed, then B stole the slot
    // (e.g. WorkflowBuilderChat subscribing to its own chat) without ever
    // giving it back.
    useGlobalUpdatesStore.setState({
      wsService: svc as never,
      subscribedChatId: CHAT_B,
      connectionStatus: "connected",
    });
    svc.subscribed = CHAT_B;

    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT_A);

    expect(svc.subscribeToChatDetails).toHaveBeenCalledWith(CHAT_A);
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_A);
    expect(svc.getSubscribedChatId()).toBe(CHAT_A);
  });

  it("does not resubscribe when the rendered chat already matches the subscription (no loop)", () => {
    const svc = makeService();
    svc.subscribed = CHAT_A;
    useGlobalUpdatesStore.setState({
      wsService: svc as never,
      subscribedChatId: CHAT_A,
      connectionStatus: "connected",
    });

    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT_A);
    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT_A);
    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT_A);

    expect(svc.subscribeToChatDetails).not.toHaveBeenCalled();
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_A);
  });

  it("is a no-op for a null rendered chat id", () => {
    const svc = makeService();
    svc.subscribed = CHAT_B;
    useGlobalUpdatesStore.setState({
      wsService: svc as never,
      subscribedChatId: CHAT_B,
      connectionStatus: "connected",
    });

    useGlobalUpdatesStore.getState().reconcileChatSubscription(null);

    expect(svc.subscribeToChatDetails).not.toHaveBeenCalled();
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_B);
  });

  it("re-subscribes after a reconnect drops the service-side subscription even though the store's id looks unchanged", () => {
    const svc = makeService();
    // The store still thinks it's subscribed to A, but the service lost it
    // (e.g. a reconnect raced and the new connection came up unsubscribed).
    svc.subscribed = undefined;
    useGlobalUpdatesStore.setState({
      wsService: svc as never,
      subscribedChatId: CHAT_A,
      connectionStatus: "connected",
    });

    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT_A);

    expect(svc.subscribeToChatDetails).toHaveBeenCalledWith(CHAT_A);
    expect(svc.getSubscribedChatId()).toBe(CHAT_A);
  });
});
