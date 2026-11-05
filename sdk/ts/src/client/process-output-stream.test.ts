import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ProcessOutputStream, type BackgroundClient } from "./process-output-stream.js";

// An API replica that is shutting down ends every stream it holds with an OK
// status. A process-output stream ended that way has NOT reached the end of the
// output: the process is still running. Only a stream that delivered the
// "complete" event is over.

let streams: Array<() => AsyncIterable<unknown>> = [];
const requests: Array<{ processId: string; newOnly: boolean; daemonId?: string }> = [];

const client = (): BackgroundClient =>
  ({
    streamProcessOutput: (request: { processId: string; newOnly: boolean; daemonId?: string }) => {
      requests.push({ processId: request.processId, newOnly: request.newOnly, daemonId: request.daemonId });
      const next = streams.shift();
      if (!next) throw new Error("no stream scripted for this connection");
      return next();
    },
  }) as unknown as BackgroundClient;

const line = { event: { case: "line", value: { type: 1, text: "building…", sequence: 1n } } };
const complete = { event: { case: "complete", value: { status: 2, exitCode: 0, endTime: "" } } };
const snapshot = { event: { case: "snapshot", value: { lines: [{ type: 2, text: "warn", sequence: 3n }], latestSequence: 3n, isComplete: false } } };

const endsAfter = (...events: unknown[]) => () => ({
  async *[Symbol.asyncIterator]() {
    for (const event of events) yield event;
  },
});
const staysOpenAfter = (...events: unknown[]) => () => ({
  async *[Symbol.asyncIterator]() {
    for (const event of events) yield event;
    await new Promise(() => undefined);
  },
});

function make(daemonId?: string) {
  const callbacks = { onSnapshot: vi.fn(), onLine: vi.fn(), onComplete: vi.fn(), onError: vi.fn(), onStatusChange: vi.fn() };
  const stream = new ProcessOutputStream({ processId: "proc-1234-5678", callbacks, client, daemonId });
  return { stream, callbacks };
}

beforeEach(() => {
  vi.useFakeTimers();
  streams = [];
  requests.length = 0;
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("a process-output stream the server ended", () => {
  it("reconnects when it ended without the complete event, asking only for new output", async () => {
    streams = [endsAfter(line), staysOpenAfter(line)];
    const { stream, callbacks } = make();
    stream.start();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(requests).toHaveLength(2);
    expect(requests.map((r) => r.newOnly)).toEqual([false, true]);
    expect(stream.isConnected()).toBe(true);
    expect(callbacks.onError).not.toHaveBeenCalled();
    stream.stop();
  });

  it("stays ended after the complete event", async () => {
    streams = [endsAfter(line, complete)];
    const { stream, callbacks } = make();
    stream.start();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(requests).toHaveLength(1);
    expect(callbacks.onComplete).toHaveBeenCalledTimes(1);
  });

  it("backs off and gives up on a server that ends every stream at once", async () => {
    // Answering and immediately ending is not a recovered server: the first
    // event must not reset the backoff, or this reconnects every second.
    streams = Array.from({ length: 10 }, () => endsAfter(line));
    const { stream, callbacks } = make();
    stream.start();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(requests).toHaveLength(6); // the first connection + 5 attempts
    expect(callbacks.onError).toHaveBeenCalledWith("Max reconnection attempts reached");
  });

  it("is not subject to the silence watchdog: a quiet process is not a dead connection", async () => {
    streams = [staysOpenAfter(line)];
    const { stream } = make();
    stream.start();
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(requests).toHaveLength(1);
    stream.stop();
  });
});

describe("what it reports", () => {
  it("hands over the snapshot and each line with their type and sequence, and remembers the last sequence", async () => {
    streams = [staysOpenAfter(snapshot, line)];
    const { stream, callbacks } = make();
    stream.start();
    await vi.advanceTimersByTimeAsync(10);
    expect(callbacks.onSnapshot).toHaveBeenCalledWith([{ type: "stderr", text: "warn", sequence: 3 }], 3);
    expect(callbacks.onLine).toHaveBeenCalledWith({ type: "stdout", text: "building…", sequence: 1 });
    expect(stream.getLastSequence()).toBe(1);
    stream.stop();
  });
});

describe("a process on a named machine", () => {
  it("asks that machine for the output, on every reconnect too", async () => {
    streams = [endsAfter(line), staysOpenAfter(line)];
    const { stream } = make("daemon-gpu");
    stream.start();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(requests.length).toBeGreaterThanOrEqual(2);
    for (const request of requests) expect(request.daemonId).toBe("daemon-gpu");
    stream.stop();
  });
});
