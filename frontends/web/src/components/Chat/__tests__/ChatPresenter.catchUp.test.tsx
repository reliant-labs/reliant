/**
 * The timeline in view covers itself with a "Loading conversation…" overlay
 * — over the blurred, non-interactive stale transcript — until it has caught
 * up, keyed by the viewed (chat, thread):
 *
 *  - a chat: its cached transcript renders at once, then the stream delivers
 *    what changed (a snapshot, or a replay) and ends with chat_caught_up;
 *  - a side (spawn) thread: its history is a thread-scoped read, and until
 *    it lands the timeline still shows the previous thread.
 *
 * It waits before appearing, so a cached switch shows nothing, and once up it
 * holds long enough not to flicker. A caught-up timeline — streaming or not —
 * never shows it.
 *
 * Driven through the real signals: chatStore.chatSyncPendingId, and the real
 * thread-messages query against the app's query client with only the network
 * call stubbed.
 */

import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactElement } from "react";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { queryClient } from "../../../lib/query-client";
import { ChatPresenter } from "../ChatPresenter";
import type { Message } from "../../../api/client";
import { useChatStore } from "../../../store/chatStore";
import {
  clearAllMessagesCache,
  fanOutMessagesToThreadCaches,
  messageKeys,
} from "../../../hooks/message-queries";
import {
  CATCH_UP_MIN_VISIBLE_MS,
  CATCH_UP_SHOW_DELAY_MS,
} from "../../../lib/timelineCatchUp";

const { listMessagesMock, scrollTimelineToBottom } = vi.hoisted(() => ({
  listMessagesMock: vi.fn(),
  scrollTimelineToBottom: vi.fn(),
}));

vi.mock("../../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../../api/client")>(
    "../../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: { ...actual.api.chatsV2, listMessages: listMessagesMock },
    },
  };
});

vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: () => <div data-testid="chat-input" />,
}));
vi.mock("../ChatThinkingIndicator", () => ({ ChatThinkingIndicator: () => null }));
vi.mock("../ScrollToBottomButton", () => ({ ScrollToBottomButton: () => null }));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => (
    <>{children}</>
  ),
}));
vi.mock("../PermissionsPanel", () => ({ PermissionsPanel: () => null }));
vi.mock("../ChatHeader", () => ({ ChatHeader: () => <div data-testid="chat-header" /> }));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));

// ChatPresenter reads the chat's pending questions to tell whether the thread
// in view is waiting on the user; none is pending here.
vi.mock("../../../hooks/approval-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/approval-queries")
  >("../../../hooks/approval-queries");
  return { ...actual, usePendingQuestions: () => ({ data: [] }) };
});
// A running chat reads its machine; none is needed here.
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [], isLoading: false }),
}));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
// The real timeline hands the presenter its "jump to the end" callback, and
// opens threads (spawn card, pill, header selector) through onSelectThread.
vi.mock("../thread-views", () => ({
  InterleavedTimeline: ({
    messages,
    onResumeFollow,
    onSelectThread,
  }: {
    messages: { id: string }[];
    onResumeFollow?: (cb: () => void) => void;
    onSelectThread?: (threadId: string | null) => void;
  }) => {
    onResumeFollow?.(scrollTimelineToBottom);
    return (
      <div data-testid="timeline" data-ids={messages.map((m) => m.id).join(",")}>
        <button onClick={() => onSelectThread?.(SPAWN)}>open spawn</button>
        <button onClick={() => onSelectThread?.(OTHER_SPAWN)}>open other spawn</button>
        <button onClick={() => onSelectThread?.(null)}>open main</button>
      </div>
    );
  },
}));
// The presenter reads the chat row; this suite needs none of it.
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return { ...actual, useChat: () => ({ data: undefined }) };
});
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => null,
}));

const CHAT = "chat-1";
const SPAWN = "spawn-1";
const OTHER_SPAWN = "spawn-2";

const cached = [{ id: "m1" } as Message];

function threadMessage(threadId: string, id: string): Message {
  return { id, chatId: CHAT, thread: threadId } as Message;
}

function page(messages: Message[]) {
  return { messages, total: messages.length, hasMore: false, oldestSeq: 0 };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

interface Props {
  messages?: Message[];
  busy?: boolean;
  surface?: "desktop" | "mobile";
}

function presenter({ messages = cached, busy = false, surface = "desktop" }: Props): ReactElement {
  return (
    <QueryClientProvider client={queryClient}>
      <SurfaceProvider surface={surface}>
        <ChatPresenter
          messages={messages}
          approvals={[]}
          errorEvents={[]}
          infoEvents={[]}
          runOutputs={[]}
          chatId={CHAT}
          isChatBusy={busy}
          pendingApprovals={[]}
          connectionStatus="connected"
          projectId="project-1"
          onSendMessage={vi.fn(async () => {})}
          onStopStreaming={vi.fn(async () => {})}
        />
      </SurfaceProvider>
    </QueryClientProvider>
  );
}

function setup(initial: Props = {}) {
  const view = render(presenter(initial));
  return { rerender: (next: Props) => view.rerender(presenter(next)) };
}

/** chat_caught_up outstanding (true) or arrived (false) for CHAT. */
function setChatSyncPending(pending: boolean) {
  act(() => {
    useChatStore.setState({ chatSyncPendingId: pending ? CHAT : null });
  });
}

/** Advance fake time, letting query promises and their notifications land. */
async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

async function openThread(label: string) {
  fireEvent.click(screen.getByText(label));
  await advance(0);
}

function cacheThread(threadId: string, messages: Message[]) {
  queryClient.setQueryData(messageKeys.thread(CHAT, threadId), messages);
}

const overlay = () => screen.queryByTestId("timeline-catchup-overlay");
const frame = () => screen.getByTestId("chat-messages");
const status = () => screen.getByTestId("timeline-catchup-status");
const shownIds = () => screen.getByTestId("timeline").getAttribute("data-ids");

beforeEach(() => {
  vi.useFakeTimers();
  scrollTimelineToBottom.mockClear();
  listMessagesMock.mockReset();
  clearAllMessagesCache();
  useChatStore.setState({ chatSyncPendingId: null });
});

afterEach(() => {
  useChatStore.setState({ chatSyncPendingId: null });
  clearAllMessagesCache();
  vi.useRealTimers();
});

describe("ChatPresenter timeline catch-up overlay — opening a chat", () => {
  it("shows nothing for a cached switch that catches up before the delay", async () => {
    setChatSyncPending(true);
    setup();

    await advance(CATCH_UP_SHOW_DELAY_MS - 50);
    setChatSyncPending(false);
    await advance(1000);

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(status()).toBeEmptyDOMElement();
  });

  it("appears after the delay while catching up, over a blurred, inert transcript", async () => {
    setChatSyncPending(true);
    setup();
    expect(overlay()).not.toBeInTheDocument();

    await advance(CATCH_UP_SHOW_DELAY_MS);

    expect(overlay()).toBeInTheDocument();
    expect(overlay()).toHaveClass("backdrop-blur-xs");
    expect(frame()).toHaveAttribute("aria-busy", "true");
    expect(status()).toHaveTextContent("Loading conversation…");
    // The stale transcript stays rendered underneath, but out of reach.
    expect(screen.getByTestId("timeline").closest("[inert]")).not.toBeNull();
    // The composer is not covered.
    expect(screen.getByTestId("chat-input").closest("[inert]")).toBeNull();
  });

  it("shows only the indicator for a cold open with nothing to blur", async () => {
    setChatSyncPending(true);
    setup({ messages: [] });

    await advance(CATCH_UP_SHOW_DELAY_MS);

    expect(overlay()).toBeInTheDocument();
    expect(overlay()).not.toHaveClass("backdrop-blur-xs");
  });

  it("lifts once caught up, after holding long enough not to flicker, and lands at the bottom", async () => {
    setChatSyncPending(true);
    setup();
    await advance(CATCH_UP_SHOW_DELAY_MS);
    expect(overlay()).toBeInTheDocument();

    setChatSyncPending(false);
    await advance(CATCH_UP_MIN_VISIBLE_MS - 50);
    expect(overlay()).toBeInTheDocument();

    await advance(50);
    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(screen.getByTestId("timeline").closest("[inert]")).toBeNull();
    expect(status()).toBeEmptyDOMElement();
    expect(scrollTimelineToBottom).toHaveBeenCalledTimes(1);
  });

  it("stays up without a gap when catch-up resumes during the hold", async () => {
    setChatSyncPending(true);
    setup();
    await advance(CATCH_UP_SHOW_DELAY_MS);

    setChatSyncPending(false);
    await advance(100);
    setChatSyncPending(true);
    await advance(1000);

    expect(overlay()).toBeInTheDocument();
    expect(scrollTimelineToBottom).not.toHaveBeenCalled();
  });

  it("never appears while a caught-up chat streams", async () => {
    const { rerender } = setup({ busy: true });

    // Tokens and tool events keep landing; the chat stays caught up.
    for (let i = 2; i < 12; i++) {
      rerender({ busy: true, messages: [...cached, { id: `m${i}` } as Message] });
      await advance(100);
    }

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(scrollTimelineToBottom).not.toHaveBeenCalled();
  });
});

describe("ChatPresenter timeline catch-up overlay — opening a spawn thread", () => {
  it("covers the previous thread after the delay while the spawn's history loads, and lifts when it lands", async () => {
    const response = deferred<ReturnType<typeof page>>();
    listMessagesMock.mockReturnValueOnce(response.promise);
    setup();

    await openThread("open spawn");
    expect(listMessagesMock).toHaveBeenCalledWith(CHAT, { threadId: SPAWN, recent: undefined });
    expect(overlay()).not.toBeInTheDocument();

    await advance(CATCH_UP_SHOW_DELAY_MS);
    expect(overlay()).toBeInTheDocument();
    // What is under it is the main thread, blurred and out of reach.
    expect(overlay()).toHaveClass("backdrop-blur-xs");
    expect(shownIds()).toBe("m1");
    expect(screen.getByTestId("timeline").closest("[inert]")).not.toBeNull();
    expect(frame()).toHaveAttribute("aria-busy", "true");
    expect(status()).toHaveTextContent("Loading conversation…");

    await act(async () => {
      response.resolve(page([threadMessage(SPAWN, "s1"), threadMessage(SPAWN, "s2")]));
    });
    await advance(CATCH_UP_MIN_VISIBLE_MS);

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(shownIds()).toBe("s1,s2");
    expect(scrollTimelineToBottom).toHaveBeenCalledTimes(1);
  });

  it("shows nothing for a spawn whose history loads before the delay", async () => {
    const response = deferred<ReturnType<typeof page>>();
    listMessagesMock.mockReturnValueOnce(response.promise);
    setup();

    await openThread("open spawn");
    await advance(CATCH_UP_SHOW_DELAY_MS - 50);
    await act(async () => {
      response.resolve(page([threadMessage(SPAWN, "s1")]));
    });
    await advance(1000);

    expect(overlay()).not.toBeInTheDocument();
    expect(shownIds()).toBe("s1");
  });

  it("shows nothing for a cached spawn, and fetches nothing", async () => {
    cacheThread(SPAWN, [threadMessage(SPAWN, "s1")]);
    setup();

    await openThread("open spawn");
    await advance(1000);

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(listMessagesMock).not.toHaveBeenCalled();
    expect(shownIds()).toBe("s1");
  });

  it("shows nothing switching back to the main thread", async () => {
    cacheThread(SPAWN, [threadMessage(SPAWN, "s1")]);
    setup();
    await openThread("open spawn");

    await openThread("open main");
    await advance(1000);

    expect(overlay()).not.toBeInTheDocument();
    expect(shownIds()).toBe("m1");
  });

  it("shows nothing leaving a still-loading spawn for the main thread", async () => {
    listMessagesMock.mockReturnValueOnce(deferred<ReturnType<typeof page>>().promise);
    setup();
    await openThread("open spawn");
    await advance(CATCH_UP_SHOW_DELAY_MS - 50);

    await openThread("open main");
    await advance(1000);

    expect(overlay()).not.toBeInTheDocument();
  });

  it("covers a spawn → spawn switch to one not loaded yet", async () => {
    cacheThread(SPAWN, [threadMessage(SPAWN, "s1")]);
    listMessagesMock.mockReturnValueOnce(deferred<ReturnType<typeof page>>().promise);
    setup();
    await openThread("open spawn");
    await advance(1000);
    expect(overlay()).not.toBeInTheDocument();

    await openThread("open other spawn");
    await advance(CATCH_UP_SHOW_DELAY_MS);

    expect(overlay()).toBeInTheDocument();
    expect(listMessagesMock).toHaveBeenCalledWith(CHAT, {
      threadId: OTHER_SPAWN,
      recent: undefined,
    });
  });

  it("covers a spawn opened on mobile too (same ChatContainer path)", async () => {
    const response = deferred<ReturnType<typeof page>>();
    listMessagesMock.mockReturnValueOnce(response.promise);
    setup({ surface: "mobile" });

    await openThread("open spawn");
    await advance(CATCH_UP_SHOW_DELAY_MS);
    expect(overlay()).toBeInTheDocument();
    expect(frame()).toHaveAttribute("aria-busy", "true");

    await act(async () => {
      response.resolve(page([threadMessage(SPAWN, "s1")]));
    });
    await advance(CATCH_UP_MIN_VISIBLE_MS);
    expect(overlay()).not.toBeInTheDocument();
    expect(shownIds()).toBe("s1");
  });

  it("never appears while a loaded spawn streams", async () => {
    cacheThread(SPAWN, [threadMessage(SPAWN, "s1")]);
    const { rerender } = setup({ busy: true });
    await openThread("open spawn");

    // The chat stream fans the spawn's live messages out to its cache.
    for (let i = 2; i < 12; i++) {
      act(() => {
        fanOutMessagesToThreadCaches(CHAT, [threadMessage(SPAWN, `s${i}`)]);
      });
      rerender({ busy: true });
      await advance(100);
    }

    expect(overlay()).not.toBeInTheDocument();
    expect(frame()).not.toHaveAttribute("aria-busy");
    expect(scrollTimelineToBottom).not.toHaveBeenCalled();
    expect(shownIds()?.split(",")).toHaveLength(11);
  });
});
