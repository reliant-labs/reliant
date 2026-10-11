/**
 * A sent message is one thing on screen, in the right place, whatever happens
 * to the send.
 *
 *   - A send that fails for good is marked "Not sent", with Retry and Remove.
 *     It used to stay dimmed ("Waiting for message to save") until the next
 *     message or a reopen silently took it away. The composer clears on send
 *     and a refused send leaves nothing on the server (#688), so that bubble
 *     was the only copy of the text.
 *   - Retried across a machine coming up, it is ONE bubble, not one per
 *     attempt.
 *   - Sent while the run is executing (waiting on its machine, say), it goes
 *     straight to the pending-queue strip, where it will end up. It used to go
 *     into the transcript and jump to the strip when the response said it was
 *     queued.
 *
 * Driven through the real store actions, the real strip hook and the real
 * message cache. Only the RPCs are faked.
 */

import { act, render, waitFor } from "@testing-library/react";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
  WorkflowState,
} from "../../gen/reliant/v1/chat_pb";
import type { QueuedAgentMessageView } from "../../api/chat-grpc";
import type { ChatUpdate } from "../../types/streaming";

const CHAT = "chat-send-lifecycle";
const THREAD = "thread-main";
const TEXT = "rename the module";

const { sendMessageMock, startMock, listQueuedMock } = vi.hoisted(() => ({
  sendMessageMock: vi.fn(),
  startMock: vi.fn(),
  listQueuedMock: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return {
    ...actual,
    api: {
      ...actual.api,
      chatsV2: { ...actual.api.chatsV2, sendMessage: sendMessageMock, start: startMock },
    },
  };
});

vi.mock("../../api/chat-grpc", async () => {
  const actual = await vi.importActual<typeof import("../../api/chat-grpc")>("../../api/chat-grpc");
  return { ...actual, chatGrpc: { ...actual.chatGrpc, listQueuedAgentMessages: listQueuedMock } };
});

// Retry without waiting out the real poll.
vi.mock("../../lib/daemon-wait", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/daemon-wait")>()),
  DAEMON_WAIT_POLL_MS: 1,
}));

import { canRetryFailedSend, useChatStore } from "../chatStore";
import { useActivityStore, ChatActivity } from "../activityStore";
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
import { sendWithDaemonWait } from "../../lib/daemon-retry";

type Placement = { transcript: string[]; strip: string[] };
let renders: Placement[] = [];

/** The two places the message can appear: transcript and strip. */
function Screen() {
  const { data } = useQuery<MessageListResult>({
    queryKey: messageKeys.list(CHAT),
    queryFn: () => Promise.reject(new Error("fed by the stream")),
    enabled: false,
  });
  const { messages: queued } = useQueuedAgentMessages(CHAT, THREAD, true);
  renders.push({
    transcript: (data?.messages ?? [])
      .filter((m) => m.role === MessageRole.USER)
      .map((m) => m.id),
    strip: queued.filter((m) => m.body === TEXT).map((m) => m.id),
  });
  return null;
}

/** React Query notifies observers on a zero-delay timer. */
async function settle() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
  });
}

function latest(): Placement {
  return renders.at(-1)!;
}

function transcriptIds(): string[] {
  return (
    queryClient.getQueryData<MessageListResult>(messageKeys.list(CHAT))?.messages ?? []
  ).map((m) => m.id);
}

function failedEntry(): string | undefined {
  return transcriptIds().find((id) => id.startsWith("optimistic-failed-"));
}

function clientIdOf(failedId: string): string {
  return failedId.slice("optimistic-failed-".length);
}

function sentIds(): string[] {
  return sendMessageMock.mock.calls.map((call) => call[3]?.client_message_id as string);
}

const daemonConnecting = () =>
  new ConnectError("unavailable: no daemon connected for user", Code.Internal);

const refused = () =>
  new ConnectError("model \"gpt-0\" is not a known model", Code.InvalidArgument);

const saved = (messageId = "m1") => ({
  chatId: CHAT,
  workflowId: THREAD,
  runId: "r",
  messageId,
  queued: false,
});

async function failOnce() {
  sendMessageMock.mockRejectedValueOnce(refused());
  await act(async () => {
    await expect(useChatStore.getState().sendMessage(CHAT, TEXT)).rejects.toThrow();
  });
  await settle();
}

function realUserMessage(id: string, text: string) {
  return {
    update_type: "message",
    message: {
      id,
      chatId: CHAT,
      role: MessageRole.USER,
      contentBlocks: [{ id: "b0", type: ContentBlockType.TEXT, index: 0, content: text }],
      createdAt: "2026-01-01T00:00:02.000Z",
      updatedAt: "2026-01-01T00:00:02.000Z",
      streamingState: StreamingState.COMPLETE,
      seq: 5n,
      thread: THREAD,
      sequenceNumber: 0n,
      attachments: [],
    },
  } as unknown as ChatUpdate;
}

function homeChat(workflowState = WorkflowState.COMPLETED) {
  queryClient.setQueryData(chatKeys.detail(CHAT), {
    id: CHAT,
    projectId: "p1",
    title: "t",
    workflowId: THREAD,
    workflowState,
  });
}

beforeEach(() => {
  initEventBus();
  renders = [];
  sendMessageMock.mockReset();
  startMock.mockReset();
  listQueuedMock.mockReset();
  listQueuedMock.mockResolvedValue({ messages: [] });
  clearAllMessagesCache();
  queryClient.setQueryData<MessageListResult>(messageKeys.list(CHAT), {
    messages: [],
    total: 0,
    hasMore: false,
    oldestSeq: 0,
  });
  queryClient.removeQueries({ queryKey: ["queuedAgentMessages"] });
  useActivityStore.getState().setActivity(CHAT, ChatActivity.IDLE);
  useProjectStore.setState({ currentProject: { id: "p1", name: "p", path: "/p" } } as never);
  homeChat();
  render(
    <QueryClientProvider client={queryClient}>
      <Screen />
    </QueryClientProvider>,
  );
});

afterEach(() => {
  queryClient.removeQueries({ queryKey: ["queuedAgentMessages"] });
});

describe("a send that fails for good", () => {
  it("is marked Not sent instead of left pending", async () => {
    await failOnce();

    const failed = failedEntry();
    expect(failed, JSON.stringify(transcriptIds())).toBeDefined();
    expect(transcriptIds()).toEqual([failed]);
    expect(canRetryFailedSend(clientIdOf(failed!))).toBe(true);
  });

  it("survives the next message and a snapshot, neither of which can know about it", async () => {
    await failOnce();
    const failed = failedEntry()!;

    act(() => {
      useChatStore.getState().processChatStreamUpdates(CHAT, [realUserMessage("m-next", "next")], false);
    });
    expect(transcriptIds()).toContain(failed);

    act(() => {
      useChatStore.getState().processChatStreamUpdates(CHAT, [], true);
    });
    expect(transcriptIds()).toContain(failed);
  });

  it("is sent again by Retry, and is one message on screen throughout", async () => {
    await failOnce();
    const failedId = clientIdOf(failedEntry()!);

    sendMessageMock.mockResolvedValueOnce(saved());
    await act(async () => {
      await useChatStore.getState().retryFailedSend(CHAT, failedId);
    });
    await settle();

    expect(sendMessageMock).toHaveBeenCalledTimes(2);
    expect(sendMessageMock.mock.calls[1][1]).toBe(TEXT);
    // A new send, under a new id; the failed entry is gone.
    const retryId = sentIds()[1];
    expect(retryId).not.toBe(failedId);
    expect(transcriptIds()).toEqual([`optimistic-user-${retryId}`]);
    expect(canRetryFailedSend(failedId)).toBe(false);
    expect(Math.max(...renders.map((r) => r.transcript.length + r.strip.length))).toBe(1);
  });

  it("is Not sent again, once, when the retry fails too", async () => {
    await failOnce();
    const firstId = clientIdOf(failedEntry()!);

    sendMessageMock.mockRejectedValueOnce(refused());
    await act(async () => {
      await expect(useChatStore.getState().retryFailedSend(CHAT, firstId)).rejects.toThrow();
    });
    await settle();

    expect(transcriptIds()).toEqual([`optimistic-failed-${sentIds()[1]}`]);
  });

  it("can be removed", async () => {
    await failOnce();
    const id = clientIdOf(failedEntry()!);

    act(() => useChatStore.getState().discardFailedSend(CHAT, id));

    expect(transcriptIds()).toEqual([]);
    expect(canRetryFailedSend(id)).toBe(false);
  });

  it("covers a branch's first send too, and Retry starts the chat", async () => {
    homeChat(WorkflowState.PENDING);
    startMock.mockRejectedValueOnce(new ConnectError("upstream reset", Code.Unavailable));
    await act(async () => {
      await expect(
        useChatStore.getState().startExistingChat(CHAT, TEXT, undefined, { workflow: null }),
      ).rejects.toThrow();
    });
    const failed = failedEntry();
    expect(failed, JSON.stringify(transcriptIds())).toBeDefined();
    expect(transcriptIds()).toEqual([failed]);

    startMock.mockResolvedValueOnce({ id: CHAT, projectId: "p1", workflowId: THREAD });
    await act(async () => {
      await useChatStore.getState().retryFailedSend(CHAT, clientIdOf(failed!));
    });

    expect(startMock).toHaveBeenCalledTimes(2);
    expect(sendMessageMock).not.toHaveBeenCalled();
    expect(failedEntry()).toBeUndefined();
  });
});

describe("a send retried across a machine coming up", () => {
  it("is one bubble on screen through every attempt", async () => {
    sendMessageMock
      .mockRejectedValueOnce(daemonConnecting())
      .mockRejectedValueOnce(daemonConnecting())
      .mockResolvedValueOnce(saved());
    const clientMessageId = "11111111-2222-4333-8444-555555555555";

    await act(async () => {
      await sendWithDaemonWait({
        action: () =>
          useChatStore.getState().sendMessage(CHAT, TEXT, undefined, { clientMessageId }),
      });
    });
    await settle();

    expect(sendMessageMock).toHaveBeenCalledTimes(3);
    expect(new Set(sentIds())).toEqual(new Set([clientMessageId]));
    // Never "Not sent" between attempts, and never more than one copy.
    expect(renders.some((r) => r.transcript.some((id) => id.startsWith("optimistic-failed-")))).toBe(false);
    expect(Math.max(...renders.map((r) => r.transcript.length + r.strip.length))).toBe(1);
    expect(latest().transcript).toEqual([`optimistic-user-${clientMessageId}`]);
  });
});

describe("a send to a run waiting on its machine", () => {
  beforeEach(() => {
    useActivityStore.getState().setActivity(CHAT, ChatActivity.WAITING_FOR_DAEMON);
  });

  it("goes straight to the queue strip and never visits the transcript", async () => {
    let answer!: (value: unknown) => void;
    sendMessageMock.mockReturnValueOnce(new Promise((r) => (answer = r)));

    let sent!: Promise<void>;
    act(() => {
      sent = useChatStore.getState().sendMessage(CHAT, TEXT);
    });
    await settle();

    const clientMessageId = sentIds()[0];
    expect(latest()).toEqual({ transcript: [], strip: [clientMessageId] });

    // A mailbox read that lands before the server's row exists keeps it.
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: ["queuedAgentMessages"] });
    });
    await settle();
    expect(latest().strip).toEqual([clientMessageId]);

    // The server queues it under the client's id: the same message.
    const row: QueuedAgentMessageView = {
      id: clientMessageId,
      body: TEXT,
      created_at: "2026-01-01T00:00:00Z",
      sender_kind: 5,
      attachments: [],
    };
    listQueuedMock.mockResolvedValue({ messages: [row] });
    await act(async () => {
      answer({ ...saved(clientMessageId), queued: true });
      await sent;
      await queryClient.invalidateQueries({ queryKey: ["queuedAgentMessages"] });
    });
    await settle();

    expect(latest()).toEqual({ transcript: [], strip: [clientMessageId] });
    expect(renders.every((r) => r.transcript.length === 0)).toBe(true);
  });

  it("moves to the transcript if the run had stopped and the server saved it", async () => {
    sendMessageMock.mockResolvedValueOnce(saved("m-saved"));

    await act(async () => {
      await useChatStore.getState().sendMessage(CHAT, TEXT);
    });
    await settle();

    expect(latest()).toEqual({ transcript: [`optimistic-user-${sentIds()[0]}`], strip: [] });
  });

  it("leaves the strip and is Not sent in the transcript when it fails", async () => {
    sendMessageMock.mockRejectedValueOnce(refused());

    await act(async () => {
      await expect(useChatStore.getState().sendMessage(CHAT, TEXT)).rejects.toThrow();
    });
    await settle();

    expect(latest()).toEqual({ transcript: [`optimistic-failed-${sentIds()[0]}`], strip: [] });
  });
});

describe("a send to an idle run", () => {
  it("is shown in the transcript, as before", async () => {
    sendMessageMock.mockResolvedValueOnce(saved());
    await act(async () => {
      await useChatStore.getState().sendMessage(CHAT, TEXT);
    });
    await settle();
    await waitFor(() => expect(latest().transcript).toHaveLength(1));
    expect(latest().strip).toEqual([]);
  });
});
