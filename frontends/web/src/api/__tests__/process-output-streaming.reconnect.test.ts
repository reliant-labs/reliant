import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// An API replica that is shutting down ends every stream it holds, with an
// OK status, so a rollout is not held up waiting on them (serverkit's
// drain, run by the API role's APIListener). A process-output stream ended that way has NOT
// reached the end of the output: the process is still running. Only a stream
// that delivered the "complete" event is over.
//
// Before, the client treated every clean end as "process complete" and
// stopped, so each rollout froze every open log viewer until a reload.

let streams: Array<() => AsyncIterable<unknown>> = [];
const requests: unknown[] = [];

// BackgroundService is reached through connectClient; answer its stream here.
vi.mock("../../lib/connect", () => ({
  connectClient: () => ({
    streamProcessOutput: (request: unknown) => {
      requests.push(request);
      const next = streams.shift();
      if (!next) throw new Error("no stream scripted for this connection");
      return next();
    },
  }),
}));

import { ProcessOutputStreamingService } from "../process-output-streaming";

const line = { event: { case: "line", value: { type: 1, text: "building…", sequence: 1n } } };
const complete = { event: { case: "complete", value: { status: 2, exitCode: 0, endTime: "" } } };

/** A stream that sends events and then ends with an OK status. */
function endsAfter(...events: unknown[]): () => AsyncIterable<unknown> {
  return () => ({
    async *[Symbol.asyncIterator]() {
      for (const event of events) yield event;
    },
  });
}

/** A stream that sends events and then stays open. */
function staysOpenAfter(...events: unknown[]): () => AsyncIterable<unknown> {
  return () => ({
    async *[Symbol.asyncIterator]() {
      for (const event of events) yield event;
      await new Promise(() => undefined);
    },
  });
}

function makeService() {
  const callbacks = {
    onSnapshot: vi.fn(),
    onLine: vi.fn(),
    onComplete: vi.fn(),
    onError: vi.fn(),
    onStatusChange: vi.fn(),
  };
  const service = new ProcessOutputStreamingService("proc-1234-5678", callbacks);
  return { service, callbacks };
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
  it("reconnects when it ended without the complete event", async () => {
    streams = [endsAfter(line), staysOpenAfter(line)];
    const { service, callbacks } = makeService();

    service.start();
    await vi.advanceTimersByTimeAsync(5_000);

    expect(requests).toHaveLength(2);
    expect(service.isConnected()).toBe(true);
    expect(callbacks.onError).not.toHaveBeenCalled();
    service.stop();
  });

  it("stays ended after the complete event", async () => {
    streams = [endsAfter(line, complete)];
    const { service, callbacks } = makeService();

    service.start();
    await vi.advanceTimersByTimeAsync(30_000);

    expect(requests).toHaveLength(1);
    expect(callbacks.onComplete).toHaveBeenCalledTimes(1);
  });

  it("backs off and gives up on a server that ends every stream at once", async () => {
    // Answering and immediately ending is not a recovered server: the first
    // event must not reset the backoff, or this reconnects every second.
    streams = Array.from({ length: 10 }, () => endsAfter(line));
    const { service, callbacks } = makeService();

    service.start();
    await vi.advanceTimersByTimeAsync(120_000);

    expect(requests).toHaveLength(6); // the first connection + 5 attempts
    expect(callbacks.onError).toHaveBeenCalledWith("Max reconnection attempts reached");
  });
});

// A package command runs on the machine holding its workspace; its output is
// streamed from there, not from the default machine.
describe("a process on a named machine", () => {
  it("asks that machine for the output, on every reconnect too", async () => {
    streams = [endsAfter(line), staysOpenAfter(line)];
    const callbacks = {
      onSnapshot: vi.fn(),
      onLine: vi.fn(),
      onComplete: vi.fn(),
      onError: vi.fn(),
      onStatusChange: vi.fn(),
    };
    const service = new ProcessOutputStreamingService("proc-1234-5678", callbacks, "daemon-gpu");

    service.start();
    await vi.advanceTimersByTimeAsync(5_000);

    expect(requests.length).toBeGreaterThanOrEqual(2);
    for (const request of requests) {
      expect((request as { daemonId?: string }).daemonId).toBe("daemon-gpu");
    }
    service.stop();
  });
});

