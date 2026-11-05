// Copyright (c) 2025 Reliant Labs
//
// Migration guard for the chat stream: snapshot -> caught_up -> live -> an
// approval -> reconnect with the cursor, end to end through a scripted Connect
// server (the transport seam, `setTransport`) and the real stores.
//
// It deliberately imports none of the stream modules (api/stream-connection,
// api/streaming-grpc, api/chat-streaming-grpc) or the decoders. Moving them
// into @reliantlabs/sdk must not change a single assertion here: the test names
// what the stores and caches hold and what each connection asked the server
// for, never how the client got there.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { setTransport } from "@/lib/connect";
import { ChatUpdateType, ChatStreamEventSchema } from "../../gen/reliant/v1/streaming_pb";
import { MessageRole, StreamingState } from "../../gen/reliant/v1/chat_pb";
import { ApprovalStatus } from "../../gen/reliant/v1/approval_pb";
import { useChatStore } from "../../store/chatStore";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { useThreadActivityStore } from "../../store/threadActivityStore";
import { approvalsFromCache } from "../../hooks/approval-queries";
import { clearAllMessagesCache, getMessagesFromCache } from "../../hooks/message-queries";
import { queryClient } from "../../lib/query-client";
import { resetPageActivityForTests } from "../../lib/pageActivity";

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

const CHAT = "c0ffee00-1111-2222-3333-444444444444";
const THREAD = "";

// ── A scripted Connect server ──────────────────────────────────────────────

interface Connection {
  method: string;
  request: Record<string, unknown>;
  signal: AbortSignal | undefined;
  push(event: unknown): void;
  fail(error: Error): void;
  end(): void;
}

function scriptedServer() {
  const connections: Connection[] = [];
  const transport: Transport = {
    async unary(method) {
      throw new ConnectError(`scripted server: no unary ${method.name}`, Code.Unimplemented);
    },
    async stream(method, signal, _timeoutMs, _header, input) {
      let request: Record<string, unknown> = {};
      for await (const first of input) {
        request = create(method.input, first as never) as unknown as Record<string, unknown>;
        break;
      }

      const queue: Array<{ event: unknown } | { error: Error } | { done: true }> = [];
      let wake: (() => void) | null = null;
      const enqueue = (item: (typeof queue)[number]) => {
        queue.push(item);
        wake?.();
      };
      signal?.addEventListener("abort", () =>
        enqueue({ error: new ConnectError("aborted", Code.Canceled) }),
      );
      connections.push({
        method: method.name,
        request,
        signal,
        push: (event) => enqueue({ event }),
        fail: (error) => enqueue({ error }),
        end: () => enqueue({ done: true }),
      });

      return {
        stream: true,
        service: method.parent,
        method,
        header: new Headers(),
        trailer: new Headers(),
        message: {
          async *[Symbol.asyncIterator]() {
            for (;;) {
              while (queue.length === 0) await new Promise<void>((r) => (wake = r));
              const item = queue.shift()!;
              if ("event" in item) yield item.event;
              else if ("error" in item) throw item.error;
              else return;
            }
          },
        },
      } as never;
    },
  };
  return {
    transport,
    connections,
    chatStreams: () => connections.filter((c) => c.method === "StreamChatUpdates"),
  };
}

// ── Wire frames, as the server sends them ──────────────────────────────────

const frame = (event: Record<string, unknown>) => create(ChatStreamEventSchema, { event } as never);

const textMessage = (id: string, role: MessageRole, seq: number, text: string) => ({
  id,
  chatId: CHAT,
  role,
  seq: BigInt(seq),
  thread: THREAD,
  streamingState: StreamingState.COMPLETE,
  createdAt: "2026-01-01T00:00:00.000Z",
  updatedAt: "2026-01-01T00:00:00.000Z",
  contentBlocks: [{ id: `${id}-b0`, type: 1, index: 0, content: text }],
});

const approvalData = (seq: number, status: "pending" | "approved") => ({
  updateType: ChatUpdateType.APPROVAL,
  sequenceNumber: BigInt(seq),
  entityId: "appr-1",
  chatId: CHAT,
  dataJson: JSON.stringify({
    id: "appr-1",
    chat_id: CHAT,
    approval_type: "tool_call",
    entity_id: "tc-1",
    title: "Run the migration",
    status,
    sequence_number: seq,
    created_at: "2026-01-01T00:00:01.000Z",
  }),
});

const messageData = (seq: number, id: string, text: string) => ({
  updateType: ChatUpdateType.MESSAGE,
  sequenceNumber: BigInt(seq),
  entityId: id,
  chatId: CHAT,
  dataJson: JSON.stringify({
    message: {
      id,
      chat_id: CHAT,
      role: MessageRole.ASSISTANT,
      seq,
      thread: THREAD,
      streaming_state: StreamingState.COMPLETE,
      content_blocks: [{ id: `${id}-b0`, type: 1, index: 0, content: text }],
    },
  }),
});

const delta = (deltaType: string, extra: Record<string, unknown> = {}) => ({
  updateType: ChatUpdateType.STREAMING_DELTA,
  sequenceNumber: 0n,
  entityId: "m-live",
  chatId: CHAT,
  dataJson: JSON.stringify({
    delta_type: deltaType,
    block_index: 0,
    message_id: "m-live",
    thread: THREAD,
    ...extra,
  }),
});

const snapshotFrame = (latest: number) =>
  frame({
    case: "snapshot",
    value: {
      messages: [
        textMessage("m-1", MessageRole.USER, 1, "ship it"),
        textMessage("m-2", MessageRole.ASSISTANT, 2, "on it"),
      ],
      otherUpdates: [approvalData(latest, "pending")],
      latestSequence: BigInt(latest),
      total: 2,
      hasMore: false,
      oldestSeq: 1n,
      threadTokenCount: 120n,
      compactionThreshold: 20000n,
    },
  });

const updatesFrame = (updates: unknown[], latest: number) =>
  frame({ case: "updates", value: { updates, latestSequence: BigInt(latest) } });

const caughtUpFrame = (latest: number) =>
  frame({ case: "caughtUp", value: { latestSequence: BigInt(latest) } });

const heartbeatFrame = () => frame({ case: "heartbeat", value: { timestamp: 0n } });

// ── Harness ────────────────────────────────────────────────────────────────

const flush = () => vi.advanceTimersByTimeAsync(0);

const texts = () =>
  getMessagesFromCache(CHAT).map((m) => m.contentBlocks.map((b) => b.content).join(""));

let server: ReturnType<typeof scriptedServer>;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
  resetPageActivityForTests();
  useChatStore.getState().reset();
  clearAllMessagesCache();
  queryClient.clear();
  useThreadActivityStore.setState({ threads: {} } as never);
  useGlobalUpdatesStore.setState({
    wsService: null,
    chatStream: null,
    subscribedChatId: null,
    connectionStatus: "disconnected",
    lastSequence: 0,
  });
  server = scriptedServer();
  setTransport(server.transport);
});

afterEach(() => {
  useGlobalUpdatesStore.getState().disconnect();
  setTransport(null);
  vi.useRealTimers();
});

describe("the chat stream, end to end over a scripted transport", () => {
  it("snapshot, catch-up, live deltas and messages, and an approval", async () => {
    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT);
    await flush();

    // A first open asks for a snapshot (cursor 0), for this chat.
    expect(server.chatStreams()).toHaveLength(1);
    expect(server.chatStreams()[0].request).toMatchObject({ chatId: CHAT, sinceSeq: 0n });
    expect(useChatStore.getState().chatSyncPendingId).toBe(CHAT);

    const conn = server.chatStreams()[0];
    conn.push(snapshotFrame(10));
    await flush();

    // The snapshot replaced the transcript and carried the pending approval,
    // and the syncing indicator stays up until the server says caught_up.
    expect(texts()).toEqual(["ship it", "on it"]);
    expect(approvalsFromCache(CHAT).map((a) => [a.id, a.status])).toEqual([
      ["appr-1", ApprovalStatus.PENDING],
    ]);
    expect(useChatStore.getState().chatSyncPendingId).toBe(CHAT);

    conn.push(caughtUpFrame(10));
    await flush();
    expect(useChatStore.getState().chatSyncPendingId).toBeNull();
    expect(useGlobalUpdatesStore.getState().chatStream?.getLastChatSequence()).toBe(10);

    // Live: ephemeral deltas build a streaming placeholder under the
    // pre-allocated message id without moving the cursor ...
    conn.push(
      updatesFrame(
        [
          delta("message_start"),
          delta("content_block_start"),
          delta("content_block_delta", { delta: "wor" }),
          delta("content_block_delta", { delta: "king" }),
        ],
        10,
      ),
    );
    // Deltas are coalesced into one commit per frame (or the fallback timer).
    await vi.advanceTimersByTimeAsync(500);
    const streaming = useChatStore.getState().streamingMessages[CHAT]?.[CHAT];
    expect(streaming?.id).toBe("m-live");
    expect(streaming?.contentBlocks.map((b) => b.content).join("")).toBe("working");
    expect(useGlobalUpdatesStore.getState().chatStream?.getLastChatSequence()).toBe(10);

    // ... and the persisted message advances the cursor and joins the transcript.
    conn.push(updatesFrame([messageData(11, "m-3", "done")], 11));
    await flush();
    expect(texts()).toEqual(["ship it", "on it", "done"]);
    expect(useGlobalUpdatesStore.getState().chatStream?.getLastChatSequence()).toBe(11);

    // The approval resolves live: same approval, new status, title kept.
    conn.push(updatesFrame([approvalData(12, "approved")], 12));
    await flush();
    const [approval] = approvalsFromCache(CHAT);
    expect(approval).toMatchObject({ id: "appr-1", status: ApprovalStatus.APPROVED, title: "Run the migration" });
    expect(useGlobalUpdatesStore.getState().chatStream?.getLastChatSequence()).toBe(12);

    // Heartbeats change nothing.
    conn.push(heartbeatFrame());
    await flush();
    expect(texts()).toHaveLength(3);
    expect(server.chatStreams()).toHaveLength(1);
  });

  it("reconnects after a dropped stream from the last processed sequence and takes the replay", async () => {
    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT);
    await flush();
    const first = server.chatStreams()[0];
    first.push(snapshotFrame(10));
    first.push(caughtUpFrame(10));
    first.push(updatesFrame([messageData(11, "m-3", "done")], 11));
    await flush();
    expect(texts()).toEqual(["ship it", "on it", "done"]);

    // The connection dies. The client backs off, then reconnects.
    first.fail(new ConnectError("connection reset", Code.Unavailable));
    await flush();
    expect(server.chatStreams()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(2_500);
    expect(server.chatStreams()).toHaveLength(2);

    // It resumes from the cursor: no snapshot is requested.
    const second = server.chatStreams()[1];
    expect(second.request).toMatchObject({ chatId: CHAT, sinceSeq: 11n });

    // The server replays what was missed, then exactly one caught_up. The
    // approval resolved while the client was away; the transcript is kept.
    second.push(updatesFrame([approvalData(12, "approved"), messageData(13, "m-4", "after")], 13));
    second.push(caughtUpFrame(13));
    await flush();
    expect(texts()).toEqual(["ship it", "on it", "done", "after"]);
    expect(approvalsFromCache(CHAT)[0].status).toBe(ApprovalStatus.APPROVED);
    expect(useChatStore.getState().chatSyncPendingId).toBeNull();
    expect(useGlobalUpdatesStore.getState().chatStream?.getLastChatSequence()).toBe(13);
    expect(useGlobalUpdatesStore.getState().chatStream?.isConnected()).toBe(true);
  });

  it("replaces a connection that claims to be live when the network comes back, keeping the cursor", async () => {
    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT);
    await flush();
    const first = server.chatStreams()[0];
    first.push(snapshotFrame(10));
    first.push(caughtUpFrame(10));
    await flush();

    window.dispatchEvent(new Event("online"));
    await flush();

    // Immediate, no backoff: the old socket is aborted and a new one resumes.
    expect(first.signal?.aborted).toBe(true);
    expect(server.chatStreams()).toHaveLength(2);
    expect(server.chatStreams()[1].request).toMatchObject({ chatId: CHAT, sinceSeq: 10n });

    server.chatStreams()[1].push(updatesFrame([messageData(11, "m-3", "back")], 11));
    server.chatStreams()[1].push(caughtUpFrame(11));
    await flush();
    expect(texts()).toEqual(["ship it", "on it", "back"]);
  });

  it("applies a snapshot the server sends in place of a replay", async () => {
    useGlobalUpdatesStore.getState().subscribeToChatDetails(CHAT);
    await flush();
    const first = server.chatStreams()[0];
    first.push(snapshotFrame(10));
    first.push(caughtUpFrame(10));
    await flush();

    first.fail(new ConnectError("connection reset", Code.Unavailable));
    await vi.advanceTimersByTimeAsync(2_500);
    const second = server.chatStreams()[1];
    expect(second.request).toMatchObject({ sinceSeq: 10n });

    // The cursor is unreplayable: the server answers with a snapshot instead.
    second.push(
      frame({
        case: "snapshot",
        value: {
          messages: [textMessage("m-9", MessageRole.ASSISTANT, 40, "fresh start")],
          otherUpdates: [],
          latestSequence: 40n,
          total: 1,
          hasMore: false,
          oldestSeq: 40n,
          threadTokenCount: 0n,
          compactionThreshold: 20000n,
        },
      }),
    );
    second.push(caughtUpFrame(40));
    await flush();

    expect(texts()).toEqual(["fresh start"]);
    expect(useGlobalUpdatesStore.getState().chatStream?.getLastChatSequence()).toBe(40);
  });
});
