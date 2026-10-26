import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  RESUME_LIVENESS_GRACE_MS,
  RESUME_REPLACE_AFTER_HIDDEN_MS,
  RESUME_REPLACE_MIN_INTERVAL_MS,
  RESUME_STALE_AFTER_MS,
  UserStreamingService,
  isConnectionLevelFailure,
} from "../streaming-grpc";
import { pageActivity, resetPageActivityForTests } from "../../lib/pageActivity";
import type { ConnectionStatus } from "../../types/streaming";

// A phone unlocked, or a laptop opened, with the app's update stream still
// marked "connected": iOS suspended the socket without closing it, so no
// stream error ever arrives. Before this, nothing replaced that stream until
// the 75s watchdog — every update in between was missed, and every read
// multiplexed onto the same dead HTTP/2 connection hung with it
// (ELECTRON-B5: "ListChats 86s, GetChat 86s, ListDaemons 86s").

type Internals = {
  bindWakeHandlers(): void;
  isConnected_: boolean;
  lastEventAt: number;
};

function makeService() {
  const onStatusChange = vi.fn<(s: ConnectionStatus) => void>();
  const service = new UserStreamingService({
    onUpdate: vi.fn(),
    onStatusChange,
    onSync: vi.fn(),
    onError: vi.fn(),
  });
  const connectSpy = vi
    .spyOn(service as unknown as { establishConnection: () => Promise<void> }, "establishConnection")
    .mockResolvedValue(undefined);
  const internals = service as unknown as Internals;
  internals.bindWakeHandlers();
  return { service, internals, connectSpy, onStatusChange };
}

function becomeVisible() {
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers();
  resetPageActivityForTests();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  resetPageActivityForTests();
});

describe("returning to the tab with a 'connected' stream", () => {
  it("replaces a stream that has been silent past a heartbeat, and says the connection is gone", () => {
    const { service, internals, connectSpy, onStatusChange } = makeService();
    const lost = vi.fn();
    pageActivity().onResume((e) => lost(e.reason));
    internals.isConnected_ = true;
    internals.lastEventAt = Date.now() - 10 * 60_000;

    becomeVisible();
    // A moment to drain anything the socket buffered while frozen.
    vi.advanceTimersByTime(RESUME_LIVENESS_GRACE_MS);

    expect(connectSpy).toHaveBeenCalledTimes(1);
    expect(onStatusChange).toHaveBeenCalledWith("disconnected");
    expect(lost).toHaveBeenCalledWith("connection-lost");
    service.stop();
  });

  it("leaves a stream alone when it is still delivering", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;
    internals.lastEventAt = Date.now() - 10 * 60_000;

    becomeVisible();
    // The buffered heartbeat arrives as the page thaws.
    internals.lastEventAt = Date.now();
    vi.advanceTimersByTime(RESUME_LIVENESS_GRACE_MS);

    expect(connectSpy).not.toHaveBeenCalled();
    service.stop();
  });

  it("treats a stream that has merely been quiet for less than a heartbeat as live", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;
    internals.lastEventAt = Date.now() - (RESUME_STALE_AFTER_MS - 10_000);

    becomeVisible();
    vi.advanceTimersByTime(RESUME_LIVENESS_GRACE_MS);

    expect(connectSpy).not.toHaveBeenCalled();
    service.stop();
  });
});

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("returning after the page was hidden for a while", () => {
  afterEach(() => {
    delete (document as unknown as Record<string, unknown>).visibilityState;
  });

  it("replaces a stream that is still delivering as far as it knows, without waiting out the silence threshold", () => {
    const { service, internals, connectSpy, onStatusChange } = makeService();
    internals.isConnected_ = true;

    setVisibility("hidden");
    vi.advanceTimersByTime(RESUME_REPLACE_AFTER_HIDDEN_MS + 1_000);
    // The frozen page's last event is recent as far as its own clock goes.
    internals.lastEventAt = Date.now();
    setVisibility("visible");

    expect(connectSpy).toHaveBeenCalledTimes(1);
    expect(onStatusChange).toHaveBeenCalledWith("disconnected");
    service.stop();
  });

  it("keeps a stream that was hidden only briefly", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;

    setVisibility("hidden");
    vi.advanceTimersByTime(RESUME_REPLACE_AFTER_HIDDEN_MS - 1_000);
    internals.lastEventAt = Date.now();
    setVisibility("visible");
    vi.advanceTimersByTime(RESUME_LIVENESS_GRACE_MS);

    expect(connectSpy).not.toHaveBeenCalled();
    service.stop();
  });

  it("replaces once for a burst of resume signals", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;

    setVisibility("hidden");
    vi.advanceTimersByTime(60_000);
    setVisibility("visible");
    // reconnectNow flipped it off; the new connection comes up and the browser
    // fires pageshow + online in the same wake.
    internals.isConnected_ = true;
    window.dispatchEvent(Object.assign(new Event("pageshow"), { persisted: true }));
    window.dispatchEvent(new Event("online"));

    expect(connectSpy).toHaveBeenCalledTimes(1);
    service.stop();
  });

  it("replaces again for a later, separate resume", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;

    setVisibility("hidden");
    vi.advanceTimersByTime(60_000);
    setVisibility("visible");
    internals.isConnected_ = true;
    vi.advanceTimersByTime(RESUME_REPLACE_MIN_INTERVAL_MS + 1);
    setVisibility("hidden");
    vi.advanceTimersByTime(60_000);
    setVisibility("visible");

    expect(connectSpy).toHaveBeenCalledTimes(2);
    service.stop();
  });

  it("replaces a connected stream when restored from the back/forward cache", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;

    window.dispatchEvent(Object.assign(new Event("pageshow"), { persisted: true }));

    expect(connectSpy).toHaveBeenCalledTimes(1);
    service.stop();
  });

  it("ignores a pageshow that is an ordinary load", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;

    window.dispatchEvent(Object.assign(new Event("pageshow"), { persisted: false }));

    expect(connectSpy).not.toHaveBeenCalled();
    service.stop();
  });

  it("replaces a connected stream when the network comes back", () => {
    const { service, internals, connectSpy } = makeService();
    internals.isConnected_ = true;

    window.dispatchEvent(new Event("online"));

    expect(connectSpy).toHaveBeenCalledTimes(1);
    service.stop();
  });
});

describe("isConnectionLevelFailure", () => {
  it("is true for a network failure and false for a status the server sent", () => {
    expect(isConnectionLevelFailure(new TypeError("Load failed"))).toBe(true);
    expect(isConnectionLevelFailure(new ConnectError("network error", Code.Unknown))).toBe(true);
    expect(isConnectionLevelFailure(new ConnectError("unavailable", Code.Unavailable))).toBe(true);
    expect(isConnectionLevelFailure(new ConnectError("denied", Code.PermissionDenied))).toBe(false);
    expect(isConnectionLevelFailure(new ConnectError("boom", Code.Internal))).toBe(false);
  });
});
