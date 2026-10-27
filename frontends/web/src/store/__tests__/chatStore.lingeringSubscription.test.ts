import { beforeEach, describe, expect, it, vi } from "vitest";
import { useChatStore } from "../chatStore";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";
import { useProjectStore } from "../projectStore";

// The chat stream can only change which chat it carries by reconnecting. Leaving
// a chat for the new-chat view (desktop) or the chat list (mobile) used to
// drop the subscription — one reconnect — and coming back to the same chat
// added a second, for a chat whose state never left the cache. These pin the
// replacement: a deselected chat stays subscribed until another chat takes
// the chat stream, or the chat itself goes away.

type MockService = {
  connected: boolean;
  subscribed: string | undefined;
  isConnected: () => boolean;
  getChatId: () => string | undefined;
  subscribeToChat: ReturnType<typeof vi.fn>;
  unsubscribe: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
};

function liveServiceOn(chatId: string): MockService {
  const svc: MockService = {
    connected: true,
    subscribed: chatId,
    isConnected: () => svc.connected,
    getChatId: () => svc.subscribed,
    subscribeToChat: vi.fn((id: string) => {
      svc.subscribed = id;
    }),
    unsubscribe: vi.fn(() => {
      svc.subscribed = undefined;
    }),
    stop: vi.fn(),
  };
  useGlobalUpdatesStore.setState({
    // A connected user stream: subscribing never needs to connect one.
    wsService: { isConnected: () => true, stop: vi.fn() } as never,
    chatStream: svc as never,
    subscribedChatId: chatId,
    connectionStatus: "connected",
  });
  return svc;
}

const CHAT = "aaaaaaaa-1111-2222-3333-444444444444";
const OTHER = "bbbbbbbb-5555-6666-7777-888888888888";

beforeEach(() => {
  useProjectStore.setState({ currentProject: null } as never);
  useChatStore.setState({ activeChatId: null });
});

describe("leaving a chat", () => {
  it("keeps the stream on it: no reconnect on the way out", () => {
    const svc = liveServiceOn(CHAT);
    useChatStore.setState({ activeChatId: CHAT });

    useChatStore.getState().clearCurrentChat();

    expect(useChatStore.getState().activeChatId).toBeNull();
    expect(svc.unsubscribe).not.toHaveBeenCalled();
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT);
  });

  it("makes coming back to the same chat free: no reconnect on the way in", () => {
    const svc = liveServiceOn(CHAT);
    useChatStore.setState({ activeChatId: CHAT });
    useChatStore.getState().clearCurrentChat();

    // What ChatContainer does on mount for the chat it renders.
    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT);

    expect(svc.subscribeToChat).not.toHaveBeenCalled();
    expect(svc.unsubscribe).not.toHaveBeenCalled();
  });

  it("opening a different chat replaces it in a single switch", () => {
    const svc = liveServiceOn(CHAT);
    useChatStore.setState({ activeChatId: CHAT });
    useChatStore.getState().clearCurrentChat();

    useGlobalUpdatesStore.getState().reconcileChatSubscription(OTHER);

    expect(svc.unsubscribe).not.toHaveBeenCalled();
    expect(svc.subscribeToChat).toHaveBeenCalledTimes(1);
    expect(svc.subscribeToChat).toHaveBeenCalledWith(OTHER);
  });
});

describe("evicting a chat", () => {
  it("releases the subscription it still holds", () => {
    const svc = liveServiceOn(CHAT);

    useChatStore.getState().evictChat(CHAT);

    expect(svc.unsubscribe).toHaveBeenCalledTimes(1);
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBeNull();
  });

  it("leaves another chat's subscription alone", () => {
    const svc = liveServiceOn(CHAT);

    useChatStore.getState().evictChat(OTHER);

    expect(svc.unsubscribe).not.toHaveBeenCalled();
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT);
  });
});

describe("a chat the server refused", () => {
  it("is not recorded as subscribed when the service declines it", () => {
    const svc = liveServiceOn(CHAT);
    // The real service ignores a chat it has seen refused.
    svc.subscribeToChat.mockImplementation(() => undefined);

    useGlobalUpdatesStore.getState().subscribeToChatDetails(OTHER);

    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT);
  });
});
