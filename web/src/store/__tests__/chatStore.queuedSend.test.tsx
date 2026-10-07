/**
 * A message sent to a chat whose run is executing is on screen exactly once —
 * just sent, queued, and picked up — at every render.
 *
 * SendMessage to an executing run does not write the message into the
 * transcript: it queues it in the thread's mailbox (it would otherwise land
 * before the in-flight reply that never saw it) and answers queued=true with
 * the queued row's id. The composer still shows the send at once, as an
 * optimistic transcript entry; that entry and the pending-queue strip's row are
 * the same message, keyed by the id the client chose and sent as
 * client_message_id.
 *
 * Driven through the real send action, the real strip hook and the real
 * chat-update path (globalUpdatesStore.handleChatUpdate) that delivers the
 * drained message and announces the drain. Only the two RPCs are faked.
 */

import { act, render, waitFor } from "@testing-library/react";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../gen/reliant/v1/chat_pb";
import type { ChatUpdate } from "../../types/streaming";
import type { QueuedAgentMessageView } from "../../api/chat-grpc";

const CHAT = "chat-queued-send";
const THREAD = "thread-main";
const TEXT = "one more thing";
const SERVER_CHOSEN_ID = "0b6f3c1e-8a52-4f0e-9b7d-2c4a1e5f6d70";

const { sendMessageMock, listQueuedMock } = vi.hoisted(() => ({
  sendMessageMock: vi.fn(),
  listQueuedMock: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>(
    "../../api/client",
  );
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: { ...actual.api.chatsV2, sendMessage: sendMessageMock },
    },
  };
});

vi.mock("../../api/chat-grpc", async () => {
  const actual = await vi.importActual<typeof import("../../api/chat-grpc")>(
    "../../api/chat-grpc",
  );
  return {
    ...actual,
    chatGrpc: { ...actual.chatGrpc, listQueuedAgentMessages: listQueuedMock },
  };
});

import { useChatStore } from "../chatStore";
import { useGlobalUpdatesStore } from "../globalUpdatesStore";
import { useProjectStore } from "../projectStore";
import { useQueuedAgentMessages } from "../../hooks/queued-agent-messages";
import {
  clearAllMessagesCache,
  messageKeys,
  type MessageListResult,
} from "../../hooks/message-queries";
import { chatKeys } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";
import { initEventBus } from "../../lib/events";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

/** How many times the message is on screen in each render, in order. */
let onScreen: number[] = [];

function transcriptCopies(result: MessageListResult | undefined): number {
  return (result?.messages ?? []).filter(
    (m) =>
      m.role === MessageRole.USER &&
      m.contentBlocks.some((b) => b.content === TEXT),
  ).length;
}

/** The two places the composer's message can appear: transcript and strip. */
function Screen() {
  const { data: transcript } = useQuery<MessageListResult>({
    queryKey: messageKeys.list(CHAT),
    queryFn: () => Promise.reject(new Error("transcript is fed by the stream")),
    enabled: false,
  });
  const { messages: queued } = useQueuedAgentMessages(CHAT, THREAD, true);
  const copies =
    transcriptCopies(transcript) + queued.filter((m) => m.body === TEXT).length;
  onScreen.push(copies);
  return null;
}

function queuedRow(id: string): QueuedAgentMessageView {
  return { id, body: TEXT, created_at: "2026-01-01T00:00:00Z", sender_kind: 5, attachments: [] };
}

/**
 * Let React Query deliver what it has scheduled. It notifies observers on a
 * zero-delay timer, so a render reflects a cache write only after one.
 */
async function settle() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
  });
}

/** The drain, as the chat stream delivers it: the message, then the announcement. */
async function deliverDrain(rowId: string) {
  const updates = [
    {
      update_type: "message",
      message: {
        id: "msg-delivered",
        chatId: CHAT,
        role: MessageRole.USER,
        contentBlocks: [{ id: "b0", type: ContentBlockType.TEXT, index: 0, content: TEXT }],
        createdAt: "2026-01-01T00:00:02.000Z",
        updatedAt: "2026-01-01T00:00:02.000Z",
        streamingState: StreamingState.COMPLETE,
        seq: 5n,
        thread: THREAD,
        sequenceNumber: 0n,
        attachments: [],
      },
    },
    { update_type: "agent_messages_drained", thread: THREAD, message_ids: [rowId] },
  ] as unknown as ChatUpdate[];
  act(() => {
    useGlobalUpdatesStore.getState().handleChatUpdate(updates);
  });
  await settle();
}

/** The mailbox read the enqueue announcement triggers. */
async function readMailbox() {
  await act(async () => {
    await queryClient.invalidateQueries({ queryKey: ["queuedAgentMessages"] });
  });
  await settle();
}

async function startSend(): Promise<
  Deferred<unknown> & { sent: Promise<void>; clientMessageId: () => string }
> {
  const response = deferred<unknown>();
  sendMessageMock.mockReturnValueOnce(response.promise);
  let sent!: Promise<void>;
  act(() => {
    sent = useChatStore.getState().sendMessage(CHAT, TEXT, undefined, {});
  });
  await settle();
  return {
    ...response,
    sent,
    // The id the queued row is created with: the client's own, or — from a
    // client that sends none — one the server chose.
    clientMessageId: () =>
      (sendMessageMock.mock.calls[0][3]?.client_message_id as string | undefined) ??
      SERVER_CHOSEN_ID,
  };
}

function renderScreen() {
  return render(
    <QueryClientProvider client={queryClient}>
      <Screen />
    </QueryClientProvider>,
  );
}

/** Every render from the first one showing the message onward. */
function rendersSinceSent(): number[] {
  const first = onScreen.indexOf(1);
  expect(first, `the message never appeared: ${JSON.stringify(onScreen)}`).toBeGreaterThanOrEqual(0);
  return onScreen.slice(first);
}

beforeEach(() => {
  initEventBus();
  onScreen = [];
  sendMessageMock.mockReset();
  listQueuedMock.mockReset();
  listQueuedMock.mockResolvedValue({ messages: [] });
  clearAllMessagesCache();
  queryClient.removeQueries({ queryKey: ["queuedAgentMessages"] });
  useChatStore.setState({
    activeChatId: null,
    streamingMessages: {},
    toolResultsByCallId: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
  } as never);
  useProjectStore.setState({ currentProject: { id: "p1", name: "p", path: "/p" } } as never);
  queryClient.setQueryData(chatKeys.detail(CHAT), {
    id: CHAT,
    projectId: "p1",
    title: "t",
    workflowId: THREAD,
  });
  useGlobalUpdatesStore.setState({ subscribedChatId: CHAT } as never);
});

afterEach(() => {
  queryClient.removeQueries({ queryKey: ["queuedAgentMessages"] });
});

describe("a send to an executing run is on screen exactly once", () => {
  it("just sent, queued, picked up", async () => {
    renderScreen();
    await waitFor(() => expect(listQueuedMock).toHaveBeenCalled());

    // Just sent. The server has already queued the row and announced it, and
    // the strip reads the mailbox before SendMessage answers.
    const send = await startSend();
    const id = send.clientMessageId();
    expect(id).not.toBe(SERVER_CHOSEN_ID);
    listQueuedMock.mockResolvedValue({ messages: [queuedRow(id)] });
    await readMailbox();
    expect(onScreen.at(-1)).toBe(1);

    // Queued: SendMessage answers that it was, under the client's id.
    await act(async () => {
      send.resolve({ chatId: CHAT, workflowId: THREAD, runId: "r1", messageId: id, queued: true });
      await send.sent;
    });
    await settle();
    const transcript = queryClient.getQueryData<MessageListResult>(messageKeys.list(CHAT));
    expect(transcriptCopies(transcript)).toBe(0);
    expect(onScreen.at(-1)).toBe(1);

    // Picked up: the next turn drains it into the transcript.
    listQueuedMock.mockResolvedValue({ messages: [] });
    await deliverDrain(id);
    expect(onScreen.at(-1)).toBe(1);
    expect(
      transcriptCopies(queryClient.getQueryData<MessageListResult>(messageKeys.list(CHAT))),
    ).toBe(1);

    expect(rendersSinceSent()).toEqual(rendersSinceSent().map(() => 1));
  });

  it("is shown once when the drain beats SendMessage's answer", async () => {
    renderScreen();
    await waitFor(() => expect(listQueuedMock).toHaveBeenCalled());

    const send = await startSend();
    const id = send.clientMessageId();
    await deliverDrain(id);
    expect(onScreen.at(-1)).toBe(1);

    await act(async () => {
      send.resolve({ chatId: CHAT, workflowId: THREAD, runId: "r1", messageId: id, queued: true });
      await send.sent;
    });
    await settle();
    expect(onScreen.at(-1)).toBe(1);
    expect(rendersSinceSent()).toEqual(rendersSinceSent().map(() => 1));
  });

  it("stays in the transcript when the send was saved rather than queued", async () => {
    renderScreen();
    await waitFor(() => expect(listQueuedMock).toHaveBeenCalled());

    const send = await startSend();
    await act(async () => {
      send.resolve({ chatId: CHAT, workflowId: THREAD, runId: "r1", messageId: "m1", queued: false });
      await send.sent;
    });
    await settle();
    expect(
      transcriptCopies(queryClient.getQueryData<MessageListResult>(messageKeys.list(CHAT))),
    ).toBe(1);
    expect(rendersSinceSent()).toEqual(rendersSinceSent().map(() => 1));
  });
});
