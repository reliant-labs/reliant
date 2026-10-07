/**
 * A request that finds its machine asleep: the server wakes the machine and
 * fails the request as Unavailable with a `DaemonWaking` detail naming it
 * (internal/grpc/services/machine_wake.go). What the web must do with that:
 *
 *   - record the wake once, in the transport, so every surface reads it;
 *   - say "Waking up…" and keep retrying, rather than "Your machine is
 *     suspended" with no retry — which is what the registry still reads for
 *     a moment after the wake;
 *   - retry a one-shot action (commit, create a workspace) until the machine
 *     is up, but ONLY when the server said it woke one.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  DaemonStatus,
  DaemonWakingSchema,
  type DaemonInfo as Daemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { machineWakeInterceptor } from "@/api/machineWakeInterceptor";
import { isDaemonConnectingError, wakingDaemonId } from "../daemon-errors";
import { classifyDaemonWait, DAEMON_WAIT_POLL_MS, MACHINE_NOUN } from "../daemon-wait";
import { retryAcrossWake } from "../daemon-retry";
import { clearWaking, WAKE_SUSPENDED_GRACE_MS } from "../machineWake";
import * as machineWake from "../machineWake";

/** The error the server returns after waking daemonId for a request. */
function wakingError(daemonId: string): ConnectError {
  return new ConnectError("your machine is waking up: no daemon connected yet", Code.Unavailable, undefined, [
    { desc: DaemonWakingSchema, value: { daemonId } },
  ]);
}

/** A machine that is not connected and that nothing woke (offline laptop). */
function offlineError(): ConnectError {
  return new ConnectError("unavailable: no daemon connected for user", Code.Internal);
}

function daemon(partial: Partial<Daemon>): Daemon {
  return partial as Daemon;
}

describe("the waking error", () => {
  it("names the machine the server woke, and still reads as a machine wait", () => {
    const err = wakingError("daemon-b");
    expect(wakingDaemonId(err)).toBe("daemon-b");
    expect(isDaemonConnectingError(err)).toBe(true);
  });

  it("names nothing for a machine that was not woken", () => {
    expect(wakingDaemonId(offlineError())).toBeNull();
    expect(wakingDaemonId(new Error("boom"))).toBeNull();
  });
});

describe("machineWakeInterceptor", () => {
  afterEach(() => clearWaking("daemon-b"));

  it("records the wake and re-throws, so the caller's own wait takes over", async () => {
    const mark = vi.spyOn(machineWake, "markWaking");
    const err = wakingError("daemon-b");
    const call = machineWakeInterceptor(() => Promise.reject(err));

    await expect(call({} as never)).rejects.toBe(err);
    expect(mark).toHaveBeenCalledWith("daemon-b");
    mark.mockRestore();
  });

  it("records nothing for any other failure", async () => {
    const mark = vi.spyOn(machineWake, "markWaking");
    const call = machineWakeInterceptor(() => Promise.reject(offlineError()));

    await expect(call({} as never)).rejects.toBeInstanceOf(ConnectError);
    expect(mark).not.toHaveBeenCalled();
    mark.mockRestore();
  });
});

describe("classifyDaemonWait for a machine being woken", () => {
  const now = 1_000_000;

  it("says Waking up… and keeps retrying while the registry still reads SUSPENDED", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ daemonId: "daemon-b", status: DaemonStatus.SUSPENDED }),
      elapsedMs: 0,
      isCloud: true,
      wakeStartedAt: now - 1_000,
      now,
    });

    expect(state.title).toBe("Waking up…");
    expect(state.tone).not.toBe("failed");
    // The regression: the suspended branch returned shouldRetry=false, so the
    // tree stopped asking and never loaded once the machine was back.
    expect(state.shouldRetry).toBe(true);
  });

  it("narrates the control plane's start-up stage while the machine comes up", () => {
    const state = classifyDaemonWait({
      daemon: daemon({
        daemonId: "daemon-b",
        status: DaemonStatus.PENDING,
        lastStatusMessage: "Preparing your workspace image.",
      }),
      elapsedMs: 0,
      isCloud: true,
      wakeStartedAt: now - 10_000,
      now,
    });

    expect(state.title).toBe("Waking up…");
    expect(state.detail).toBe("Preparing your workspace image.");
    expect(state.shouldRetry).toBe(true);
  });

  it("falls back to the suspended copy once a wake plainly did not happen", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ daemonId: "daemon-b", status: DaemonStatus.SUSPENDED }),
      elapsedMs: 0,
      isCloud: true,
      wakeStartedAt: now - WAKE_SUSPENDED_GRACE_MS - 1,
      now,
    });

    expect(state.title).toBe(`Your ${MACHINE_NOUN} is suspended`);
    expect(state.shouldRetry).toBe(false);
  });

  it("is not a wake without a record: a suspended machine nobody woke stays suspended", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ daemonId: "daemon-b", status: DaemonStatus.SUSPENDED }),
      elapsedMs: 0,
      isCloud: true,
    });

    expect(state.title).toBe(`Your ${MACHINE_NOUN} is suspended`);
  });

  it("still reports a machine that failed to wake", () => {
    const state = classifyDaemonWait({
      daemon: daemon({ daemonId: "daemon-b", status: DaemonStatus.FAILED }),
      elapsedMs: 0,
      isCloud: true,
      wakeStartedAt: now - 1_000,
      now,
    });

    expect(state.tone).toBe("failed");
  });
});

describe("retryAcrossWake", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("finishes the action once the machine the server woke is up", async () => {
    const action = vi
      .fn<() => Promise<string>>()
      .mockRejectedValueOnce(wakingError("daemon-b"))
      .mockRejectedValueOnce(offlineError()) // still booting
      .mockResolvedValueOnce("committed");
    const onWaking = vi.fn();

    const result = retryAcrossWake(action, { onWaking });
    await vi.advanceTimersByTimeAsync(DAEMON_WAIT_POLL_MS * 3);

    await expect(result).resolves.toBe("committed");
    expect(onWaking).toHaveBeenCalledOnce();
    expect(onWaking).toHaveBeenCalledWith("daemon-b");
    expect(action).toHaveBeenCalledTimes(3);
  });

  it("fails at once when nothing was woken, as before", async () => {
    const err = offlineError();
    const action = vi.fn<() => Promise<void>>().mockRejectedValue(err);
    const onWaking = vi.fn();

    await expect(retryAcrossWake(action, { onWaking })).rejects.toBe(err);
    expect(action).toHaveBeenCalledOnce();
    expect(onWaking).not.toHaveBeenCalled();
  });
});
