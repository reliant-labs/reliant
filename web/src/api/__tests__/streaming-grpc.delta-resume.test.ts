import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChatUpdateType, UserStreamingService } from "../streaming-grpc";
import type { UserStreamEvent } from "../streaming-grpc";
import type { ChatUpdate, ContextUsageInfo } from "../../types/streaming";

// Reopening a chat used to cost a full ChatSyncSnapshot every time — measured
// at 2.6s / 2.3MB for a 323-message chat — because subscribeToChatDetails
// zeroed lastChatSequence on every switch. The server already replays
// (chat_since_seq, latest] for any cursor > 0, so a chat whose state the
// client still holds only needs the updates it missed.
//
// These tests pin the client half of that contract:
//   - leaving a chat hands its cursor to the owner of the chat's state;
//   - reopening asks the owner, and subscribes with its answer (0 = snapshot);
//   - whichever reply the server picks — replay or snapshot — is applied, and
//     gap detection runs against the restored cursor;
//   - the "syncing" signal opens with a subscription and closes only on the
//     server's explicit chat_caught_up, never on a heartbeat or a batch;
//   - a replay keeps the context-usage indicator current from the message
//     updates it carries, since no snapshot arrives to refresh it.
//
// The network is stubbed at establishConnection(), so `lastChatSequence` at
// the moment of the reconnect IS the chat_since_seq the request would carry.

type ServiceInternals = {
  handleEvent(event: UserStreamEvent): void;
  lastChatSequence: bigint;
  subscribedChatId: string | undefined;
  isConnected_: boolean;
  connectAttemptInFlight: boolean;
};

function makeService(resume: Record<string, bigint> = {}) {
  const onChatUpdate = vi.fn<(updates: ChatUpdate[]) => void>();
  const onChatSnapshot = vi.fn<(updates: ChatUpdate[]) => void>();
  const onChatCursorRelease = vi.fn<(chatId: string, seq: bigint) => void>();
  const onChatSyncPending = vi.fn<(chatId: string | null) => void>();
  const onChatContextUsage = vi.fn<(usage: ContextUsageInfo) => void>();
  const resolveChatResumeSequence = vi.fn(
    (chatId: string) => resume[chatId] ?? 0n,
  );
  const service = new UserStreamingService({
    onUpdate: vi.fn(),
    onStatusChange: vi.fn(),
    onSync: vi.fn(),
    onError: vi.fn(),
    onChatUpdate,
    onChatSnapshot,
    onChatCursorRelease,
    onChatSyncPending,
    onChatContextUsage,
    resolveChatResumeSequence,
  });
  const internals = service as unknown as ServiceInternals;
  // Record chat_since_seq exactly as establishConnection would send it.
  const requestedChatSinceSeq: bigint[] = [];
  const connectSpy = vi
    .spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    )
    .mockImplementation(async () => {
      requestedChatSinceSeq.push(internals.lastChatSequence);
    });
  return {
    service,
    internals,
    connectSpy,
    requestedChatSinceSeq,
    onChatUpdate,
    onChatSnapshot,
    onChatCursorRelease,
    onChatSyncPending,
    onChatContextUsage,
    resolveChatResumeSequence,
  };
}

/** Steady state: connected and synced to `chatId` at `seq`. */
function viewing(internals: ServiceInternals, chatId: string, seq: bigint) {
  internals.subscribedChatId = chatId;
  internals.lastChatSequence = seq;
  internals.isConnected_ = true;
  internals.connectAttemptInFlight = false;
}

function toolCall(seq: number) {
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

function chatUpdates(seqs: number[], latestSequence: bigint): UserStreamEvent {
  return {
    event: {
      case: "chatUpdates",
      value: { updates: seqs.map(toolCall), latestSequence },
    },
  } as unknown as UserStreamEvent;
}

function snapshot(latestSequence: bigint): UserStreamEvent {
  return {
    event: {
      case: "chatSyncSnapshot",
      value: {
        messages: [],
        otherUpdates: [toolCall(Number(latestSequence))],
        latestSequence,
        total: 0,
        hasMore: false,
        oldestSeq: 0n,
        threadTokenCount: 0n,
        compactionThreshold: 0n,
      },
    },
  } as unknown as UserStreamEvent;
}

const heartbeat = {
  event: { case: "heartbeat", value: { timestamp: 0n } },
} as unknown as UserStreamEvent;

function caughtUp(latestSequence: bigint): UserStreamEvent {
  return {
    event: { case: "chatCaughtUp", value: { latestSequence } },
  } as unknown as UserStreamEvent;
}

/**
 * A persisted message update as the server sends it: the save_message.go
 * payload, wrapped in {message: ...} by formatChatUpdateDataJSON.
 */
function messageUpdate(
  seq: number,
  thread: string,
  usage?: { tokens: number; threshold: number },
) {
  return {
    updateType: ChatUpdateType.MESSAGE,
    entityId: `m-${seq}`,
    sequenceNumber: BigInt(seq),
    dataJson: JSON.stringify({
      message: {
        id: `m-${seq}`,
        role: 2,
        seq,
        thread,
        content_blocks: [],
        ...(usage
          ? { thread_token_count: usage.tokens, compaction_threshold: usage.threshold }
          : {}),
      },
    }),
  };
}

function messageBatch(
  updates: ReturnType<typeof messageUpdate>[],
  latestSequence: bigint,
): UserStreamEvent {
  return {
    event: { case: "chatUpdates", value: { updates, latestSequence } },
  } as unknown as UserStreamEvent;
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("delta resume on chat reopen", () => {
  it("subscribes with the cached cursor when the chat's state is still held", () => {
    const { service, internals, requestedChatSinceSeq } = makeService({
      c1: 500n,
    });
    viewing(internals, "c2", 90n);

    service.subscribeToChatDetails("c1");

    expect(requestedChatSinceSeq).toEqual([500n]);
    expect(internals.subscribedChatId).toBe("c1");
  });

  it("subscribes with 0 (snapshot) when the owner holds no state for the chat", () => {
    // An evicted / never-opened / reset chat: resolve answers 0n.
    const { service, internals, requestedChatSinceSeq } = makeService({});
    viewing(internals, "c2", 90n);

    service.subscribeToChatDetails("c1");

    expect(requestedChatSinceSeq).toEqual([0n]);
  });

  it("releases the left chat's cursor before resolving the entered one", () => {
    const { service, internals, onChatCursorRelease, resolveChatResumeSequence } =
      makeService({ c1: 500n });
    viewing(internals, "c2", 90n);

    service.subscribeToChatDetails("c1");

    expect(onChatCursorRelease).toHaveBeenCalledWith("c2", 90n);
    expect(resolveChatResumeSequence).toHaveBeenCalledWith("c1");
    expect(onChatCursorRelease.mock.invocationCallOrder[0]).toBeLessThan(
      resolveChatResumeSequence.mock.invocationCallOrder[0],
    );
  });

  it("releases the cursor on unsubscribe and on stop", () => {
    const a = makeService();
    viewing(a.internals, "c1", 77n);
    a.service.unsubscribeFromChatDetails();
    expect(a.onChatCursorRelease).toHaveBeenCalledWith("c1", 77n);

    const b = makeService();
    viewing(b.internals, "c1", 88n);
    b.service.stop();
    expect(b.onChatCursorRelease).toHaveBeenCalledWith("c1", 88n);
  });

  it("start() resumes the initial subscription from the cached cursor", () => {
    const { service, requestedChatSinceSeq } = makeService({ c1: 321n });

    service.start(10, "c1", 0);

    expect(requestedChatSinceSeq).toEqual([321n]);
  });

  it("applies a replay reply incrementally and advances the cursor", () => {
    const { service, internals, onChatUpdate, onChatSnapshot } = makeService({
      c1: 500n,
    });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(chatUpdates([501, 502], 502n));

    expect(onChatSnapshot).not.toHaveBeenCalled();
    expect(onChatUpdate).toHaveBeenCalledTimes(1);
    expect(internals.lastChatSequence).toBe(502n);
  });

  it("applies a snapshot reply to a cursor resume with replace semantics", () => {
    // The server answers a stale or foreign cursor with a snapshot instead of
    // a replay (gap too large, or cursor ahead of the chat).
    const { service, internals, onChatUpdate, onChatSnapshot } = makeService({
      c1: 500n,
    });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(snapshot(9000n));

    expect(onChatSnapshot).toHaveBeenCalledTimes(1);
    expect(onChatUpdate).not.toHaveBeenCalled();
    expect(internals.lastChatSequence).toBe(9000n);
  });

  it("adopts a snapshot's sequence even when it is BEHIND the restored cursor", () => {
    // Cursor ahead of the server (another database, a rebuilt update log):
    // the snapshot is the truth, and the cursor must follow it backwards or
    // every later live update would look like a duplicate.
    const { service, internals } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(snapshot(40n));

    expect(internals.lastChatSequence).toBe(40n);
  });

  it("detects a gap against the restored cursor and resyncs from it", () => {
    const { service, internals, onChatUpdate, connectSpy, requestedChatSinceSeq } =
      makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");
    connectSpy.mockClear();
    requestedChatSinceSeq.length = 0;

    // 501 never arrived.
    internals.handleEvent(chatUpdates([502], 502n));

    expect(onChatUpdate).not.toHaveBeenCalled();
    expect(internals.lastChatSequence).toBe(500n);
    expect(requestedChatSinceSeq).toEqual([500n]);
  });
});

describe("chat sync pending signal", () => {
  it("opens when a chat subscription begins", () => {
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);

    service.subscribeToChatDetails("c1");

    expect(onChatSyncPending).toHaveBeenLastCalledWith("c1");
  });

  it("closes on chat_caught_up after a snapshot, not on the snapshot itself", () => {
    const { service, internals, onChatSyncPending } = makeService({});
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(snapshot(10n));
    expect(onChatSyncPending).toHaveBeenLastCalledWith("c1");

    internals.handleEvent(caughtUp(10n));
    expect(onChatSyncPending).toHaveBeenLastCalledWith(null);
  });

  it("stays open across every replay batch until chat_caught_up", () => {
    // Each replay batch's latest_sequence is that batch's own max, so a
    // batch "reaching" it says nothing about whether more batches follow.
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");
    onChatSyncPending.mockClear();

    internals.handleEvent(chatUpdates([501, 502], 502n));
    internals.handleEvent(chatUpdates([503, 504], 504n));
    expect(onChatSyncPending).not.toHaveBeenCalled();

    internals.handleEvent(caughtUp(504n));
    expect(onChatSyncPending).toHaveBeenLastCalledWith(null);
  });

  it("closes on chat_caught_up when the replay had nothing to send", () => {
    // Nothing changed while away: chat_caught_up is the only chat frame.
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(caughtUp(500n));

    expect(onChatSyncPending).toHaveBeenLastCalledWith(null);
  });

  it("stays open while a batch leaves the cursor short of latest_sequence", () => {
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");
    onChatSyncPending.mockClear();

    // A live batch whose latest_sequence names a server state beyond what we
    // hold, without a gap in what arrived (e.g. an ephemeral delta).
    internals.handleEvent({
      event: {
        case: "chatUpdates",
        value: {
          updates: [
            {
              updateType: ChatUpdateType.STREAMING_DELTA,
              entityId: "",
              sequenceNumber: 0n,
              dataJson: JSON.stringify({ delta_type: "content_block_delta" }),
            },
          ],
          latestSequence: 640n,
        },
      },
    } as unknown as UserStreamEvent);

    expect(onChatSyncPending).not.toHaveBeenCalled();
  });

  it("stays open across a gap — the resync's replay settles it", () => {
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");
    onChatSyncPending.mockClear();

    internals.handleEvent(chatUpdates([505], 505n));

    expect(onChatSyncPending).not.toHaveBeenCalled();
  });

  it("is not closed by a heartbeat", () => {
    // Heartbeats prove the socket is alive, nothing about the chat sync.
    // Settling on one left the indicator up for up to 30s after every reopen.
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");
    onChatSyncPending.mockClear();

    internals.handleEvent(heartbeat);

    expect(onChatSyncPending).not.toHaveBeenCalled();
  });

  it("is not re-opened by a gap resync of an already-synced chat", () => {
    const { internals, onChatSyncPending } = makeService();
    viewing(internals, "c1", 500n);

    internals.handleEvent(chatUpdates([503], 503n));

    expect(onChatSyncPending).not.toHaveBeenCalled();
  });

  it("closes when the chat is unsubscribed", () => {
    const { service, internals, onChatSyncPending } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    service.unsubscribeFromChatDetails();

    expect(onChatSyncPending).toHaveBeenLastCalledWith(null);
  });
});

describe("context usage from message updates", () => {
  // The compaction indicator used to be set only from a snapshot's
  // thread_token_count. A replay resume sends no snapshot, so the indicator
  // kept showing the value from when the chat was last open — even though
  // every persisted message update already carries the current usage.

  it("updates the main thread's usage from a replayed message", () => {
    const { service, internals, onChatContextUsage } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(
      messageBatch([messageUpdate(501, "c1", { tokens: 64_000, threshold: 850_000 })], 501n),
    );

    expect(onChatContextUsage).toHaveBeenCalledWith({
      threadTokenCount: 64_000,
      compactionThreshold: 850_000,
    });
  });

  it("applies only the latest main-thread usage in a batch", () => {
    const { service, internals, onChatContextUsage } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(
      messageBatch(
        [
          messageUpdate(501, "c1", { tokens: 10_000, threshold: 850_000 }),
          messageUpdate(502, "c1", { tokens: 20_000, threshold: 850_000 }),
          // A spawned thread's context is its own, not the chat's.
          messageUpdate(503, "spawn-1", { tokens: 99_000, threshold: 185_000 }),
        ],
        503n,
      ),
    );

    expect(onChatContextUsage).toHaveBeenCalledTimes(1);
    expect(onChatContextUsage).toHaveBeenCalledWith({
      threadTokenCount: 20_000,
      compactionThreshold: 850_000,
    });
  });

  it("ignores spawned-thread messages and messages that carry no usage", () => {
    const { service, internals, onChatContextUsage } = makeService({ c1: 500n });
    viewing(internals, "c2", 90n);
    service.subscribeToChatDetails("c1");

    internals.handleEvent(
      messageBatch(
        [
          messageUpdate(501, "spawn-1", { tokens: 99_000, threshold: 185_000 }),
          messageUpdate(502, "c1"),
        ],
        502n,
      ),
    );

    expect(onChatContextUsage).not.toHaveBeenCalled();
  });
});
