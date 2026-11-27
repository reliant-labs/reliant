// @vitest-environment node
import { create } from "@bufbuild/protobuf";
import { analyticsTier } from "@reliantlabs/forge-web-runtime";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatStreamEventSchema, type ChatStreamEvent } from "../../gen/reliant/v1/streaming_pb";
import { chatStreamDeliveryProbe } from "../chatStreamDeliveryProbe";

const snapshot = create(ChatStreamEventSchema, {
  event: { case: "snapshot", value: { latestSequence: 123n } },
});
const caughtUp = create(ChatStreamEventSchema, {
  event: { case: "caughtUp", value: { latestSequence: 456n } },
});

function interaction() {
  return {
    name: "chat.open", scope: "foreground", traceId: "trace", ended: false,
    mark: vi.fn(), setAttributes: vi.fn(), end: vi.fn(), endAfterPaint: vi.fn(),
    run: <T,>(fn: () => T): T => fn(),
  };
}

function controlledStream() {
  let resolve!: (result: IteratorResult<ChatStreamEvent>) => void;
  let reject!: (error: unknown) => void;
  let read!: Promise<IteratorResult<ChatStreamEvent>>;
  const close = vi.fn(async () => ({ done: true as const, value: undefined }));
  const forwardError = vi.fn(async (error: unknown) => { throw error; });
  const next = vi.fn(() => {
    read = new Promise((res, rej) => { resolve = res; reject = rej; });
    return read;
  });
  return {
    stream: { [Symbol.asyncIterator]: () => ({ next, return: close, throw: forwardError }) },
    next,
    close,
    forwardError,
    originalRead: () => read,
    deliver: (value: ChatStreamEvent) => resolve({ value, done: false }),
    reject: (error: unknown) => reject(error),
  };
}

vi.mock("@reliantlabs/forge-web-runtime", () => ({ analyticsTier: vi.fn(() => "identified") }));

afterEach(() => {
  vi.restoreAllMocks();
  vi.mocked(analyticsTier).mockReturnValue("identified");
});

describe("chat-open Connect delivery probe", () => {
  it("does not start an attempt without an open, after abort, or beyond the retry limit", () => {
    expect(chatStreamDeliveryProbe(undefined)).toBeUndefined();
    expect(chatStreamDeliveryProbe(interaction(), AbortSignal.abort())).toBeUndefined();
    const open = interaction();
    for (let i = 1; i <= 3; i++) {
      chatStreamDeliveryProbe(open)?.onHeader(new Headers());
      expect(open.mark).toHaveBeenLastCalledWith("stream_headers_received", { attempt: i, content_encoding: "not_visible" });
    }
    expect(chatStreamDeliveryProbe(open)).toBeUndefined();
  });

  it("separates headers from decoded delivery without read-ahead, bounds marks, and passes through after catch-up", async () => {
    const open = interaction();
    const source = controlledStream();
    const probe = chatStreamDeliveryProbe(open)!;
    const it = probe.observe(source.stream)[Symbol.asyncIterator]();
    expect(source.next).not.toHaveBeenCalled();
    const now = vi.spyOn(performance, "now").mockReturnValue(100);
    const first = it.next();
    expect(source.next).toHaveBeenCalledTimes(1);
    expect(open.mark.mock.calls).toEqual([["stream_frame_read_started", { attempt: 1, frame: 1 }]]);
    now.mockReturnValue(120);
    probe.onHeader(new Headers({ "content-encoding": "GZip", "x-secret": "not logged" }));
    expect(open.mark).toHaveBeenLastCalledWith("stream_headers_received", { attempt: 1, content_encoding: "gzip" });
    now.mockReturnValue(125);
    source.deliver(snapshot);
    expect((await first).value).toBe(snapshot);
    expect(open.mark).toHaveBeenLastCalledWith("stream_frame_delivered", { attempt: 1, frame: 1, read_wait_ms: 5 });

    for (let i = 0; i < 5; i++) {
      const read = it.next();
      source.deliver(snapshot);
      await read;
    }
    const catchUp = it.next();
    source.deliver(caughtUp);
    expect((await catchUp).value).toBe(caughtUp);
    expect(open.mark.mock.calls.map(([name]) => name)).toEqual([
      "stream_frame_read_started", "stream_headers_received", "stream_frame_delivered",
      "stream_frame_read_started", "stream_frame_delivered",
      "stream_frame_read_started", "stream_frame_delivered",
      "stream_frame_read_started", "stream_frame_delivered",
      "stream_caught_up_frame_delivered",
    ]);
    expect(open.mark).toHaveBeenLastCalledWith("stream_caught_up_frame_delivered", {
      attempt: 1, frame: 7, read_wait_ms: expect.any(Number),
    });
    const live = it.next();
    expect(live).toBe(source.originalRead());
    source.deliver(snapshot);
    await live;
    expect(open.mark).toHaveBeenCalledTimes(10);
    await it.return?.();
    expect(source.close).toHaveBeenCalledOnce();
  });

  it("does not record a stale completion after abort or when the original open ends", async () => {
    for (const stop of ["abort", "supersede"]) {
      const open = interaction();
      const abort = new AbortController();
      const probe = chatStreamDeliveryProbe(open, abort.signal)!;
      const source = controlledStream();
      const it = probe.observe(source.stream)[Symbol.asyncIterator]();
      const pending = it.next();
      if (stop === "abort") abort.abort();
      else open.ended = true;
      probe.onHeader(new Headers({ "content-encoding": "gzip" }));
      source.deliver(caughtUp);
      expect((await pending).value).toBe(caughtUp);
      expect(open.mark.mock.calls).toEqual([["stream_frame_read_started", { attempt: 1, frame: 1 }]]);
      const live = it.next();
      expect(live).toBe(source.originalRead());
      source.deliver(snapshot);
      await live;
    }
  });

  it("keeps errors and iterator cancellation intact and records only a known encoding", async () => {
    const open = interaction();
    const probe = chatStreamDeliveryProbe(open)!;
    const source = controlledStream();
    const it = probe.observe(source.stream)[Symbol.asyncIterator]();
    probe.onHeader(new Headers({ "content-encoding": "arbitrary value" }));
    expect(open.mark).toHaveBeenLastCalledWith("stream_headers_received", { attempt: 1, content_encoding: "other" });
    const error = new Error("original error");
    const pending = it.next();
    source.reject(error);
    await expect(pending).rejects.toBe(error);
    await expect(it.throw?.(error)).rejects.toBe(error);
    expect(source.forwardError).toHaveBeenCalledWith(error);
    expect(open.mark).toHaveBeenCalledTimes(2);
  });

  it("stops marking an already recording open when telemetry is disabled", async () => {
    const open = interaction();
    const source = controlledStream();
    const probe = chatStreamDeliveryProbe(open)!;
    const it = probe.observe(source.stream)[Symbol.asyncIterator]();
    const pending = it.next();
    vi.mocked(analyticsTier).mockReturnValue("off");
    probe.onHeader(new Headers({ "content-encoding": "gzip" }));
    source.deliver(caughtUp);
    expect((await pending).value).toBe(caughtUp);
    expect(open.mark).toHaveBeenCalledExactlyOnceWith("stream_frame_read_started", { attempt: 1, frame: 1 });
    expect(chatStreamDeliveryProbe(interaction())).toBeUndefined();
  });
});
