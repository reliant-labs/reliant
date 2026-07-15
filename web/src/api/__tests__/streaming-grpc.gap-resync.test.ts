import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatUpdateType, UserStreamingService } from "../streaming-grpc";
import type { UserStreamEvent } from "../streaming-grpc";
import type { ChatUpdate, UserUpdate } from "../../types/streaming";

// The backend assigns chat_updates sequence numbers per chat, contiguously
// (MAX+1 inside a transaction — internal/db/repo.go CreateChatUpdate), and
// supports replay from any sequence via chat_since_seq / since_seq
// (internal/grpc/services/streaming.go). The NATS hub can drop events under
// load ("slow consumer"), so the client MUST detect sequence gaps and resync
// by reconnecting from its last contiguous cursor — otherwise a dropped
// finalization/tool-call event leaves the chat stale until a page refresh.
//
// These tests drive the private handleEvent() directly and stub the network
// (establishConnection) to observe resync behavior.

type ServiceInternals = {
  handleEvent(event: UserStreamEvent): void;
  lastSequence: bigint;
  lastChatSequence: bigint;
  subscribedChatId: string | undefined;
  isConnected_: boolean;
  establishConnection(): Promise<void>;
};

function makeService() {
  const onUpdate = vi.fn<(updates: UserUpdate[]) => void>();
  const onChatUpdate = vi.fn<(updates: ChatUpdate[]) => void>();
  const onSync = vi.fn<(seq: number) => void>();
  const service = new UserStreamingService({
    onUpdate,
    onStatusChange: vi.fn(),
    onSync,
    onError: vi.fn(),
    onChatUpdate,
  });
  const internals = service as unknown as ServiceInternals;
  const reconnectSpy = vi
    .spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    )
    .mockResolvedValue(undefined);
  return { service, internals, onUpdate, onChatUpdate, onSync, reconnectSpy };
}

function chatToolCallUpdate(seq: number) {
  return {
    updateType: ChatUpdateType.TOOL_CALL,
    entityId: `cb-${seq}`,
    sequenceNumber: BigInt(seq),
    dataJson: JSON.stringify({
      content_block_id: `cb-${seq}`,
      tool_name: "view",
      status: "completed",
    }),
  };
}

function chatUpdatesEvent(
  seqs: number[],
  latestSequence: bigint,
): UserStreamEvent {
  return {
    event: {
      case: "chatUpdates",
      value: {
        updates: seqs.map(chatToolCallUpdate),
        latestSequence,
      },
    },
  } as unknown as UserStreamEvent;
}

function chatDeltaEvent(latestSequence: bigint): UserStreamEvent {
  return {
    event: {
      case: "chatUpdates",
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
  } as unknown as UserStreamEvent;
}

function userUpdatesEvent(seqs: number[]): UserStreamEvent {
  return {
    event: {
      case: "updates",
      value: {
        updates: seqs.map((seq) => ({
          id: `uu-${seq}`,
          userId: "user-1",
          sequenceNumber: BigInt(seq),
          projectId: "p1",
          worktreeId: "",
          chatId: "c1",
          updateType: 1,
          entityType: 1,
          entityId: "c1",
          dataJson: "{}",
          createdAt: "2026-01-01T00:00:00.000Z",
        })),
      },
    },
  } as unknown as UserStreamEvent;
}

function syncEvent(lastSequence: bigint): UserStreamEvent {
  return {
    event: {
      case: "sync",
      value: { lastSequence },
    },
  } as unknown as UserStreamEvent;
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("chat detail stream gap detection", () => {
  it("applies contiguous chat updates and advances the chat cursor", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 5n;

    internals.handleEvent(chatUpdatesEvent([6, 7], 7n));

    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(onChatUpdate.mock.calls[0][0]).toHaveLength(2);
    expect(internals.lastChatSequence).toBe(7n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("drops post-gap chat updates and resyncs from the last contiguous sequence", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 5n;

    // seq 6 and 7 were dropped upstream (hub slow consumer) — seq 8 arrives.
    internals.handleEvent(chatUpdatesEvent([8], 8n));

    // The out-of-order update must NOT be applied; the reconnect replays
    // 6..8 from the DB in order instead.
    const applied = onChatUpdate.mock.calls.flatMap((c) => c[0]);
    expect(applied).toHaveLength(0);
    expect(internals.lastChatSequence).toBe(5n);
    expect(reconnectSpy).toHaveBeenCalledTimes(1);
  });

  it("processes the contiguous prefix before resyncing on a mid-batch gap", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 5n;

    internals.handleEvent(chatUpdatesEvent([6, 9], 9n));

    const applied = onChatUpdate.mock.calls.flatMap((c) => c[0]);
    expect(applied).toHaveLength(1);
    expect(
      (applied[0] as unknown as { content_block_id: string }).content_block_id,
    ).toBe("cb-6");
    expect(internals.lastChatSequence).toBe(6n);
    expect(reconnectSpy).toHaveBeenCalledTimes(1);
  });

  it("ephemeral deltas (seq 0) pass through without gap checks or cursor movement", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 5n;

    // Delta batches echo the server's latestSequence; it must not be able to
    // advance the client cursor past updates the client never received.
    internals.handleEvent(chatDeltaEvent(7n));

    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(onChatUpdate.mock.calls[0][0]).toHaveLength(1);
    expect(internals.lastChatSequence).toBe(5n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("does not treat the first update after a fresh subscribe as a gap", () => {
    const { internals, onChatUpdate, reconnectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 0n; // fresh subscribe, snapshot not yet applied

    internals.handleEvent(chatUpdatesEvent([42], 42n));

    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(internals.lastChatSequence).toBe(42n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });
});

describe("user stream gap handling", () => {
  it("applies contiguous user updates and advances the cursor", () => {
    const { internals, onUpdate, reconnectSpy } = makeService();
    internals.lastSequence = 5n;

    internals.handleEvent(userUpdatesEvent([6]));

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(internals.lastSequence).toBe(6n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("still applies a jumped batch but rewinds and resyncs to fill the gap", () => {
    // User-update sequences are per-user; the server filters live events by
    // project, so a jump is only *possibly* a drop. The batch is applied
    // (it may be all we ever get for this project), and a replay-resync from
    // the pre-jump cursor fills anything that was really dropped.
    const { internals, onUpdate, reconnectSpy } = makeService();
    internals.lastSequence = 5n;

    internals.handleEvent(userUpdatesEvent([9]));

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(onUpdate.mock.calls[0][0]).toHaveLength(1);
    expect(reconnectSpy).toHaveBeenCalledTimes(1);
    // Cursor rewound so the reconnect replays 6..9 from the DB.
    expect(internals.lastSequence).toBe(5n);
  });

  it("throttles user gap resyncs", () => {
    const { internals, reconnectSpy } = makeService();
    internals.lastSequence = 5n;

    internals.handleEvent(userUpdatesEvent([9]));
    // Replayed catch-up arrives, then another jump right away.
    internals.handleEvent(userUpdatesEvent([6, 7, 8, 9]));
    internals.handleEvent(userUpdatesEvent([12]));

    expect(reconnectSpy).toHaveBeenCalledTimes(1);
  });
});

describe("sync event", () => {
  it("does not advance the resume cursor past unprocessed catch-up updates", () => {
    // The server sends sync(latest) BEFORE replaying (since_seq, latest].
    // If sync moved the cursor, a disconnect mid-replay would skip the
    // remaining updates forever on reconnect.
    const { internals, onSync } = makeService();
    internals.lastSequence = 5n;

    internals.handleEvent(syncEvent(100n));

    expect(onSync).toHaveBeenCalledWith(100);
    expect(internals.lastSequence).toBe(5n);
  });
});
