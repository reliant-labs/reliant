import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UserStreamingService } from "../streaming-grpc";
import type { ConnectionStatus } from "../../types/streaming";

// This stream is the app's ONLY push path. Every path that leaves it neither
// connected nor scheduled to reconnect strands the entire UI on stale state
// until a full page reload — the observed failure mode was a chat that had
// completed server-side (workflow status "completed" persisted in
// chat_updates) while the UI still showed it mid-run.
//
// Three recovery gaps are covered here:
//   1. reconnect budget exhaustion (was: 10 attempts ≈ 3min, then dead forever)
//   2. half-open connections that never raise a stream error, so `for await`
//      blocks forever and no reconnect is ever attempted
//   3. start() refusing to restart a service holding a stale AbortController
//
// The network is stubbed at establishConnection() so timers can be driven
// deterministically.

type ServiceInternals = {
  establishConnection(): Promise<void>;
  attemptReconnect(): void;
  armWatchdog(): void;
  isConnected_: boolean;
  connectAttemptInFlight: boolean;
  lastEventAt: number;
  reconnectAttempts: number;
  lastChatSequence: bigint;
  lastSequence: bigint;
  abortController: AbortController | null;
  subscribedChatId: string | undefined;
  connectionOpenedAt: number;
  reconnectNow(reason: string): void;
};

function makeService() {
  const onStatusChange = vi.fn<(s: ConnectionStatus) => void>();
  const onError = vi.fn<(e: string) => void>();
  const service = new UserStreamingService({
    onUpdate: vi.fn(),
    onStatusChange,
    onSync: vi.fn(),
    onError,
    onChatUpdate: vi.fn(),
  });
  const internals = service as unknown as ServiceInternals;
  const connectSpy = vi
    .spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    )
    .mockResolvedValue(undefined);
  return { service, internals, connectSpy, onStatusChange, onError };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("reconnect persistence", () => {
  it("keeps retrying well past the old 10-attempt ceiling", () => {
    const { internals, connectSpy, onError } = makeService();

    // Simulate a long backend outage: every scheduled attempt fails, which in
    // the real client re-enters attemptReconnect() from the catch block.
    for (let i = 0; i < 50; i++) {
      internals.attemptReconnect();
      vi.advanceTimersByTime(60_000);
    }

    expect(connectSpy).toHaveBeenCalledTimes(50);
    // The old code called onError("Max reconnection attempts reached") and
    // then stopped forever. Nothing should give up now.
    expect(onError).not.toHaveBeenCalled();
  });

  it("caps backoff at 30s rather than growing unboundedly", () => {
    const { internals, connectSpy } = makeService();

    for (let i = 0; i < 20; i++) {
      internals.attemptReconnect();
      vi.advanceTimersByTime(60_000);
    }
    connectSpy.mockClear();

    // A 21st attempt must still fire within the 30s ceiling (+1s jitter).
    internals.attemptReconnect();
    vi.advanceTimersByTime(31_000);
    expect(connectSpy).toHaveBeenCalledTimes(1);
  });

  it("does not stack a second timer when one is already pending", () => {
    const { internals, connectSpy } = makeService();

    internals.attemptReconnect();
    internals.attemptReconnect();
    internals.attemptReconnect();
    vi.advanceTimersByTime(60_000);

    expect(connectSpy).toHaveBeenCalledTimes(1);
  });
});

describe("half-open connection watchdog", () => {
  it("forces a reconnect when no event arrives for 2.5 heartbeat intervals", () => {
    const { internals, connectSpy, onStatusChange } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 42n;
    internals.isConnected_ = true;
    internals.armWatchdog();
    connectSpy.mockClear();

    // Server heartbeat is 30s; nothing at all arrives for 80s.
    vi.advanceTimersByTime(80_000);

    expect(connectSpy).toHaveBeenCalledTimes(1);
    expect(onStatusChange).toHaveBeenCalledWith("disconnected");
    // Cursors must survive so the server replays the missed range from the DB.
    expect(internals.lastChatSequence).toBe(42n);
    expect(internals.subscribedChatId).toBe("c1");
  });

  it("stays quiet while heartbeats keep arriving", () => {
    const { internals, connectSpy } = makeService();
    internals.isConnected_ = true;
    internals.armWatchdog();
    connectSpy.mockClear();

    // A heartbeat every 30s keeps the connection alive indefinitely.
    for (let i = 0; i < 10; i++) {
      vi.advanceTimersByTime(30_000);
      internals.lastEventAt = Date.now();
    }

    expect(connectSpy).not.toHaveBeenCalled();
  });

  it("stops firing once the stream is stopped", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;
    internals.armWatchdog();

    service.stop();
    connectSpy.mockClear();
    vi.advanceTimersByTime(300_000);

    expect(connectSpy).not.toHaveBeenCalled();
  });
});

describe("start() after a dead connection", () => {
  it("restarts a service left holding a stale, non-aborted controller", () => {
    const { service, internals, connectSpy } = makeService();

    // Shape left behind by a failed attempt: the controller was never aborted
    // (the stream errored rather than being cancelled), nothing is in flight.
    internals.abortController = new AbortController();
    internals.isConnected_ = false;
    internals.connectAttemptInFlight = false;
    connectSpy.mockClear();

    service.start(7, "c1", 3);

    // The old guard keyed off the non-aborted controller and no-op'd forever.
    expect(connectSpy).toHaveBeenCalledTimes(1);
    expect(internals.lastSequence).toBe(7n);
    expect(internals.lastChatSequence).toBe(3n);
  });

  it("still refuses to double-connect a live stream", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;
    connectSpy.mockClear();

    service.start(1, "c1", 1);

    expect(connectSpy).not.toHaveBeenCalled();
  });
});

// Regression tests for the reconnect storm.
//
// A server-side fault (NATS restart wiped the memory-backed STREAMING_DELTAS
// stream, so the per-chat subscribe 404'd and ended the RPC) made every stream
// connect successfully and then end within milliseconds. The client treated
// each "connected" as a recovery and reset its backoff, so the exponential
// delay never engaged: measured at ~20 reconnect cycles/second for three-plus
// hours, each cycle dragging a full ListChats/GetChat/ListArchivedChats
// refetch behind it.
//
// The client cannot fix the server, but it must not amplify it. These pin the
// two rules that turn a storm into a slow retry.
describe("reconnect storm damping", () => {
  it("does not reset backoff for a connection that dies immediately", () => {
    const { internals } = makeService();

    // Cycle: attempt -> connects -> ends almost instantly. This is exactly the
    // observed loop, and it must escalate rather than stay pinned at attempt 1.
    for (let i = 0; i < 5; i++) {
      internals.attemptReconnect();
      vi.advanceTimersByTime(60_000);
      internals.isConnected_ = true;
      internals.connectionOpenedAt = Date.now();
      internals.armWatchdog();
      // Dies well inside the healthy-connection window.
      vi.advanceTimersByTime(50);
      internals.isConnected_ = false;
    }

    expect(internals.reconnectAttempts).toBe(5);
  });

  it("resets backoff once a connection survives the healthy window", () => {
    const { internals } = makeService();

    // Advance between calls: attemptReconnect no-ops while a timer is pending.
    internals.attemptReconnect();
    vi.advanceTimersByTime(60_000);
    internals.attemptReconnect();
    vi.advanceTimersByTime(60_000);
    expect(internals.reconnectAttempts).toBe(2);

    // A genuinely healthy connection: stays up past SHORT_LIVED_CONNECTION_MS
    // and keeps receiving events, so the watchdog promotes it.
    internals.isConnected_ = true;
    internals.connectionOpenedAt = Date.now();
    internals.armWatchdog();
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(15_000);
      internals.lastEventAt = Date.now();
    }

    expect(internals.reconnectAttempts).toBe(0);
  });

  it("routes a churning internal resubscribe through backoff, not an instant reconnect", () => {
    const { internals, connectSpy } = makeService();

    // Backoff already engaged from prior failures.
    internals.attemptReconnect();
    vi.advanceTimersByTime(60_000);
    connectSpy.mockClear();

    // An internal trigger (resubscribe / gap resync) must NOT reconnect
    // instantly while the backoff is engaged — that is the bypass that let the
    // loop run at full speed.
    internals.reconnectNow(
      "resubscribe",
    );
    expect(connectSpy).not.toHaveBeenCalled();

    // It is scheduled, not dropped: the stream still heals.
    vi.advanceTimersByTime(60_000);
    expect(connectSpy).toHaveBeenCalledTimes(1);
  });

  it("still collapses backoff instantly for a real user wake", () => {
    const { internals, connectSpy } = makeService();

    internals.attemptReconnect();
    vi.advanceTimersByTime(60_000);
    connectSpy.mockClear();

    // Back online / tab visible are genuine signals that the world changed,
    // so these keep reconnecting immediately.
    internals.reconnectNow(
      "online",
    );

    expect(connectSpy).toHaveBeenCalledTimes(1);
    expect(internals.reconnectAttempts).toBe(0);
  });
});
