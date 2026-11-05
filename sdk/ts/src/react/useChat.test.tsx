// @vitest-environment jsdom
import { act } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { ChatUpdateType } from "#gen/reliant/v1/streaming_pb.js";
import { harness, settle } from "../test/react-harness.js";
import {
  approvalData,
  CHAT,
  caughtUpFrame,
  deltaData,
  messageData,
  snapshotFrame,
  textMessage,
  updateData,
  updatesFrame,
} from "../test/scripted-server.js";
import { useChat, useThread } from "./useChat.js";

const SUB = "d1f1a2b3-0000-0000-0000-00000000aaaa";
const texts = (messages: { contentBlocks: { content?: string }[] }[]) =>
  messages.map((m) => m.contentBlocks.map((b) => b.content).join(""));

describe("useChat", () => {
  it("streams the chat: snapshot, catch-up, live deltas, an approval, and a persisted message", async () => {
    const h = harness();
    const { result, unmount } = h.render(() => useChat(CHAT));
    await settle();

    expect(h.server.chatStreams()).toHaveLength(1);
    expect(h.server.chatStreams()[0]!.request).toMatchObject({ chatId: CHAT, sinceSeq: 0n });
    expect(result.current.syncPending).toBe(true);
    expect(result.current.status).toBe("connecting");

    const conn = h.server.chatStreams()[0]!;
    act(() => {
      conn.push(snapshotFrame(10, [textMessage("m-1", 1, 1, "ship it")], [approvalData(10, "pending")]));
      conn.push(caughtUpFrame(10));
    });
    await settle();
    expect(result.current.status).toBe("connected");
    expect(result.current.syncPending).toBe(false);
    expect(texts(result.current.messages)).toEqual(["ship it"]);
    expect(result.current.pendingApprovals.map((a) => a.id)).toEqual(["appr-1"]);

    act(() => {
      conn.push(
        updatesFrame(
          [deltaData("message_start"), deltaData("content_block_start"), deltaData("content_block_delta", { delta: "wor" })],
          10,
        ),
      );
    });
    await settle();
    expect(result.current.streaming[CHAT]?.contentBlocks[0]?.content).toBe("wor");

    act(() => {
      conn.push(updatesFrame([messageData(11, "m-live", "working"), approvalData(12, "approved")], 12));
    });
    await settle();
    expect(texts(result.current.messages)).toEqual(["ship it", "working"]);
    expect(result.current.streaming[CHAT]).toBeUndefined();
    expect(result.current.pendingApprovals).toEqual([]);
    unmount();
  });

  it("shares one stream between hooks on the same chat, and closes it when the last one unmounts", async () => {
    const h = harness();
    const a = h.render(() => useChat(CHAT));
    const b = h.render(() => useChat(CHAT));
    await settle();
    expect(h.server.chatStreams()).toHaveLength(1);

    act(() => {
      h.server.chatStreams()[0]!.push(snapshotFrame(3, [textMessage("m-1", 1, 1, "hello")]));
    });
    await settle();
    expect(texts(a.result.current.messages)).toEqual(["hello"]);
    expect(texts(b.result.current.messages)).toEqual(["hello"]);

    a.unmount();
    await settle();
    expect(h.server.chatStreams()[0]!.signal?.aborted).toBe(false);
    b.unmount();
    await settle();
    expect(h.server.chatStreams()[0]!.signal?.aborted).toBe(true);
  });

  it("opens one stream per project scope: the same chat under two projects is not shared", async () => {
    const h = harness();
    const a = h.render(() => useChat(CHAT, { projectId: "p-a" }));
    const b = h.render(() => useChat(CHAT, { projectId: "p-b" }));
    const a2 = h.render(() => useChat(CHAT, { projectId: "p-a" }));
    await settle();
    const streams = h.server.chatStreams();
    expect(streams).toHaveLength(2);
    expect(streams.map((c) => c.request.projectId).sort()).toEqual(["p-a", "p-b"]);

    // Each scope's stream feeds only the hooks that asked for it.
    const forA = streams.find((c) => c.request.projectId === "p-a")!;
    act(() => forA.push(snapshotFrame(2, [textMessage("m-1", 1, 1, "for a")])));
    await settle();
    expect(texts(a.result.current.messages)).toEqual(["for a"]);
    expect(texts(a2.result.current.messages)).toEqual(["for a"]);
    expect(b.result.current.messages).toEqual([]);
  });

  it("holds nothing while disabled or without a chat id", async () => {
    const h = harness();
    const none = h.render(() => useChat(undefined));
    const off = h.render(() => useChat(CHAT, { enabled: false }));
    await settle();
    expect(h.server.connections).toHaveLength(0);
    expect(none.result.current.messages).toEqual([]);
    expect(off.result.current.status).toBe("disconnected");
    await expect(none.result.current.send("x")).rejects.toThrow(/not enabled/);
  });

  it("retries a transient send failure under one id, and keeps a failed send for retry()", async () => {
    let calls = 0;
    const ids: string[] = [];
    const h = harness({
      SendMessage: (req) => {
        calls++;
        ids.push(req.clientMessageId as string);
        if (calls === 1) throw new ConnectError("blip", Code.Unavailable);
        if (calls === 2) return { messageId: "m-9", queued: false, runId: "r-1" };
        if (calls === 3) throw new ConnectError("no", Code.FailedPrecondition);
        return { messageId: "m-10", queued: false, runId: "r-1" };
      },
    });
    const { result } = h.render(() => useChat(CHAT));
    await settle();

    const sent = await act(() => result.current.send("deploy it", { retries: 1 }));
    expect(sent).toMatchObject({ messageId: "m-9", queued: false });
    expect(ids[0]).toBe(ids[1]);

    await act(async () => {
      await expect(result.current.send("again", { retries: 0 })).rejects.toThrow("no");
    });
    const failed = result.current.messages.find((m) => m.id.startsWith("optimistic-failed-"));
    expect(failed).toBeDefined();
    expect(texts([failed!])).toEqual(["again"]);

    // retry() resends exactly that request under the id it already has.
    await act(() => result.current.retry(failed!.id, { retries: 0 }));
    expect(ids[3]).toBe(ids[2]);
    expect(result.current.messages.some((m) => m.id.startsWith("optimistic-failed-"))).toBe(false);
  });

  it("pauses, resumes and interrupts through the chat service", async () => {
    const seen: Array<[string, Record<string, unknown>]> = [];
    const h = harness({
      PauseChat: (r) => (seen.push(["pause", r]), { success: true }),
      ResumeChat: (r) => (seen.push(["resume", r]), { success: true }),
      InterruptThread: (r) => (seen.push(["interrupt", r]), { cancelledToolCalls: 2 }),
    });
    const { result } = h.render(() => useChat(CHAT));
    await settle();
    await act(async () => {
      await result.current.pause();
      await result.current.resume();
      expect(await result.current.interrupt()).toMatchObject({ cancelledToolCalls: 2 });
      await result.current.interrupt(SUB);
    });
    expect(seen.map(([name]) => name)).toEqual(["pause", "resume", "interrupt", "interrupt"]);
    expect(seen[2]![1]).toMatchObject({ chatId: CHAT, threadId: CHAT });
    expect(seen[3]![1]).toMatchObject({ chatId: CHAT, threadId: SUB });
  });

  it("a short page is not the end of history while the server says more remains", async () => {
    const h = harness({
      ListMessages: () => ({ messages: [textMessage("m-0", 1, 5, "earlier")], total: 9, hasMore: true }),
    });
    const { result } = h.render(() => useChat(CHAT));
    await settle();
    const snapshot = snapshotFrame(10, [textMessage("m-1", 1, 6, "recent")]);
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).hasMore = true;
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).oldestSeq = 6n;
    act(() => h.server.chatStreams()[0]!.push(snapshot));
    await settle();

    // Asked for 100, got 1: filtered or truncated, with earlier history still there.
    let more = false;
    await act(async () => {
      more = await result.current.loadOlder(100);
    });
    expect(more).toBe(true);
    expect(result.current.hasMore).toBe(true);
    expect(texts(result.current.messages)).toEqual(["earlier", "recent"]);
  });

  it("an empty page, or a short one the server calls the last, ends history", async () => {
    let page = 0;
    const h = harness({
      ListMessages: () => (++page === 1 ? { messages: [textMessage("m-0", 1, 5, "earlier")], hasMore: false } : { messages: [] }),
    });
    const { result } = h.render(() => useChat(CHAT));
    await settle();
    const snapshot = snapshotFrame(10, [textMessage("m-1", 1, 6, "recent")]);
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).hasMore = true;
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).oldestSeq = 6n;
    act(() => h.server.chatStreams()[0]!.push(snapshot));
    await settle();

    let more = true;
    await act(async () => {
      more = await result.current.loadOlder(100);
    });
    expect(more).toBe(false);
    expect(result.current.hasMore).toBe(false);
  });

  it("pages older history in, one page at a time", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const asked: Array<Record<string, unknown>> = [];
    const h = harness({
      ListMessages: async (req) => {
        asked.push(req);
        await gate;
        return { messages: [textMessage("m-0", 1, 5, "earlier"), textMessage("m-00", 2, 4, "earliest")], total: 6 };
      },
    });
    const { result } = h.render(() => useChat(CHAT));
    await settle();
    const snapshot = snapshotFrame(10, [textMessage("m-1", 1, 6, "recent")]);
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).hasMore = true;
    (snapshot.event.value as { hasMore: boolean; oldestSeq: bigint }).oldestSeq = 6n;
    act(() => {
      h.server.chatStreams()[0]!.push(snapshot);
      h.server.chatStreams()[0]!.push(caughtUpFrame(10));
    });
    await settle();
    expect(result.current.hasMore).toBe(true);

    let first!: Promise<boolean>;
    let second!: Promise<boolean>;
    act(() => {
      first = result.current.loadOlder(2);
      second = result.current.loadOlder(2);
    });
    expect(result.current.loadingOlder).toBe(true);
    release();
    await act(async () => {
      expect(await second).toBe(true); // joined the page in flight
      expect(await first).toBe(true); // a full page: more may remain
    });
    expect(asked).toHaveLength(1);
    expect(asked[0]).toMatchObject({ chatId: CHAT, beforeSeq: 6n, recent: 2 });
    expect(texts(result.current.messages)).toEqual(["earliest", "earlier", "recent"]);
    expect(result.current.loadingOlder).toBe(false);
  });
});

describe("useThread", () => {
  const threadUpdate = (seq: number, status: string) =>
    updateData(ChatUpdateType.THREAD, seq, SUB, {
      id: SUB,
      chat_id: CHAT,
      thread: SUB,
      title: "Researcher",
      is_planning_mode: false,
      status,
      created_at: "2026-01-01T00:00:00.000Z",
    });

  it("reads one thread of the chat: its messages, streaming turn and record", async () => {
    const h = harness();
    const main = h.render(() => useThread(CHAT, CHAT));
    const sub = h.render(() => useThread(CHAT, SUB));
    await settle();
    expect(h.server.chatStreams()).toHaveLength(1);

    act(() => {
      h.server.chatStreams()[0]!.push(
        updatesFrame(
          [
            messageData(1, "m-main", "lead says hi", 2, ""),
            messageData(2, "m-sub", "researching", 2, SUB),
            threadUpdate(3, "running"),
          ],
          3,
        ),
      );
    });
    await settle();
    // A turn streaming under the thread (apart from the batch of a persisted
    // assistant message, which would retire its placeholder).
    act(() => {
      h.server.chatStreams()[0]!.push(
        updatesFrame(
          [
            deltaData("message_start", { thread: SUB }, "m-sub-live"),
            deltaData("content_block_start", { thread: SUB }, "m-sub-live"),
            deltaData("content_block_delta", { thread: SUB, delta: "found" }, "m-sub-live"),
          ],
          3,
        ),
      );
    });
    await settle();
    expect(texts(main.result.current.messages)).toEqual(["lead says hi"]);
    expect(texts(sub.result.current.messages)).toEqual(["researching"]);
    expect(sub.result.current.thread).toMatchObject({ title: "Researcher", status: "running" });
    expect(sub.result.current.streaming?.contentBlocks[0]?.content).toBe("found");
    expect(sub.result.current.active).toBe(true);
    expect(main.result.current.active).toBe(false);
  });

  it("sends to the thread it reads, and interrupts only that thread", async () => {
    const sends: Array<Record<string, unknown>> = [];
    const interrupts: Array<Record<string, unknown>> = [];
    const h = harness({
      SendMessage: (r) => (sends.push(r), { messageId: "m", runId: "r" }),
      InterruptThread: (r) => (interrupts.push(r), {}),
    });
    const main = h.render(() => useThread(CHAT, CHAT));
    const sub = h.render(() => useThread(CHAT, SUB));
    await settle();
    await act(async () => {
      await main.result.current.send("to lead");
      await sub.result.current.send("to sub");
      await sub.result.current.interrupt();
    });
    expect(sends[0]!.targetThread).toBeFalsy();
    expect(sends[1]).toMatchObject({ targetThread: SUB });
    expect(interrupts).toEqual([expect.objectContaining({ chatId: CHAT, threadId: SUB })]);
  });
});
