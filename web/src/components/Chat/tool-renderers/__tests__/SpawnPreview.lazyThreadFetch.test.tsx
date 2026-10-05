import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { Message } from "../../../../api/client";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
  WorkflowState,
  WorkflowStopReason,
} from "../../../../gen/reliant/v1/chat_pb";
import { queryClient } from "../../../../lib/query-client";
import {
  clearAllMessagesCache,
  fanOutMessagesToThreadCaches,
} from "../../../../hooks/message-queries";
import { SpawnToolRenderer } from "../SpawnToolRenderer";
import type { ToolRenderContext } from "../types";

// The chat-open snapshot no longer carries spawn-thread transcripts (they were
// most of its bytes and nothing rendered from them). A spawn card therefore
// arrives with NO cached child messages, and opening it is what loads them.
//
// These tests run the real SpawnToolRenderer and the real useThreadMessages
// against the app's query client; only the network call is stubbed.

const { listMessagesMock } = vi.hoisted(() => ({ listMessagesMock: vi.fn() }));

vi.mock("../../../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../../../api/client")>(
    "../../../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: { ...actual.api.chatsV2, listMessages: listMessagesMock },
    },
  };
});

vi.mock("../../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({
    allWorkflows: [
      {
        id: CHILD,
        state: WorkflowState.ACTIVE,
        stopReason: WorkflowStopReason.UNSPECIFIED,
        children: [],
      },
    ],
  }),
}));

vi.mock("../../../../store/chatStoreHooks", () => ({
  useToolResultsByCallId: () => ({}),
}));

const CHAT = "chat-lazy-spawn";
const CHILD = "child-thread-1";

function childMessage(id: string, seq: number, text: string): Message {
  return {
    id,
    chatId: CHAT,
    role: MessageRole.ASSISTANT,
    contentBlocks: [{ id: `${id}-b`, index: 0, type: ContentBlockType.TEXT, content: text }],
    createdAt: "2026-10-04T12:00:00.000Z",
    updatedAt: "2026-10-04T12:00:00.000Z",
    streamingState: StreamingState.COMPLETE,
    seq: BigInt(seq),
    thread: CHILD,
    sequenceNumber: 0n,
    attachments: [],
  } as unknown as Message;
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

function ctx(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: "spawn",
    toolCallId: "toolu_spawn",
    childWorkflowId: CHILD,
    input: { title: "audit" },
    result: undefined,
    chatId: CHAT,
    isExpanded: true,
    isCompleted: true,
    isExecuting: false,
    isPreparing: false,
    hasFailed: false,
    ...overrides,
  };
}

function wrap(ui: ReactNode) {
  return <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>;
}

beforeEach(() => {
  clearAllMessagesCache();
  listMessagesMock.mockReset();
});

describe("SpawnPreview lazily loads its child thread", () => {
  it("fetches the child thread when a card with no cached messages is opened, shows loading, then renders it", async () => {
    const response = deferred<ReturnType<typeof page>>();
    listMessagesMock.mockReturnValueOnce(response.promise);

    render(wrap(<SpawnToolRenderer ctx={ctx()} />));

    expect(listMessagesMock).toHaveBeenCalledTimes(1);
    const [chatId, options] = listMessagesMock.mock.calls[0];
    expect(chatId).toBe(CHAT);
    expect(options.threadId).toBe(CHILD);
    // Bounded: the preview shows the last few messages, not a whole spawn
    // transcript (real ones run to ~2,000 messages).
    expect(options.recent).toBeGreaterThan(0);

    expect(screen.getByText("Loading agent activity…")).toBeInTheDocument();
    expect(screen.queryByText("Starting…")).not.toBeInTheDocument();

    await act(async () => {
      response.resolve(page([childMessage("m1", 10, "Reading the snapshot code")]));
    });

    expect(await screen.findByText("Reading the snapshot code")).toBeInTheDocument();
    expect(screen.queryByText("Loading agent activity…")).not.toBeInTheDocument();
  });

  it("keeps a streamed child message that lands while the fetch is in flight", async () => {
    const response = deferred<ReturnType<typeof page>>();
    listMessagesMock.mockReturnValueOnce(response.promise);

    render(wrap(<SpawnToolRenderer ctx={ctx()} />));
    expect(listMessagesMock).toHaveBeenCalledTimes(1);

    // A live spawn keeps writing. Its newest message reaches the stream
    // before the page — read from a snapshot taken a moment earlier — comes
    // back without it.
    act(() => {
      fanOutMessagesToThreadCaches(CHAT, [childMessage("m2", 12, "Streamed while loading")]);
    });

    await act(async () => {
      response.resolve(page([childMessage("m1", 10, "Fetched page message")]));
    });

    expect(await screen.findByText("Fetched page message")).toBeInTheDocument();
    expect(screen.getByText("Streamed while loading")).toBeInTheDocument();
  });

  it("does not fetch again once the thread is loaded", async () => {
    listMessagesMock.mockResolvedValueOnce(page([childMessage("m1", 10, "Loaded once")]));

    const { rerender } = render(wrap(<SpawnToolRenderer ctx={ctx()} />));
    expect(await screen.findByText("Loaded once")).toBeInTheDocument();

    // Collapsing and re-opening remounts the renderer.
    rerender(wrap(<div />));
    rerender(wrap(<SpawnToolRenderer ctx={ctx()} />));

    expect(screen.getByText("Loaded once")).toBeInTheDocument();
    expect(listMessagesMock).toHaveBeenCalledTimes(1);

    // And a later streamed message still applies to the loaded thread.
    act(() => {
      fanOutMessagesToThreadCaches(CHAT, [childMessage("m3", 14, "Live after load")]);
    });
    await waitFor(() => expect(screen.getByText("Live after load")).toBeInTheDocument());
  });
});
