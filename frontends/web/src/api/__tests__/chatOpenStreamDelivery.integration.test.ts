// @vitest-environment node
import { create } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { context, type Context } from "@opentelemetry/api";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ChatStreamEventSchema, StreamingService } from "../../gen/reliant/v1/streaming_pb";
import { beginChatOpen, recordingChatOpen, resetChatOpen } from "../../lib/chatOpenInteraction";
import { setTransport } from "../../lib/connect";
import { installRecordingTracer } from "../../test/recordingTracer";
import { ChatStreamingService } from "../chat-streaming-grpc";

const CHAT = "aaaaaaaa-1111-2222-3333-444444444444";
let restoreTransport: (() => void) | undefined;
let uninstallTracer: (() => void) | undefined;

beforeEach(() => {
  // The browser telemetry provider installs a context manager. Use an inline
  // synchronous one here; Interaction.run only needs the synchronous part.
  let active = context.active();
  context.setGlobalContextManager({
    active: () => active,
    with<A extends unknown[], F extends (...args: A) => ReturnType<F>>(
      ctx: Context, fn: F, thisArg?: ThisParameterType<F>, ...args: A
    ): ReturnType<F> {
      const previous = active;
      active = ctx;
      try { return fn.apply(thisArg, args); }
      finally { active = previous; }
    },
    bind: (_ctx, target) => target,
    enable() { return this; },
    disable() { return this; },
  });
});

afterEach(() => {
  restoreTransport?.();
  restoreTransport = undefined;
  resetChatOpen();
  uninstallTracer?.();
  uninstallTracer = undefined;
  context.disable();
});

describe("chat stream delivery on the real interaction and Connect client boundary", () => {
  it("ignores opens when the tracer is off", () => {
    beginChatOpen(CHAT);
    expect(recordingChatOpen(CHAT)).toBeUndefined();
  });

  it("marks headers and the CaughtUp frame on the recording open, before the SDK applies it", async () => {
    const tracer = installRecordingTracer();
    uninstallTracer = tracer.uninstall;
    const op = beginChatOpen(CHAT);
    expect(recordingChatOpen("different chat")).toBeUndefined();
    expect(recordingChatOpen(CHAT)).toBe(op);
    const caughtUp = create(ChatStreamEventSchema, { event: { case: "caughtUp", value: { latestSequence: 7n } } });
    restoreTransport = setTransport(createRouterTransport((router) => {
      router.rpc(StreamingService.method.streamChatUpdates, async function* (_request, ctx) {
        ctx.responseHeader.set("content-encoding", "identity");
        yield caughtUp;
      });
    }));
    const service = new ChatStreamingService({ onChatUpdate() {}, onChatSnapshot() {} });
    // Exercise the same SDK method used to begin a connection without starting
    // timers/reconnects or a real Fetch request.
    const attempt = service as unknown as {
      chatId: string;
      openStream(signal: AbortSignal): AsyncIterable<typeof caughtUp>;
    };
    attempt.chatId = CHAT;
    const stream = attempt.openStream(new AbortController().signal);
    const it = stream[Symbol.asyncIterator]();
    expect((await it.next()).value).toEqual(caughtUp);
    expect(tracer.spans.find((span) => span.name === "chat.open")?.events).toEqual([
      { name: "stream_frame_read_started", attributes: { attempt: 1, frame: 1 } },
      { name: "stream_headers_received", attributes: { attempt: 1, content_encoding: "identity" } },
      { name: "stream_caught_up_frame_delivered", attributes: { attempt: 1, frame: 1, read_wait_ms: expect.any(Number) } },
    ]);
    await expect(it.next()).resolves.toMatchObject({ done: true });
  });
});
