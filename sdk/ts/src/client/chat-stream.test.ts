// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { ChatStream } from "./chat-stream.js";
import type { ChatUpdate } from "./events.js";
import { FIRST_FRAME_TIMEOUT_MS, type WakeSource } from "./reconnecting-stream.js";
import { CHAT, caughtUpFrame, heartbeatFrame, scriptedServer, toolCallData, updatesFrame } from "../test/scripted-server.js";

// The chat stream's recovery from what a mobile page meets: sequence gaps in
// quick succession, gap resyncs after an earlier network failure, and an
// attempt whose first frame never comes. Each of these left the chat stale
// for seconds, minutes or for good.

const flush = () => vi.advanceTimersByTimeAsync(0);
const noWake: WakeSource = { subscribe: () => () => {}, isVisible: () => true };
const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => from + i);
const tools = (seqs: number[]) => seqs.map((seq) => toolCallData(seq, `tc-${seq}`, "completed"));

let server: ReturnType<typeof scriptedServer>;
let applied: number;

function openStream(resumeFrom: bigint): ChatStream {
  server = scriptedServer();
  applied = 0;
  const stream = new ChatStream({
    client: server.streamingClient,
    wake: noWake,
    callbacks: {
      onChatUpdate: (_chatId: string, updates: ChatUpdate[]) => {
        applied += updates.length;
      },
      onChatSnapshot: () => {},
      resolveChatResumeSequence: () => resumeFrom,
    },
  });
  stream.subscribeToChat(CHAT);
  return stream;
}

const conns = () => server.chatStreams();
const cursorOf = (i: number) => conns()[i]!.request.sinceSeq;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
  vi.spyOn(Math, "random").mockReturnValue(0);
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("ChatStream gap resync", () => {
  it("resyncs a gap that arrives within a second of a resync that already caught up", async () => {
    const stream = openStream(10n);
    await flush();
    conns()[0]!.push(caughtUpFrame(10));
    conns()[0]!.push(updatesFrame(tools([12]), 12));
    await flush();
    expect(conns()).toHaveLength(2);
    expect(cursorOf(1)).toBe(10n);

    // The resync replays 11..20 and catches up.
    conns()[1]!.push(updatesFrame(tools(range(11, 20)), 20));
    conns()[1]!.push(caughtUpFrame(20));
    await flush();
    expect(stream.getLastChatSequence()).toBe(20);

    // 21 is lost upstream; 22 arrives half a second after the resync began.
    await vi.advanceTimersByTimeAsync(500);
    conns()[1]!.push(updatesFrame(tools([22]), 22));
    await flush();
    expect(conns()).toHaveLength(2);

    // It is resynced when the pacing interval ends, never dropped.
    await vi.advanceTimersByTimeAsync(500);
    expect(conns()).toHaveLength(3);
    expect(cursorOf(2)).toBe(20n);
    conns()[2]!.push(updatesFrame(tools([21, 22]), 22));
    conns()[2]!.push(caughtUpFrame(22));
    await flush();
    expect(stream.getLastChatSequence()).toBe(22);
    expect(applied).toBe(12);

    // Covered: nothing more to resync.
    for (let i = 0; i < 20; i++) {
      await vi.advanceTimersByTimeAsync(30_000);
      conns().at(-1)!.push(heartbeatFrame());
    }
    expect(conns()).toHaveLength(3);
    stream.stop();
  });

  it("resyncs again after caught_up when a gap seen during the resync is still ahead of the cursor", async () => {
    const stream = openStream(10n);
    await flush();
    conns()[0]!.push(caughtUpFrame(10));
    conns()[0]!.push(updatesFrame(tools([12]), 12));
    await flush();
    expect(conns()).toHaveLength(2);

    // The resync's connection sees 11..20, then a gap at 22, before caught_up.
    conns()[1]!.push(updatesFrame(tools([...range(11, 20), 22]), 22));
    await flush();
    expect(conns()).toHaveLength(2);
    conns()[1]!.push(caughtUpFrame(22));
    await vi.advanceTimersByTimeAsync(1_000);

    expect(conns()).toHaveLength(3);
    expect(cursorOf(2)).toBe(20n);
    stream.stop();
  });

  it("does not resync again when the resync's replay covered the gap", async () => {
    const stream = openStream(10n);
    await flush();
    conns()[0]!.push(caughtUpFrame(10));
    conns()[0]!.push(updatesFrame(tools([12]), 12));
    await flush();
    conns()[1]!.push(updatesFrame(tools([11, 12]), 12));
    conns()[1]!.push(caughtUpFrame(12));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(conns()).toHaveLength(2);
    expect(stream.getLastChatSequence()).toBe(12);
    stream.stop();
  });

  it("slows down resyncs that never move the cursor", async () => {
    const stream = openStream(10n);
    await flush();
    conns()[0]!.push(caughtUpFrame(12));
    conns()[0]!.push(updatesFrame(tools([12]), 12));
    await flush();

    // 11 is missing from every replay too: each resync ends where it began.
    const opensAt: number[] = [];
    let answered = 1;
    for (let t = 0; t <= 40_000; t += 100) {
      while (answered < conns().length) {
        opensAt.push(t);
        conns()[answered]!.push(updatesFrame(tools([12]), 12));
        conns()[answered]!.push(caughtUpFrame(12));
        answered++;
        await flush();
      }
      await vi.advanceTimersByTimeAsync(100);
    }
    const gaps = opensAt.slice(1).map((t, i) => t - opensAt[i]!);
    expect(gaps.slice(0, 4)).toEqual([1_000, 2_000, 4_000, 8_000]);
    expect(stream.getLastChatSequence()).toBe(10);
    stream.stop();
  });

  it("opens a gap resync at once after an earlier network failure", async () => {
    const stream = openStream(100n);
    await flush();
    // One failed attempt (iOS "Load failed" on cellular) leaves a backoff.
    conns()[0]!.fail(new ConnectError("Load failed", Code.Unavailable));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(conns()).toHaveLength(2);
    conns()[1]!.push(caughtUpFrame(100));
    await flush();

    // Each gap on the live connection resyncs without waiting out a backoff.
    let seq = 100;
    for (let round = 0; round < 6; round++) {
      const before = conns().length;
      conns().at(-1)!.push(updatesFrame(tools([seq + 2]), seq + 2));
      await flush();
      expect(conns()).toHaveLength(before + 1);
      seq += 30;
      conns().at(-1)!.push(updatesFrame(tools(range(seq - 29, seq)), seq));
      conns().at(-1)!.push(caughtUpFrame(seq));
      await vi.advanceTimersByTimeAsync(1_100);
    }
    stream.stop();
  });
});

describe("ChatStream first frame", () => {
  it("replaces an attempt that never gets its first frame while the page is visible", async () => {
    const stream = openStream(0n);
    await flush();
    expect(conns()).toHaveLength(1);

    // A redundant re-assert while the attempt is in flight is still a no-op ...
    stream.subscribeToChat(CHAT);
    await flush();
    expect(conns()).toHaveLength(1);

    // ... because the attempt itself is bounded.
    await vi.advanceTimersByTimeAsync(FIRST_FRAME_TIMEOUT_MS + 1_000);
    expect(conns()).toHaveLength(2);
    expect(conns()[0]!.signal?.aborted).toBe(true);
    conns()[1]!.push(caughtUpFrame(0));
    await flush();
    expect(stream.isConnected()).toBe(true);
    stream.stop();
  });
});
