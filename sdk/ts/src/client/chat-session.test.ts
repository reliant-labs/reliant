// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { createChatState } from "./chat-reducer.js";
import type { ChatState } from "./chat-state.js";
import { createReliantClient } from "./client.js";
import {
  fetchContentBlockBodies,
  MAX_CONTENT_BLOCK_BODY_IDS,
  type ContentBlockBodiesClient,
} from "./content-block-bodies.js";
import { ToolBodyMode } from "../gen/reliant/v1/chat_pb.js";
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

  it("retries the exact failed request (attachments, child thread) under its id, even after the id cache evicted it", async () => {
    const calls: Array<Record<string, unknown>> = [];
    let fail = true;
    const session = open({
      SendMessage: (request: Record<string, unknown>) => {
        calls.push(request);
        if (fail && request.targetThread === "t-child") throw new ConnectError("down", Code.Unavailable);
        return { chatId: CHAT, messageId: `m-${calls.length}`, queued: false };
      },
    });

    await expect(
      session.send("look at this", { attachments: ["att-1"], targetThread: "t-child", retries: 0 }),
    ).rejects.toThrow("down");
    const failed = session.getState().messages.find((m) => m.id.startsWith("optimistic-failed-"))!;
    const firstId = calls[0]!.clientMessageId;

    // Far more sends than the id cache keeps, each failing for good so it keeps its id.
    for (let i = 0; i < 40; i++) {
      await expect(session.send(`other ${i}`, { targetThread: "t-child", retries: 0 })).rejects.toThrow("down");
    }

    fail = false;
    const result = await session.retry(failed.id);
    const last = calls.at(-1)!;
    expect(result.clientMessageId).toBe(firstId);
    expect(last).toMatchObject({ clientMessageId: firstId, attachments: ["att-1"], targetThread: "t-child" });
    expect(last.messages).toMatchObject([{ content: "look at this" }]);
    expect(session.getState().messages.some((m) => m.id === failed.id)).toBe(false);
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

describe("chat stream sync hooks", () => {
  // The next connection, however long its reconnect backoff (jittered) takes.
  async function connection(n: number) {
    for (let i = 0; i < 200 && server.chatStreams().length < n; i++) {
      await vi.advanceTimersByTimeAsync(100);
    }
    return server.chatStreams()[n - 1]!;
  }

  it("reports each connection's snapshot or replay, and its caught_up, once", async () => {
    server = scriptedServer();
    const hooks = {
      onSnapshotReceived: vi.fn(),
      onReplayStarted: vi.fn(),
      onCaughtUp: vi.fn(),
      onSnapshotApplied: vi.fn(),
    };
    const client = createReliantClient({ transport: server.transport, wake: noWake });
    const session = client.chat({ chatId: CHAT, sleep: async () => {}, hooks }).start();
    await flush();

    // A first open: snapshot, then caught_up. Live updates after it are not a replay.
    const first = server.chatStreams()[0]!;
    first.push(snapshotFrame(10, [textMessage("m-1", 1, 1, "ship it")]));
    first.push(caughtUpFrame(10));
    first.push(updatesFrame([messageData(11, "m-2", "done")], 11));
    await flush();
    expect(hooks.onSnapshotReceived).toHaveBeenCalledWith({ chatId: CHAT, messages: 1, otherUpdates: 0 });
    // Deprecated, still reported for existing consumers.
    expect(hooks.onSnapshotApplied).toHaveBeenCalledOnce();
    expect(hooks.onReplayStarted).not.toHaveBeenCalled();
    expect(hooks.onCaughtUp).toHaveBeenCalledExactlyOnceWith({
      chatId: CHAT,
      sync: "snapshot",
      initialSync: true,
      replayedUpdates: 0,
      latestSequence: 10,
    });

    // A reconnect replays from the cursor: a resync, not the initial sync.
    first.fail(new ConnectError("connection reset", Code.Unavailable));
    const second = await connection(2);
    second.push(updatesFrame([messageData(12, "m-3", "a"), deltaData("message_start")], 12));
    second.push(updatesFrame([messageData(13, "m-5", "b")], 13));
    second.push(caughtUpFrame(13));
    await flush();
    expect(hooks.onReplayStarted).toHaveBeenCalledExactlyOnceWith({ chatId: CHAT, sinceSequence: 11, updates: 1 });
    expect(hooks.onCaughtUp).toHaveBeenLastCalledWith({
      chatId: CHAT,
      sync: "replay",
      initialSync: false,
      replayedUpdates: 2,
      latestSequence: 13,
    });

    // A replay of nothing sends only caught_up.
    second.fail(new ConnectError("connection reset", Code.Unavailable));
    (await connection(3)).push(caughtUpFrame(13));
    await flush();
    expect(hooks.onReplayStarted).toHaveBeenCalledOnce();
    expect(hooks.onCaughtUp).toHaveBeenCalledTimes(3);
    expect(hooks.onCaughtUp).toHaveBeenLastCalledWith(expect.objectContaining({ sync: "replay", replayedUpdates: 0 }));
    session.close();
  });
});

describe("toolBodies", () => {
  // A snapshot that says older history remains, so loadOlder has a page to ask for.
  const pagedSnapshot = () => {
    const snapshot = snapshotFrame(10, [textMessage("m-1", 1, 6, "recent")]);
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).hasMore = true;
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).oldestSeq = 6n;
    return snapshot;
  };
  const openWith = async (toolBodies?: "full" | "stub") => {
    server = scriptedServer({ ListMessages: () => ({ messages: [textMessage("m-0", 1, 5, "earlier")], hasMore: false }) });
    const client = createReliantClient({ transport: server.transport, wake: noWake });
    const session = client.chat({ chatId: CHAT, sleep: async () => {}, toolBodies }).start();
    await flush();
    server.chatStreams()[0]!.push(pagedSnapshot());
    await flush();
    await session.loadOlder(50);
    session.close();
    return {
      stream: server.chatStreams()[0]!.request,
      page: server.unaryRequests.find((r) => r.method === "ListMessages")!.request,
    };
  };

  it('"stub" reaches the stream request and every ListMessages page', async () => {
    const { stream, page } = await openWith("stub");
    expect(stream).toMatchObject({ chatId: CHAT, toolBodies: ToolBodyMode.STUB });
    expect(page).toMatchObject({ chatId: CHAT, recent: 50, beforeSeq: 6n, toolBodies: ToolBodyMode.STUB });
  });

  it("is full by default: the field is left unset, which every server reads as FULL", async () => {
    for (const toolBodies of [undefined, "full"] as const) {
      const { stream, page } = await openWith(toolBodies);
      expect(stream.toolBodies).toBeUndefined();
      expect(page.toolBodies).toBeUndefined();
    }
  });
});

describe("fetchContentBlockBodies", () => {
  const body = (id: string) => ({ blockId: id, input: `input of ${id}` });

  it("sends at most 100 ids a request, asks again for the deferred, and accounts for every id once", async () => {
    const asked: string[][] = [];
    const client: ContentBlockBodiesClient = {
      async getContentBlockBodies(request) {
        asked.push(request.blockIds);
        // The server's byte budget: answer the first two, defer the rest of the batch.
        const [first, second, ...rest] = request.blockIds;
        const answered = [first!, second].filter((id): id is string => !!id);
        return {
          bodies: answered.filter((id) => id !== "gone").map(body),
          notFound: answered.filter((id) => id === "gone"),
          deferred: rest,
        };
      },
    };
    const ids = ["gone", ...Array.from({ length: 149 }, (_, i) => `b-${i}`), "b-0", ""];
    const got = await fetchContentBlockBodies(client, CHAT, ids, { full: true });

    expect(asked.every((batch) => batch.length <= MAX_CONTENT_BLOCK_BODY_IDS)).toBe(true);
    expect(asked[0]).toHaveLength(100);
    expect(asked[0]![0]).toBe("gone");
    expect(Object.keys(got.bodies)).toHaveLength(149);
    expect(got.bodies["b-148"]).toEqual(body("b-148"));
    expect(got.notFound).toEqual(["gone"]);
  });

  it("treats an id the server names nowhere as not found, and refuses to loop on a response that answers nothing", async () => {
    const quiet: ContentBlockBodiesClient = {
      getContentBlockBodies: async () => ({ bodies: [body("a")], notFound: [], deferred: [] }),
    };
    await expect(fetchContentBlockBodies(quiet, CHAT, ["a", "b"])).resolves.toEqual({ bodies: { a: body("a") }, notFound: ["b"] });

    const stuck: ContentBlockBodiesClient = {
      getContentBlockBodies: async (request) => ({ bodies: [], notFound: [], deferred: request.blockIds }),
    };
    await expect(fetchContentBlockBodies(stuck, CHAT, ["a"])).rejects.toThrow(/answered none/);
  });

  it("ends when the server answers other ids but defers one on every response", async () => {
    let calls = 0;
    const stubborn: ContentBlockBodiesClient = {
      async getContentBlockBodies(request) {
        calls++;
        // Answer every id but "x" (and an id nobody asked for); always defer "x".
        const others = request.blockIds.filter((id) => id !== "x");
        return { bodies: [...others, "unasked"].map(body), notFound: [], deferred: ["x"] };
      },
    };
    await expect(fetchContentBlockBodies(stubborn, CHAT, ["a", "x", "b"])).rejects.toThrow(/answered none/);
    expect(calls).toBe(2);
  });

  it("is a ChatSession method over the session's own ChatService client", async () => {
    server = scriptedServer({
      GetContentBlockBodies: (r) => ({
        bodies: (r.blockIds as string[]).map((id) => ({ blockId: id, content: "whole", originalSize: 5n })),
      }),
    });
    const client = createReliantClient({ transport: server.transport, wake: noWake });
    const session = client.chat({ chatId: CHAT });
    const got = await session.fetchContentBlockBodies(["r-1"], { full: true });
    expect(got.bodies["r-1"]).toMatchObject({ blockId: "r-1", content: "whole", originalSize: 5n });
    expect(server.unaryRequests).toEqual([
      { method: "GetContentBlockBodies", request: expect.objectContaining({ chatId: CHAT, blockIds: ["r-1"], full: true }) },
    ]);
  });
});
