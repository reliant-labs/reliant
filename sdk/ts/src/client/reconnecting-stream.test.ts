import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  isConnectionLevelFailure,
  ReconnectingStream,
  RESUME_LIVENESS_GRACE_MS,
  RESUME_REPLACE_AFTER_HIDDEN_MS,
  RESUME_REPLACE_MIN_INTERVAL_MS,
  SHORT_LIVED_CONNECTION_MS,
  STREAM_STALE_TIMEOUT_MS,
  WATCHDOG_TICK_MS,
  type StreamEnvironment,
  type WakeHandlers,
  type WakeSource,
} from "./reconnecting-stream.js";
import type { ConnectionStatus } from "./events.js";

// The connection lifecycle every stream shares: backoff that only a wake
// resets, the half-open watchdog, replace-on-resume, supersession.

interface Script {
  events?: string[];
  /** After the events: "stay" open, "end" cleanly, or fail with an error. */
  then: "stay" | "end" | Error;
}

class TestStream extends ReconnectingStream<string> {
  readonly opens: AbortSignal[] = [];
  readonly handled: string[] = [];
  readonly statuses: ConnectionStatus[] = [];
  readonly errors: string[] = [];
  scripts: Script[] = [];
  cleanEndReconnects = true;

  constructor(env: StreamEnvironment = {}) {
    super("[test]", { onStatusChange: (s) => this.statuses.push(s), onError: (e) => this.errors.push(e) }, env);
  }

  start(): void {
    this.open();
  }
  stop(reset = true): void {
    this.close(reset);
  }
  get connected(): boolean {
    return this.isConnected();
  }
  get attempts(): number {
    return this.reconnectAttempts;
  }

  protected openStream(signal: AbortSignal): AsyncIterable<string> {
    this.opens.push(signal);
    const script = this.scripts.shift() ?? { then: "stay" };
    return {
      async *[Symbol.asyncIterator]() {
        for (const e of script.events ?? []) yield e;
        if (script.then === "stay") {
          await new Promise<void>((_, reject) => signal.addEventListener("abort", () => reject(new ConnectError("aborted", Code.Canceled))));
        } else if (script.then instanceof Error) {
          throw script.then;
        }
      },
    };
  }
  protected handleEvent(event: string): void {
    this.handled.push(event);
  }
  protected reconnectAfterCleanEnd(): boolean {
    return this.cleanEndReconnects;
  }
}

function fakeWake() {
  let handlers: WakeHandlers | null = null;
  let visible = true;
  const wake: WakeSource = {
    subscribe: (h) => {
      handlers = h;
      return () => {
        handlers = null;
      };
    },
    isVisible: () => visible,
  };
  return {
    wake,
    get bound() {
      return handlers !== null;
    },
    online: () => handlers!.online(),
    hide: () => {
      visible = false;
      handlers!.visibilityChange();
    },
    show: () => {
      visible = true;
      handlers!.visibilityChange();
    },
    pageShow: (cached: boolean) => handlers!.pageShow(cached),
  };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
  vi.spyOn(Math, "random").mockReturnValue(0);
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const settle = () => vi.advanceTimersByTimeAsync(0);

describe("reconnecting", () => {
  it("delivers events and reports connecting -> connected", async () => {
    const s = new TestStream({ wake: fakeWake().wake });
    s.scripts = [{ events: ["a", "b"], then: "stay" }];
    s.start();
    await settle();
    expect(s.handled).toEqual(["a", "b"]);
    expect(s.statuses).toEqual(["connecting", "connected"]);
    s.stop();
  });

  it("backs off exponentially after failures and never gives up", async () => {
    const s = new TestStream({ wake: fakeWake().wake });
    s.scripts = Array.from({ length: 14 }, () => ({ then: new ConnectError("down", Code.Unavailable) }));
    s.start();
    await settle();
    const opensAt: number[] = [];
    let last = s.opens.length;
    for (let t = 0; t < 400_000; t += 500) {
      await vi.advanceTimersByTimeAsync(500);
      if (s.opens.length !== last) {
        opensAt.push(t);
        last = s.opens.length;
      }
    }
    // 1s, 2s, 4s, 8s, 16s, 30s (capped), 30s ...
    const gaps = opensAt.slice(1).map((t, i) => t - opensAt[i]!);
    expect(gaps.slice(0, 5)).toEqual([2000, 4000, 8000, 16000, 30000]);
    expect(s.opens.length).toBeGreaterThan(9);
    expect(s.errors.length).toBeGreaterThanOrEqual(s.opens.length - 1);
    s.stop();
  });

  it("does not reset the backoff on connect, only after a connection survives", async () => {
    const s = new TestStream({ wake: fakeWake().wake });
    // Accepts, delivers, drops — four times in a row.
    s.scripts = [
      ...Array.from({ length: 4 }, () => ({ events: ["hi"], then: new ConnectError("drop", Code.Unavailable) })),
      { events: ["hi"], then: "stay" },
    ];
    s.start();
    // Four drops: delays 1s, 2s, 4s, 8s, the fifth connection stays.
    await vi.advanceTimersByTimeAsync(16_000);
    expect(s.connected).toBe(true);
    expect(s.attempts).toBe(4);

    // A connection that stays up past the short-lived window starts fresh.
    await vi.advanceTimersByTimeAsync(SHORT_LIVED_CONNECTION_MS + WATCHDOG_TICK_MS);
    expect(s.attempts).toBe(0);
    s.stop();
  });

  it("reopens a stream the server ended cleanly, unless the subclass says it is over", async () => {
    const s = new TestStream({ wake: fakeWake().wake });
    s.scripts = [{ events: ["a"], then: "end" }, { events: ["b"], then: "stay" }];
    s.start();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(s.opens).toHaveLength(2);

    const done = new TestStream({ wake: fakeWake().wake });
    done.cleanEndReconnects = false;
    done.scripts = [{ events: ["a"], then: "end" }];
    done.start();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(done.opens).toHaveLength(1);
    s.stop();
  });

  it("stops after maxReconnectAttempts when told to, and reports it", async () => {
    const s = new TestStream({ wake: fakeWake().wake, tuning: { maxReconnectAttempts: 2, jitterMs: 0 } });
    s.scripts = Array.from({ length: 10 }, () => ({ then: new ConnectError("down", Code.Unavailable) }));
    s.start();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(s.opens).toHaveLength(3);
    expect(s.errors.at(-1)).toBe("Max reconnection attempts reached");
  });

  it("does not reconnect after stop()", async () => {
    const s = new TestStream({ wake: fakeWake().wake });
    s.scripts = [{ events: ["a"], then: "stay" }];
    s.start();
    await settle();
    s.stop();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(s.opens).toHaveLength(1);
    expect(s.opens[0]!.aborted).toBe(true);
  });
});

describe("liveness", () => {
  it("replaces a half-open connection that has gone silent past the stale timeout", async () => {
    const lost = vi.fn();
    const s = new TestStream({ wake: fakeWake().wake, onConnectionLost: lost });
    s.scripts = [{ events: ["a"], then: "stay" }, { events: ["b"], then: "stay" }];
    s.start();
    await settle();
    await vi.advanceTimersByTimeAsync(STREAM_STALE_TIMEOUT_MS + WATCHDOG_TICK_MS);
    expect(s.opens).toHaveLength(2);
    expect(s.opens[0]!.aborted).toBe(true);
    expect(lost).toHaveBeenCalledTimes(1);
    s.stop();
  });

  it("is off when the tuning says silence is normal", async () => {
    const s = new TestStream({ wake: fakeWake().wake, tuning: { liveness: false } });
    s.scripts = [{ events: ["a"], then: "stay" }];
    s.start();
    await settle();
    await vi.advanceTimersByTimeAsync(10 * STREAM_STALE_TIMEOUT_MS);
    expect(s.opens).toHaveLength(1);
    s.stop();
  });
});

describe("waking", () => {
  it("replaces a connected stream when the network comes back, once per burst", async () => {
    const w = fakeWake();
    const lost = vi.fn();
    const s = new TestStream({ wake: w.wake, onConnectionLost: lost });
    s.scripts = [{ events: ["a"], then: "stay" }, { events: ["b"], then: "stay" }];
    s.start();
    await settle();
    w.online();
    await settle();
    expect(s.opens).toHaveLength(2);
    expect(lost).toHaveBeenCalledTimes(1);

    // Another wake signal in the same burst leaves the new connection alone.
    w.online();
    await settle();
    expect(s.opens).toHaveLength(2);

    // A later, separate wake replaces it again.
    await vi.advanceTimersByTimeAsync(RESUME_REPLACE_MIN_INTERVAL_MS + 1);
    w.online();
    await settle();
    expect(s.opens).toHaveLength(3);
    s.stop();
  });

  it("collapses the backoff when the network returns while disconnected", async () => {
    const w = fakeWake();
    const s = new TestStream({ wake: w.wake });
    s.scripts = [{ then: new ConnectError("down", Code.Unavailable) }, { then: new ConnectError("down", Code.Unavailable) }, { events: ["ok"], then: "stay" }];
    s.start();
    await vi.advanceTimersByTimeAsync(1_500);
    expect(s.opens).toHaveLength(2);
    // The next retry is seconds away; the wake does not wait for it.
    w.online();
    await settle();
    expect(s.opens).toHaveLength(3);
    expect(s.attempts).toBe(0);
    s.stop();
  });

  it("replaces a stream after a long hide, and checks silence after a short one", async () => {
    const w = fakeWake();
    const s = new TestStream({ wake: w.wake });
    s.scripts = [{ events: ["a"], then: "stay" }, { events: ["b"], then: "stay" }];
    s.start();
    await settle();

    w.hide();
    await vi.advanceTimersByTimeAsync(RESUME_REPLACE_AFTER_HIDDEN_MS + 1_000);
    w.show();
    await settle();
    expect(s.opens).toHaveLength(2);

    // A short hide with a live stream is left alone.
    await vi.advanceTimersByTimeAsync(RESUME_REPLACE_MIN_INTERVAL_MS + 1);
    w.hide();
    await vi.advanceTimersByTimeAsync(1_000);
    w.show();
    await vi.advanceTimersByTimeAsync(RESUME_LIVENESS_GRACE_MS + 1);
    expect(s.opens).toHaveLength(2);
    s.stop();
  });

  it("treats a back/forward-cache restore as a resume, and an ordinary page show as nothing", async () => {
    const w = fakeWake();
    const s = new TestStream({ wake: w.wake });
    s.scripts = [{ events: ["a"], then: "stay" }, { events: ["b"], then: "stay" }];
    s.start();
    await settle();
    w.pageShow(false);
    await settle();
    expect(s.opens).toHaveLength(1);
    w.pageShow(true);
    await settle();
    expect(s.opens).toHaveLength(2);
    s.stop();
  });

  it("unbinds its wake handlers on stop", async () => {
    const w = fakeWake();
    const s = new TestStream({ wake: w.wake });
    s.start();
    await settle();
    expect(w.bound).toBe(true);
    s.stop();
    expect(w.bound).toBe(false);
  });
});

describe("supersession", () => {
  it("ignores everything a replaced connection still delivers", async () => {
    const w = fakeWake();
    const s = new TestStream({ wake: w.wake });
    s.scripts = [{ events: ["old-1"], then: "stay" }, { events: ["new-1"], then: "stay" }];
    s.start();
    await settle();
    w.online();
    await settle();
    expect(s.handled).toEqual(["old-1", "new-1"]);
    expect(s.errors).toEqual([]);
    s.stop();
  });
});

describe("isConnectionLevelFailure", () => {
  it("is true for a dead connection and false for a server status", () => {
    expect(isConnectionLevelFailure(new Error("socket hang up"))).toBe(true);
    expect(isConnectionLevelFailure(new ConnectError("x", Code.Unavailable))).toBe(true);
    expect(isConnectionLevelFailure(new ConnectError("x", Code.Unknown))).toBe(true);
    expect(isConnectionLevelFailure(new ConnectError("x", Code.NotFound))).toBe(false);
    expect(isConnectionLevelFailure(new ConnectError("x", Code.PermissionDenied))).toBe(false);
  });
});
