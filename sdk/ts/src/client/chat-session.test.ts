import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { createChatState } from "./chat-reducer.js";
import type { ChatState } from "./chat-state.js";
import { createReliantClient } from "./client.js";
import type { WakeHandlers, WakeSource } from "./reconnecting-stream.js";
import {
  approvalData,
  CHAT,
  caughtUpFrame,
  deltaData,
  heartbeatFrame,
  messageData,
  scriptedServer,
  snapshotFrame,
  textMessage,
  updatesFrame,
} from "../test/scripted-server.js";

// The whole client against a scripted Connect server: snapshot, catch-up, live
// stream, an approval and a reconnect with the cursor — the same journey the
// web app's guard test drives through its stores, here through the SDK alone.

const flush = () => vi.advanceTimersByTimeAsync(0);
const texts = (s: ChatState) => s.messages.map((m) => m.contentBlocks.map((b) => b.content).join(""));
const noWake: WakeSource = { subscribe: () => () => {}, isVisible: () => true };

let server: ReturnType<typeof scriptedServer>;

function open(handlers = {}, extra: { wake?: WakeSource } = {}) {
  server = scriptedServer(handlers);
  const client = createReliantClient({ transport: server.transport, wake: extra.wake ?? noWake });
  return client.chat({ chatId: CHAT, sleep: async () => {} });
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
});
afterEach(() => vi.useRealTimers());

describe("ChatSession over a scripted transport", () => {
  it("snapshot, catch-up, live deltas and messages, and an approval", async () => {
    const session = open().start();
    await flush();

    // A first open asks for a snapshot (cursor 0), for this chat.
    expect(server.chatStreams()).toHaveLength(1);
    expect(server.chatStreams()[0]!.request).toMatchObject({ chatId: CHAT, sinceSeq: 0n });
    expect(session.getState().syncPending).toBe(true);
    expect(session.getConnectionStatus()).toBe("connecting");

    const conn = server.chatStreams()[0]!;
    conn.push(
      snapshotFrame(
        10,
        [textMessage("m-1", 1, 1, "ship it"), textMessage("m-2", 2, 2, "on it")],
        [approvalData(10, "pending")],
      ),
    );
    await flush();

    // The snapshot replaced the transcript and carried the pending approval; the
    // syncing flag stays up until the server says caught_up.
    expect(session.getConnectionStatus()).toBe("connected");
    expect(texts(session.getState())).toEqual(["ship it", "on it"]);
    expect(session.getState().approvals.map((a) => [a.id, a.status])).toEqual([["appr-1", "pending"]]);
    expect(session.getState().pagination).toMatchObject({ total: 2, hasMore: false });
    expect(session.getState().contextUsage).toEqual({ threadTokenCount: 120, compactionThreshold: 20000 });
    expect(session.getState().syncPending).toBe(true);

    conn.push(caughtUpFrame(10));
    await flush();
    expect(session.getState().syncPending).toBe(false);
    expect(session.getState().cursor).toBe(10n);

    // Live: ephemeral deltas build a placeholder under the pre-allocated id and
    // do not move the cursor ...
    conn.push(
      updatesFrame(
        [
          deltaData("message_start"),
          deltaData("content_block_start"),
          deltaData("content_block_delta", { delta: "wor" }),
          deltaData("content_block_delta", { delta: "king" }),
        ],
        10,
      ),
    );
    await flush();
    const placeholder = session.getState().streaming[CHAT];
    expect(placeholder?.id).toBe("m-live");
    expect(placeholder?.contentBlocks.map((b) => b.content).join("")).toBe("working");
    expect(session.getState().cursor).toBe(10n);

    // ... the persisted message advances the cursor and joins the transcript.
    conn.push(updatesFrame([messageData(11, "m-3", "done")], 11));
    await flush();
    expect(texts(session.getState())).toEqual(["ship it", "on it", "done"]);
    expect(session.getState().cursor).toBe(11n);

    // The approval resolves live: same approval, new status, title kept.
    conn.push(updatesFrame([approvalData(12, "approved")], 12));
    await flush();
    expect(session.getState().approvals[0]).toMatchObject({ id: "appr-1", status: "approved", title: "Run the migration" });
    expect(session.getState().cursor).toBe(12n);

    conn.push(heartbeatFrame());
    await flush();
    expect(session.getState().messages).toHaveLength(3);
    expect(server.chatStreams()).toHaveLength(1);
    session.close();
  });

  it("reconnects after a dropped stream from the last processed sequence and takes the replay", async () => {
    const session = open().start();
    await flush();
    const first = server.chatStreams()[0]!;
    first.push(snapshotFrame(10, [textMessage("m-1", 1, 1, "ship it")], [approvalData(10, "pending")]));
    first.push(caughtUpFrame(10));
    first.push(updatesFrame([messageData(11, "m-2", "done")], 11));
    await flush();
    expect(texts(session.getState())).toEqual(["ship it", "done"]);

    first.fail(new ConnectError("connection reset", Code.Unavailable));
    await flush();
    expect(session.getConnectionStatus()).toBe("error");
    expect(server.chatStreams()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(2_500);
    expect(server.chatStreams()).toHaveLength(2);

    // It resumes from the cursor: no snapshot is requested.
    const second = server.chatStreams()[1]!;
    expect(second.request).toMatchObject({ chatId: CHAT, sinceSeq: 11n });

    // The server replays what was missed, then exactly one caught_up. The
    // approval resolved while the client was away; the transcript is kept.
    second.push(updatesFrame([approvalData(12, "approved"), messageData(13, "m-3", "after")], 13));
    second.push(caughtUpFrame(13));
    await flush();
    expect(texts(session.getState())).toEqual(["ship it", "done", "after"]);
    expect(session.getState().approvals[0]!.status).toBe("approved");
    expect(session.getState().cursor).toBe(13n);
    expect(session.getConnectionStatus()).toBe("connected");
    expect(session.getError()).toBeNull();
    session.close();
  });

  it("replaces a connection that claims to be live when the network comes back, keeping the cursor", async () => {
    let handlers: WakeHandlers | null = null;
    const wake: WakeSource = {
      subscribe: (h) => {
        handlers = h;
        return () => {};
      },
      isVisible: () => true,
    };
    const session = open({}, { wake }).start();
    await flush();
    const first = server.chatStreams()[0]!;
    first.push(snapshotFrame(10, [textMessage("m-1", 1, 1, "ship it")]));
    first.push(caughtUpFrame(10));
    await flush();

    (handlers as WakeHandlers | null)!.online();
    await flush();

    // Immediate, no backoff: the old socket is aborted and a new one resumes.
    expect(first.signal?.aborted).toBe(true);
    expect(server.chatStreams()).toHaveLength(2);
    expect(server.chatStreams()[1]!.request).toMatchObject({ sinceSeq: 10n });
    session.close();
  });

  it("applies a snapshot the server sends in place of a replay", async () => {
    const session = open().start();
    await flush();
    const first = server.chatStreams()[0]!;
    first.push(snapshotFrame(10, [textMessage("m-1", 1, 1, "ship it")]));
    first.push(caughtUpFrame(10));
    await flush();

    first.fail(new ConnectError("connection reset", Code.Unavailable));
    await vi.advanceTimersByTimeAsync(2_500);
    const second = server.chatStreams()[1]!;
    expect(second.request).toMatchObject({ sinceSeq: 10n });

    // The cursor is unreplayable: the server answers with a snapshot instead.
    second.push(snapshotFrame(40, [textMessage("m-9", 2, 40, "fresh start")]));
    second.push(caughtUpFrame(40));
    await flush();
    expect(texts(session.getState())).toEqual(["fresh start"]);
    expect(session.getState().cursor).toBe(40n);
    session.close();
  });

  it("resumes a session from a persisted state's cursor", async () => {
    server = scriptedServer();
    const client = createReliantClient({ transport: server.transport, wake: noWake });
    const initialState = { ...createChatState(CHAT, { cursor: 77n }), messages: [] };
    const session = client.chat({ chatId: CHAT, initialState }).start();
    await flush();
    expect(server.chatStreams()[0]!.request).toMatchObject({ chatId: CHAT, sinceSeq: 77n });
    session.close();
  });

  it("goes idle on a chat the server refuses instead of retrying", async () => {
    const session = open().start();
    await flush();
    server.chatStreams()[0]!.fail(new ConnectError("not found", Code.NotFound));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(server.chatStreams()).toHaveLength(1);
    expect(session.getError()).toMatch(/refused/);
  });

  it("notifies subscribers once per change", async () => {
    const session = open().start();
    const seen: number[] = [];
    const off = session.subscribe(() => seen.push(session.getState().messages.length));
    await flush();
    server.chatStreams()[0]!.push(snapshotFrame(3, [textMessage("m-1", 1, 1, "hi")]));
    await flush();
    expect(seen.at(-1)).toBe(1);
    off();
    const before = seen.length;
    server.chatStreams()[0]!.push(updatesFrame([messageData(4, "m-2", "yo")], 4));
    await flush();
    expect(seen).toHaveLength(before);
    session.close();
  });
});

describe("sending", () => {
  const ok = (calls: Array<Record<string, unknown>>) => (request: Record<string, unknown>) => {
    calls.push(request);
    return { chatId: CHAT, messageId: "m-sent", runId: "run-1", queued: false };
  };

  it("sends under a client message id and keeps the optimistic entry until the message arrives", async () => {
    const calls: Array<Record<string, unknown>> = [];
    const session = open({ SendMessage: ok(calls) }).start();
    await flush();
    server.chatStreams()[0]!.push(snapshotFrame(2, [textMessage("m-1", 2, 1, "hello")]));
    await flush();

    const result = await session.send("deploy it");
    expect(result).toMatchObject({ messageId: "m-sent", queued: false });
    expect(calls).toHaveLength(1);
    expect(calls[0]).toMatchObject({ chatId: CHAT, clientMessageId: result.clientMessageId });
    expect(calls[0]!.messages).toMatchObject([{ content: "deploy it" }]);
    expect(texts(session.getState())).toEqual(["hello", "deploy it"]);

    // The real user message retires the optimistic one.
    server.chatStreams()[0]!.push(updatesFrame([messageData(3, "m-sent", "deploy it", 1)], 3));
    await flush();
    expect(texts(session.getState())).toEqual(["hello", "deploy it"]);
    expect(session.getState().messages.map((m) => m.id)).toEqual(["m-1", "m-sent"]);
    session.close();
  });

  it("retries a transient failure under the same id and reports success once", async () => {
    let attempts = 0;
    const calls: Array<Record<string, unknown>> = [];
    const session = open({
      SendMessage: (request: Record<string, unknown>) => {
        calls.push(request);
        if (++attempts < 3) throw new ConnectError("blip", Code.Unavailable);
        return { chatId: CHAT, messageId: "m-sent", queued: false };
      },
    });
    await session.send("hello");
    expect(calls).toHaveLength(3);
    expect(new Set(calls.map((c) => c.clientMessageId)).size).toBe(1);
  });

  it("marks a send that fails for good as not sent, and sending it again reuses its id", async () => {
    const calls: Array<Record<string, unknown>> = [];
    let fail = true;
    const session = open({
      SendMessage: (request: Record<string, unknown>) => {
        calls.push(request);
        if (fail) throw new ConnectError("down", Code.Unavailable);
        return { chatId: CHAT, messageId: "m-sent", queued: false };
      },
    });

    await expect(session.send("hello", { retries: 1 })).rejects.toThrow("down");
    expect(calls).toHaveLength(2);
    const failed = session.getState().messages.find((m) => m.id.startsWith("optimistic-failed-"));
    expect(failed).toBeDefined();

    fail = false;
    await session.retry(failed!.id);
    expect(new Set(calls.map((c) => c.clientMessageId)).size).toBe(1);
    expect(session.getState().messages.some((m) => m.id.startsWith("optimistic-failed-"))).toBe(false);
  });

  it("does not retry a refusal", async () => {
    const calls: unknown[] = [];
    const session = open({
      SendMessage: (r: Record<string, unknown>) => {
        calls.push(r);
        throw new ConnectError("no", Code.PermissionDenied);
      },
    });
    await expect(session.send("hello")).rejects.toThrow("no");
    expect(calls).toHaveLength(1);
  });

  it("drops the optimistic entry of a message the server queued for a running thread", async () => {
    const session = open({
      SendMessage: () => ({ chatId: CHAT, messageId: "q-1", queued: true }),
    });
    const result = await session.send("when you are done");
    expect(result.queued).toBe(true);
    expect(session.getState().messages).toHaveLength(0);
  });

  it("gives a different message a different id", async () => {
    const calls: Array<Record<string, unknown>> = [];
    const session = open({ SendMessage: ok(calls) });
    await session.send("one");
    await session.send("two");
    await session.send("one");
    // "one" succeeded the first time, so its id was retired: the third is a new message.
    expect(new Set(calls.map((c) => c.clientMessageId)).size).toBe(3);
  });
});
