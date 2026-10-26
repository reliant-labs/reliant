import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserStreamingService } from "../streaming-grpc";
import type { UserStreamEvent } from "../streaming-grpc";
import type { UserUpdate } from "../../types/streaming";

// User sequences are per-user, but the stream is
// project-filtered, so the sequences the client receives skip BY DESIGN. Only
// the server sees every sequence, so the server owns user gap detection (it
// backfills hub drops from the DB) and reports its cursor as each batch's
// latest_sequence. The client adopts it and never resyncs on a skip.
//
// These tests drive the private handleEvent() directly and stub the network
// (establishConnection) to observe that nothing reconnects. Chat sequence
// gaps are the chat stream's (chat-streaming-grpc.gap-resync.test.ts).

type ServiceInternals = {
  handleEvent(event: UserStreamEvent): void;
  lastSequence: bigint;
  isConnected_: boolean;
  establishConnection(): Promise<void>;
};

function makeService() {
  const onUpdate = vi.fn<(updates: UserUpdate[]) => void>();
  const onSync = vi.fn<(seq: number) => void>();
  const service = new UserStreamingService({
    onUpdate,
    onStatusChange: vi.fn(),
    onSync,
    onError: vi.fn(),
  });
  const internals = service as unknown as ServiceInternals;
  const reconnectSpy = vi
    .spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    )
    .mockResolvedValue(undefined);
  return { service, internals, onUpdate, onSync, reconnectSpy };
}

function userUpdatesEvent(seqs: number[], latestSequence: bigint): UserStreamEvent {
  return {
    event: {
      case: "updates",
      value: {
        latestSequence,
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

describe("user stream cursor", () => {
  it("applies contiguous user updates and advances the cursor", () => {
    const { internals, onUpdate, reconnectSpy } = makeService();
    internals.lastSequence = 5n;

    internals.handleEvent(userUpdatesEvent([6], 6n));

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(internals.lastSequence).toBe(6n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("advances across a filtered jump from latest_sequence, with no reconnect", () => {
    // 685033 belonged to another project. The server filtered it, and its
    // cursor says so. Every chat open used to trip a jump-resync here: two
    // extra reconnects, each re-subscribing the chat.
    const { internals, onUpdate, reconnectSpy } = makeService();
    internals.lastSequence = 685032n;

    internals.handleEvent(userUpdatesEvent([685034], 685034n));

    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(internals.lastSequence).toBe(685034n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("advances on a cursor-only batch whose updates were all filtered out", () => {
    const { internals, onUpdate, reconnectSpy } = makeService();
    internals.lastSequence = 10n;

    internals.handleEvent(userUpdatesEvent([], 25n));

    expect(onUpdate).not.toHaveBeenCalled();
    expect(internals.lastSequence).toBe(25n);
    expect(reconnectSpy).not.toHaveBeenCalled();
  });

  it("never moves the cursor backwards", () => {
    const { internals } = makeService();
    internals.lastSequence = 30n;

    internals.handleEvent(userUpdatesEvent([], 25n));

    expect(internals.lastSequence).toBe(30n);
  });

  // A daemon heartbeat is ephemeral: never persisted, so sequence 0. The
  // server sends it with the cursor as it already stood; the client must apply
  // it (daemon dot, memory pressure, detected_ports) without touching the
  // resume cursor, which only ever names persisted updates.
  it("applies an ephemeral (seq 0) daemon heartbeat without moving the cursor", () => {
    const { internals, onUpdate, reconnectSpy } = makeService();
    internals.lastSequence = 30n;

    const heartbeat = {
      event: {
        case: "updates",
        value: {
          latestSequence: 30n,
          updates: [
            {
              id: "",
              userId: "user-1",
              sequenceNumber: 0n,
              updateType: 20, // USER_UPDATE_TYPE_DAEMON_HEARTBEAT
              entityType: 5, // ENTITY_TYPE_SYSTEM
              entityId: "daemon-1",
              dataJson: JSON.stringify({ daemon_id: "daemon-1", last_heartbeat: 1700000000, detected_ports: [5173] }),
              createdAt: "",
            },
          ],
        },
      },
    } as unknown as UserStreamEvent;
    internals.handleEvent(heartbeat);

    expect(onUpdate).toHaveBeenCalledTimes(1);
    const [delivered] = onUpdate.mock.calls[0][0];
    expect(delivered.sequence_number).toBe(0);
    expect(delivered.entity_id).toBe("daemon-1");
    expect(delivered.data).toMatchObject({ detected_ports: [5173] });
    expect(internals.lastSequence).toBe(30n);
    expect(reconnectSpy).not.toHaveBeenCalled();
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
