import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserStreamingService } from "../streaming-grpc";

// Regression test for a resync storm: a chat-level gap resync reconnects
// (preserving lastChatSequence), and ~1ms later reconcileChatSubscription's
// self-healing invariant re-asserts the SAME chatId via subscribeToChatDetails
// — before the first reconnect has even completed. subscribeToChatDetails()
// used to treat every call as "subscribe to a (possibly new) chat" and
// unconditionally reset lastChatSequence to 0n, throwing away the resync's
// cursor and forcing a full snapshot for a chat that never changed.
//
// Captured in production logs at 15:42:56.658-662 (350ms before a measured
// scroll-jitter event): a chat gap resync connects with chatSinceSeq: 225280,
// then 1ms later a second "Immediate reconnect { reason: 'resubscribe' }"
// (triggered by subscribeToChatDetails re-asserting the same chat) connects
// with chatSinceSeq: 0 — a wasted round trip plus a redundant full snapshot.

type ServiceInternals = {
  subscribedChatId: string | undefined;
  lastChatSequence: bigint;
  lastSequence: bigint;
  isConnected_: boolean;
  connectAttemptInFlight: boolean;
  establishConnection(): Promise<void>;
};

function makeService() {
  const service = new UserStreamingService({
    onUpdate: vi.fn(),
    onStatusChange: vi.fn(),
    onSync: vi.fn(),
    onError: vi.fn(),
    onChatUpdate: vi.fn(),
  });
  const internals = service as unknown as ServiceInternals;
  const connectSpy = vi
    .spyOn(
      service as unknown as { establishConnection: () => Promise<void> },
      "establishConnection",
    )
    .mockResolvedValue(undefined);
  return { service, internals, connectSpy };
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("subscribeToChatDetails re-assert of an unchanged chat", () => {
  it("is a no-op while a connection attempt for that same chat is already in flight", () => {
    const { service, internals, connectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 225280n;
    internals.isConnected_ = false;
    internals.connectAttemptInFlight = true; // first reconnect (gap resync) is mid-flight
    connectSpy.mockClear();

    service.subscribeToChatDetails("c1");

    expect(connectSpy).not.toHaveBeenCalled();
    expect(internals.lastChatSequence).toBe(225280n);
    expect(internals.subscribedChatId).toBe("c1");
  });

  it("is a no-op once already connected to that chat", () => {
    const { service, internals, connectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 999n;
    internals.isConnected_ = true;
    internals.connectAttemptInFlight = false;
    connectSpy.mockClear();

    service.subscribeToChatDetails("c1");

    expect(connectSpy).not.toHaveBeenCalled();
    expect(internals.lastChatSequence).toBe(999n);
  });

  it("still reconnects (preserving the cursor) for the same chat if genuinely disconnected", () => {
    const { service, internals, connectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 42n;
    internals.isConnected_ = false;
    internals.connectAttemptInFlight = false; // nothing in flight — really dead
    connectSpy.mockClear();

    service.subscribeToChatDetails("c1");

    expect(connectSpy).toHaveBeenCalledTimes(1);
    // Same chat: cursor must survive so the server replays instead of a snapshot.
    expect(internals.lastChatSequence).toBe(42n);
    expect(internals.subscribedChatId).toBe("c1");
  });

  it("a genuine chat switch still resets the cursor and requests a snapshot", () => {
    const { service, internals, connectSpy } = makeService();
    internals.subscribedChatId = "c1";
    internals.lastChatSequence = 225280n;
    internals.isConnected_ = true;
    connectSpy.mockClear();

    service.subscribeToChatDetails("c2");

    expect(connectSpy).toHaveBeenCalledTimes(1);
    expect(internals.subscribedChatId).toBe("c2");
    expect(internals.lastChatSequence).toBe(0n);
  });
});
