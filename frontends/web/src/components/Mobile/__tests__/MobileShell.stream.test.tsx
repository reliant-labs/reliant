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
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { useEffect } from "react";

type MockUserService = {
  isConnected: () => boolean;
  start: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
};

type MockChatService = {
  isConnected: () => boolean;
  getChatId: () => string | undefined;
  subscribeToChat: ReturnType<typeof vi.fn>;
  unsubscribe: ReturnType<typeof vi.fn>;
  setProjectScope: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
  subscribed: string | undefined;
};

const services: MockUserService[] = [];
const chatServices: MockChatService[] = [];

vi.mock("../../../api/streaming-grpc", () => ({
  UserStreamingService: class implements MockUserService {
    isConnected = () => false;
    start = vi.fn();
    stop = vi.fn();
    constructor() {
      services.push(this);
    }
  },
}));

vi.mock("../../../api/chat-streaming-grpc", () => ({
  ChatStreamingService: class implements MockChatService {
    subscribed: string | undefined = undefined;
    isConnected = () => false;
    getChatId = () => this.subscribed;
    subscribeToChat = vi.fn((chatId: string) => {
      this.subscribed = chatId;
    });
    unsubscribe = vi.fn();
    setProjectScope = vi.fn();
    stop = vi.fn();
    constructor() {
      chatServices.push(this);
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
vi.mock("../MobileInboxBadgeSync", () => ({ MobileInboxBadgeSync: () => null }));

import { QueryClientProvider } from "@tanstack/react-query";
import { MobileShell } from "../MobileShell";
import { queryClient } from "../../../lib/query-client";
import { seedProjects } from "../../../test/projectList";
import { useChatStore } from "../../../store/chatStore";
import { useGlobalUpdatesStore } from "../../../store/globalUpdatesStore";
import { useProjectStore } from "../../../store/projectStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import type { Project } from "../../../store/projectStore";

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
  chatServices.length = 0;
  screen = () => null;
  useGlobalUpdatesStore.setState({
    wsService: null,
    chatStream: null,
    subscribedChatId: null,
    connectionStatus: "disconnected",
    lastSequence: 0,
  });
  // The shell loads the chat list itself (every project's); here that load is
  // a no-op the tests complete by hand (chatsLoad).
  useChatStore.setState({ hasLoaded: false, loadChats: vi.fn(async () => {}) });
  // A project is already selected, so the shell's initial-project restore
  // does not run.
  seedProjects(queryClient, [project]);
  useProjectStore.setState({ currentProject: project });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
});

/** The shell as the app mounts it: under the app's query client. */
function renderShell() {
  return render(
    <QueryClientProvider client={queryClient}>
      <MobileShell />
    </QueryClientProvider>,
  );
}

/** What loadChats leaves behind: the list is loaded, the resume point known. */
function chatsLoad() {
  useGlobalUpdatesStore.setState({ lastSequence: 42 });
  useChatStore.setState({ hasLoaded: true });
}

describe("MobileShell update stream", () => {
  it("connects the stream on a screen that subscribes to no chat, once chats have loaded", async () => {
    screen = () => <div>chat list</div>;

    await act(async () => {
      renderShell();
    });
    // Nothing to resume from yet: connecting now would replay from zero.
    expect(services).toHaveLength(0);

    await act(async () => {
      chatsLoad();
    });

    expect(services).toHaveLength(1);
    // No project scope: the stream carries every project's updates.
    expect(services[0]!.start).toHaveBeenCalledWith(42);
    expect(chatServices).toHaveLength(0);
  });

  it("streams a chat opened directly by URL at once, and connects the user stream once chats have loaded", async () => {
    // The chat screen mounts before the chat list has loaded. Its chat
    // stream needs nothing from the list, so it opens now; the user stream
    // waits for the resume point the list brings.
    screen = () => <ChatScreen />;

    await act(async () => {
      renderShell();
    });
    expect(services).toHaveLength(0);
    expect(chatServices).toHaveLength(1);
    expect(chatServices[0]!.subscribeToChat).toHaveBeenCalledWith(CHAT_ID);
    expect(useGlobalUpdatesStore.getState().subscribedChatId).toBe(CHAT_ID);

    await act(async () => {
      chatsLoad();
    });

    expect(services).toHaveLength(1);
    expect(services[0]!.start).toHaveBeenCalledWith(42);
    expect(chatServices[0]!.setProjectScope).toHaveBeenCalledWith(undefined);
    // Connecting the user stream did not reopen the chat stream.
    expect(chatServices[0]!.subscribeToChat).toHaveBeenCalledTimes(1);
  });

  it("disconnects the stream when the shell goes away", async () => {
    chatsLoad();
    let unmount = () => {};
    await act(async () => {
      ({ unmount } = renderShell());
    });
    expect(services).toHaveLength(1);

    await act(async () => {
      unmount();
    });

    expect(services[0]!.stop).toHaveBeenCalled();
    expect(useGlobalUpdatesStore.getState().wsService).toBeNull();
    expect(useGlobalUpdatesStore.getState().chatStream).toBeNull();
  });
});

describe("MobileShell initial project", () => {
  const other = { id: "proj-2", name: "backoffice", path: "/q" } as unknown as Project;

  beforeEach(() => {
    seedProjects(queryClient, [project, other]);
    useProjectStore.setState({ currentProject: null });
    useWorkspaceStateStore.setState({ lastProjectId: null });
  });

  it("restores the last project the user had open instead of the first one", async () => {
    useWorkspaceStateStore.setState({ lastProjectId: "proj-2" });
    const selectProject = vi.fn(async (p: Project) => useProjectStore.setState({ currentProject: p }));
    useProjectStore.setState({ selectProject });

    await act(async () => {
      renderShell();
    });

    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("proj-2"));
    expect(selectProject).toHaveBeenCalledTimes(1);
    expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "proj-2" }), { skipWorkspaceStateSave: true, restoreWorkspace: false, isCurrent: expect.any(Function) });
  });

  it("falls back to the most recent project without remembering it as the user's choice", async () => {
    const selectProject = vi.fn(async (p: Project) => useProjectStore.setState({ currentProject: p }));
    useProjectStore.setState({ selectProject });

    await act(async () => {
      renderShell();
    });

    await waitFor(() => expect(useProjectStore.getState().currentProject?.id).toBe("proj-1"));
    expect(selectProject).toHaveBeenCalledWith(expect.objectContaining({ id: "proj-1" }), { skipWorkspaceStateSave: true, restoreWorkspace: false, isCurrent: expect.any(Function) });
  });
});
