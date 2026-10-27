import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatUpdateType } from "../streaming-grpc";
import { ChatStreamingService } from "../chat-streaming-grpc";
import type { ChatStreamEvent } from "../../gen/reliant/v1/streaming_pb";
import type { ChatUpdate } from "../../types/streaming";

// The backend assigns chat_updates sequence numbers per chat, contiguously
// (MAX+1 inside a transaction — internal/db/repo.go CreateChatUpdate), and
// StreamChatUpdates replays from any cursor (since_seq). The NATS hub can drop
// events under load ("slow consumer"), so the client MUST detect chat sequence
// gaps and resync by reconnecting the chat stream from its last contiguous
// cursor — otherwise a dropped finalization/tool-call event leaves the chat
// stale until a page refresh.
//
// These tests drive the private handleEvent() directly and stub the network
// (establishConnection) to observe resync behavior.

type ServiceInternals = {
  handleEvent(event: ChatStreamEvent): void;
  lastChatSequence: bigint;
  chatId: string | undefined;
  isConnected_: boolean;
  isIntentionallyClosed: boolean;
  establishConnection(): Promise<void>;
};

function makeService() {
  const onChatUpdate = vi.fn<(chatId: string, updates: ChatUpdate[]) => void>();
  const service = new ChatStreamingService({
    onChatUpdate,
    onChatSnapshot: vi.fn(),
  });
  const internals = service as unknown as ServiceInternals;
  // Steady state: the stream is open for a chat.
  internals.isIntentionallyClosed = false;
  const reconnectSpy = vi
    .spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    )
    .mockResolvedValue(undefined);
  return { service, internals, onChatUpdate, reconnectSpy };
}

function chatToolCallUpdate(seq: number) {
  return {
    updateType: ChatUpdateType.TOOL_CALL,
    entityId: `cb-${seq}`,
    sequenceNumber: BigInt(seq),
    dataJson: JSON.stringify({
      tool_call_id: `cb-${seq}`,
      tool_name: "view",
      status: "completed",
    }),
  };
}

function chatUpdatesEvent(
  seqs: number[],
  latestSequence: bigint,
): ChatStreamEvent {
  return {
    event: {
      case: "updates",
      value: {
        updates: seqs.map(chatToolCallUpdate),
        latestSequence,
      },
    },
  } as unknown as ChatStreamEvent;
}

function chatDeltaEvent(latestSequence: bigint): ChatStreamEvent {
  return {
    event: {
      case: "updates",
      value: {
        updates: [
          {
            updateType: ChatUpdateType.STREAMING_DELTA,
            entityId: "",
            sequenceNumber: 0n, // ephemeral — never persisted
            dataJson: JSON.stringify({
              delta_type: "content_block_delta",
              block_index: 0,
              delta: "chunk",
            }),
          },
        ],
        latestSequence,
      },
    },
  } as unknown as ChatStreamEvent;
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("chat detail stream gap detection", () => {
  it("applies contiguous chat updates and advances the chat cursor", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.chatId = "c1";
    internals.lastChatSequence = 5n;

    internals.handleEvent(chatUpdatesEvent([6, 7], 7n));

    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(onChatUpdate.mock.calls[0][1]).toHaveLength(2);
    expect(internals.lastChatSequence).toBe(7n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("drops post-gap chat updates and resyncs from the last contiguous sequence", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.chatId = "c1";
    internals.lastChatSequence = 5n;

    // seq 6 and 7 were dropped upstream (hub slow consumer) — seq 8 arrives.
    internals.handleEvent(chatUpdatesEvent([8], 8n));

    // The out-of-order update must NOT be applied; the reconnect replays
    // 6..8 from the DB in order instead.
    const applied = onChatUpdate.mock.calls.flatMap((c) => c[1]);
    expect(applied).toHaveLength(0);
    expect(internals.lastChatSequence).toBe(5n);
    expect(reconnectSpy).toHaveBeenCalledTimes(1);
  });

  it("processes the contiguous prefix before resyncing on a mid-batch gap", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.chatId = "c1";
    internals.lastChatSequence = 5n;

    internals.handleEvent(chatUpdatesEvent([6, 9], 9n));

    const applied = onChatUpdate.mock.calls.flatMap((c) => c[1]);
    expect(applied).toHaveLength(1);
    expect(
      (applied[0] as unknown as { tool_call_id: string }).tool_call_id,
    ).toBe("cb-6");
    expect(internals.lastChatSequence).toBe(6n);
    expect(reconnectSpy).toHaveBeenCalledTimes(1);
  });

  it("ephemeral deltas (seq 0) pass through without gap checks or cursor movement", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.chatId = "c1";
    internals.lastChatSequence = 5n;

    // Delta batches echo the server's latestSequence; it must not be able to
    // advance the client cursor past updates the client never received.
    internals.handleEvent(chatDeltaEvent(7n));

    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(onChatUpdate.mock.calls[0][1]).toHaveLength(1);
    expect(internals.lastChatSequence).toBe(5n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("does not treat the first update after a fresh subscribe as a gap", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.chatId = "c1";
    internals.lastChatSequence = 0n; // fresh subscribe, snapshot not yet applied

    internals.handleEvent(chatUpdatesEvent([42], 42n));

    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(internals.lastChatSequence).toBe(42n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("hands chat updates to the callback under the stream's own chat", () => {
    const { internals, onChatUpdate } = makeService();
    internals.chatId = "c1";
    internals.lastChatSequence = 5n;

    internals.handleEvent(chatUpdatesEvent([6], 6n));

    expect(onChatUpdate.mock.calls[0][0]).toBe("c1");
  });
});
