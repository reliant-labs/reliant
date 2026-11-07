import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { SpanStatusCode } from "@opentelemetry/api";
import {
  armChatOpen,
  beginChatOpen,
  failChatOpen,
  markChatOpen,
  noteChatOpenSync,
  resetChatOpen,
  useChatOpenEnd,
  type ChatOpenReadiness,
} from "../chatOpenInteraction";
import { ChatStreamingService } from "../../api/chat-streaming-grpc";
import { ChatUpdateType } from "../../api/streaming-grpc";
import type { ChatStreamEvent } from "../../gen/reliant/v1/streaming_pb";
import { installRecordingTracer, type RecordedSpan } from "../../test/recordingTracer";

// The chat.open interaction against the real web runtime, recorded by a fake
// tracer: when it starts, which phases land on it, and that it ends on the
// frame after ITS chat's timeline is usable — never on another chat's.

let tracer: ReturnType<typeof installRecordingTracer>;
let frames: FrameRequestCallback[] = [];

/** Run every queued animation frame, including ones those frames queue. */
function paint() {
  while (frames.length > 0) {
    const queued = frames;
    frames = [];
    for (const cb of queued) cb(0);
  }
}

function chatOpens(): RecordedSpan[] {
  return tracer.spans.filter((s) => s.name === "chat.open");
}

function lastOpen(): RecordedSpan {
  const opens = chatOpens();
  return opens[opens.length - 1]!;
}

const idle: ChatOpenReadiness = { catchingUp: false, overlayVisible: false, syncAbandoned: false };

function renderEnd(chatId: string, initial: ChatOpenReadiness) {
  return renderHook(({ readiness }) => useChatOpenEnd(chatId, readiness), {
    initialProps: { readiness: initial },
  });
}

beforeAll(() => {
  tracer = installRecordingTracer();
});
afterAll(() => tracer.uninstall());

beforeEach(() => {
  frames = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    frames.push(cb);
    return frames.length;
  });
});

afterEach(() => {
  act(() => resetChatOpen());
  tracer.spans.length = 0;
  vi.unstubAllGlobals();
});

describe("chat.open interaction", () => {
  it("starts a chat.open span for the chat with the attributes it was given", () => {
    beginChatOpen("c1", { "chat.cached": true, "chat.cached_messages": 4 });

    expect(lastOpen().attributes).toMatchObject({
      "chat.id": "c1",
      "chat.cached": true,
      "chat.cached_messages": 4,
      "chat.sync": "none",
      "chat.overlay_shown": false,
    });
    expect(lastOpen().ended).toBe(false);
  });

  it("marks phases and the sync only on the open of the same chat", () => {
    beginChatOpen("c1");

    markChatOpen("c2", "subscribe");
    noteChatOpenSync("c2", "replay");
    markChatOpen("c1", "snapshot_received", { messages: 3 });
    noteChatOpenSync("c1", "snapshot");

    expect(lastOpen().events).toEqual([{ name: "snapshot_received", attributes: { messages: 3 } }]);
    expect(lastOpen().attributes["chat.sync"]).toBe("snapshot");
  });

  it("a second open supersedes the first", () => {
    beginChatOpen("c1");
    beginChatOpen("c2");

    const [first, second] = chatOpens();
    expect(first!.ended).toBe(true);
    expect(first!.attributes["interaction.outcome"]).toBe("superseded");
    expect(second!.ended).toBe(false);
  });

  it("fails with outcome error only for the chat it names", () => {
    beginChatOpen("c1");

    failChatOpen("c2", "rejected");
    expect(lastOpen().ended).toBe(false);

    failChatOpen("c1", "rejected");
    expect(lastOpen().ended).toBe(true);
    expect(lastOpen().attributes).toMatchObject({
      "interaction.outcome": "error",
      "chat.open.error": "rejected",
    });
    expect(lastOpen().status).toBe(SpanStatusCode.ERROR);
  });
});

describe("useChatOpenEnd", () => {
  it("waits for selectChat to arm the open, then ends after the paint", () => {
    beginChatOpen("c1");
    renderEnd("c1", idle);
    paint();
    // A render of the new chat may predate its subscription: not yet.
    expect(lastOpen().ended).toBe(false);

    act(() => armChatOpen("c1"));
    expect(lastOpen().ended).toBe(false); // waiting on the paint
    paint();

    expect(lastOpen().ended).toBe(true);
    expect(lastOpen().attributes).toMatchObject({
      "interaction.outcome": "ok",
      "interaction.paint": "painted",
      "chat.overlay_shown": false,
    });
  });

  it("ends when the overlay lifts, not when the timeline caught up under it", () => {
    beginChatOpen("c1");
    act(() => armChatOpen("c1"));
    const { rerender } = renderEnd("c1", { ...idle, catchingUp: true });
    rerender({ readiness: { ...idle, catchingUp: true, overlayVisible: true } });
    noteChatOpenSync("c1", "replay");

    // Caught up, but the overlay holds for its minimum.
    rerender({ readiness: { ...idle, overlayVisible: true } });
    paint();
    expect(lastOpen().ended).toBe(false);

    rerender({ readiness: idle });
    paint();
    expect(lastOpen().ended).toBe(true);
    expect(lastOpen().attributes).toMatchObject({
      "interaction.outcome": "ok",
      "chat.sync": "replay",
      "chat.overlay_shown": true,
    });
  });

  it("never ends an open on another chat's readiness", () => {
    beginChatOpen("c1");
    act(() => armChatOpen("c1"));

    renderEnd("c2", idle);
    paint();

    expect(lastOpen().ended).toBe(false);
  });

  it("ends with outcome error when the sync was abandoned for a dead connection", () => {
    beginChatOpen("c1");
    act(() => armChatOpen("c1"));

    renderEnd("c1", { ...idle, syncAbandoned: true });
    paint();

    expect(lastOpen().attributes).toMatchObject({
      "interaction.outcome": "error",
      "chat.open.error": "disconnected",
    });
  });
});

describe("chat stream phases", () => {
  type Internals = { handleEvent(event: ChatStreamEvent): void };

  function makeStream() {
    const service = new ChatStreamingService({
      onChatUpdate: vi.fn(),
      onChatSnapshot: vi.fn(),
      resolveChatResumeSequence: (chatId) => (chatId === "replayed" ? 500n : 0n),
    });
    vi.spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    ).mockResolvedValue(undefined);
    return { service, internals: service as unknown as Internals };
  }

  const caughtUp = (latestSequence: bigint) =>
    ({ event: { case: "caughtUp", value: { latestSequence } } }) as unknown as ChatStreamEvent;

  it("marks subscribe, snapshot_received and caught_up on a snapshot open", () => {
    const { service, internals } = makeStream();
    beginChatOpen("c1");

    service.subscribeToChat("c1");
    internals.handleEvent({
      event: {
        case: "snapshot",
        value: {
          messages: [],
          otherUpdates: [],
          latestSequence: 7n,
          total: 0,
          hasMore: false,
          oldestSeq: 0n,
          threadTokenCount: 0n,
          compactionThreshold: 0n,
        },
      },
    } as unknown as ChatStreamEvent);
    internals.handleEvent(caughtUp(7n));

    expect(lastOpen().events.map((e) => e.name)).toEqual([
      "subscribe",
      "snapshot_received",
      "caught_up",
    ]);
    expect(lastOpen().events[2]!.attributes).toMatchObject({ sync: "snapshot", initial_sync: true });
    expect(lastOpen().attributes["chat.sync"]).toBe("snapshot");
  });

  it("marks replay_started on a resume from the cached cursor", () => {
    const { service, internals } = makeStream();
    beginChatOpen("replayed");

    service.subscribeToChat("replayed");
    internals.handleEvent({
      event: {
        case: "updates",
        value: {
          updates: [
            {
              updateType: ChatUpdateType.TOOL_CALL,
              entityId: "cb-501",
              sequenceNumber: 501n,
              dataJson: JSON.stringify({ tool_call_id: "cb-501", tool_name: "view", status: "completed" }),
            },
          ],
          latestSequence: 501n,
        },
      },
    } as unknown as ChatStreamEvent);
    internals.handleEvent(caughtUp(501n));

    expect(lastOpen().events.map((e) => e.name)).toEqual([
      "subscribe",
      "replay_started",
      "caught_up",
    ]);
    expect(lastOpen().events[1]!.attributes).toMatchObject({ since_sequence: 500, updates: 1 });
    expect(lastOpen().attributes["chat.sync"]).toBe("replay");
  });
});
