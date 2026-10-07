/**
 * The mobile shell owns the live update stream, as ModernApp does on desktop.
 *
 * Before, nothing on `/m/*` connected it. It came up only as a side effect of
 * a screen subscribing to a chat AFTER the chat list had loaded — so the chat
 * list never went live, and a chat (or its workflow view) opened directly by
 * URL subscribed too early, was deferred by globalUpdatesStore.connect, and
 * never connected at all.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";
import { useEffect } from "react";

type MockService = {
  isConnected: () => boolean;
  getSubscribedChatId: () => string | undefined;
  subscribeToChatDetails: ReturnType<typeof vi.fn>;
  unsubscribeFromChatDetails: ReturnType<typeof vi.fn>;
  start: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
  subscribed: string | undefined;
};

const services: MockService[] = [];

vi.mock("../../../api/streaming-grpc", () => ({
  UserStreamingService: class implements MockService {
    subscribed: string | undefined = undefined;
    isConnected = () => false;
    getSubscribedChatId = () => this.subscribed;
    subscribeToChatDetails = vi.fn((chatId: string) => {
      this.subscribed = chatId;
    });
    unsubscribeFromChatDetails = vi.fn();
    start = vi.fn((_seq: number, chatId?: string) => {
      this.subscribed = chatId;
    });
    stop = vi.fn();
    constructor() {
      services.push(this);
    }
  },
}));

vi.mock("../../../lib/notifications", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../lib/notifications")>()),
  getNotificationPermission: () => "denied" as const,
}));

// The routed screen. Each test picks what stands in for it.
let screen: () => React.ReactNode = () => null;

vi.mock("@tanstack/react-router", () => ({
  Outlet: () => <>{screen()}</>,
  useNavigate: () => vi.fn(),
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCurrentUser: () => ({ data: { onboardingCompleted: true }, isLoading: false }),
}));

vi.mock("../MobileNavDrawer", () => ({ MobileNavDrawer: () => null }));
// Reads the live router, which this file replaces with a two-export stub.
vi.mock("../MobileNavigationFeedback", () => ({ MobileNavigationFeedback: () => null }));

import { MobileShell } from "../MobileShell";
import { useChatStore } from "../../../store/chatStore";
import { useGlobalUpdatesStore } from "../../../store/globalUpdatesStore";
import { useProjectStore } from "../../../store/projectStore";
import type { Project } from "../../../types";

const CHAT_ID = "aaaaaaaa-1111-2222-3333-444444444444";
const project = { id: "proj-1", name: "shopfront", path: "/p" } as unknown as Project;

/** A chat screen asserts its chat the way ChatContainer does. */
function ChatScreen() {
  const connectionStatus = useGlobalUpdatesStore((s) => s.connectionStatus);
  useEffect(() => {
    useGlobalUpdatesStore.getState().reconcileChatSubscription(CHAT_ID);
  }, [connectionStatus]);
  return <div>chat</div>;
}

beforeEach(() => {
  services.length = 0;
  screen = () => null;
  useGlobalUpdatesStore.setState({
    wsService: null,
    subscribedChatId: null,
    connectionStatus: "disconnected",
    lastSequence: 0,
  });
  useChatStore.setState({ hasLoaded: false });
  // A project is already selected, so the shell's first-project fallback
  // (selectProject → loadChats) does not run; the tests load chats by hand.
  useProjectStore.setState({ projects: [project], currentProject: project });
});

afterEach(() => {
  cleanup();
});

/** What loadChats leaves behind: the list is loaded, the resume point known. */
function chatsLoad() {
  useGlobalUpdatesStore.setState({ lastSequence: 42 });
  useChatStore.setState({ hasLoaded: true });
}

describe("MobileShell update stream", () => {
  it("connects the stream on a screen that subscribes to no chat, once chats have loaded", async () => {
    screen = () => <div>chat list</div>;

    await act(async () => {
      render(<MobileShell />);
    });
    // Nothing to resume from yet: connecting now would replay from zero.
    expect(services).toHaveLength(0);

    await act(async () => {
      chatsLoad();
    });

    expect(services).toHaveLength(1);
    expect(services[0]!.start).toHaveBeenCalledWith(42, undefined, 0, "proj-1");
  });

  it("connects a chat opened directly by URL, with its subscription on the first request", async () => {
    // The chat screen mounts before the chat list has loaded, so its
    // subscription is recorded but the stream it asks for is deferred.
    screen = () => <ChatScreen />;

    await act(async () => {
      render(<MobileShell />);
    });
    expect(services).toHaveLength(0);
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_ID);

    await act(async () => {
      chatsLoad();
    });

    expect(services).toHaveLength(1);
    expect(services[0]!.start).toHaveBeenCalledWith(42, CHAT_ID, 0, "proj-1");
  });

  it("disconnects the stream when the shell goes away", async () => {
    chatsLoad();
    let unmount = () => {};
    await act(async () => {
      ({ unmount } = render(<MobileShell />));
    });
    expect(services).toHaveLength(1);

    await act(async () => {
      unmount();
    });

    expect(services[0]!.stop).toHaveBeenCalled();
    expect(useGlobalUpdatesStore.getState().wsService).toBeNull();
  });
});
