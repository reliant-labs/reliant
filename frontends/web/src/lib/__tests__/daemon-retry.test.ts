/**
 * The action-retry contract.
 *
 * The behaviour under test is "the user pressed send once and it went
 * through", plus its two boundaries: a real error must not be retried into a
 * two-minute silence, and a machine that never arrives must eventually be
 * reported rather than retried forever.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";

import { sendWithDaemonWait, DAEMON_ACTION_RETRY_MS } from "../daemon-retry";

/** The shape the gateway actually returns while no machine is attached. */
function noDaemonError(): ConnectError {
  return new ConnectError("unavailable: no daemon connected for user", Code.Internal);
}

describe("sendWithDaemonWait", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("passes a healthy action straight through without announcing anything", async () => {
    const action = vi.fn().mockResolvedValue(undefined);
    const onWaiting = vi.fn();

    await sendWithDaemonWait({ action, onWaiting });

    expect(action).toHaveBeenCalledTimes(1);
    // The common case must stay silent — a toast on every send would be worse
    // than the bug this replaces.
    expect(onWaiting).not.toHaveBeenCalled();
  });

  it("retries while the machine is unavailable and resolves once it arrives", async () => {
    const action = vi
      .fn()
      .mockRejectedValueOnce(noDaemonError())
      .mockRejectedValueOnce(noDaemonError())
      .mockResolvedValueOnce(undefined);
    const onWaiting = vi.fn();
    const onResolved = vi.fn();

    const promise = sendWithDaemonWait({ action, onWaiting, onResolved });
    await vi.runAllTimersAsync();
    await promise;

    expect(action).toHaveBeenCalledTimes(3);
    // Announced once, not once per attempt.
    expect(onWaiting).toHaveBeenCalledTimes(1);
    expect(onResolved).toHaveBeenCalledTimes(1);
  });

  it("propagates a real error immediately without retrying", async () => {
    const realError = new ConnectError("permission denied", Code.PermissionDenied);
    const action = vi.fn().mockRejectedValue(realError);
    const onWaiting = vi.fn();

    await expect(sendWithDaemonWait({ action, onWaiting })).rejects.toThrow(
      "permission denied",
    );

    // Retrying an auth failure would hide it for two minutes.
    expect(action).toHaveBeenCalledTimes(1);
    expect(onWaiting).not.toHaveBeenCalled();
  });

  it("gives up once the budget is spent and surfaces the original error", async () => {
    const action = vi.fn().mockRejectedValue(noDaemonError());

    const promise = sendWithDaemonWait({ action, timeoutMs: 10_000 });
    const assertion = expect(promise).rejects.toThrow(/no daemon connected/);
    await vi.advanceTimersByTimeAsync(20_000);
    await assertion;

    expect(action.mock.calls.length).toBeGreaterThan(1);
  });

  it("defaults to a budget that covers a cold start", () => {
    // A cold cloud start is image pull plus clone; a budget under that would
    // fail sends that were about to succeed.
    expect(DAEMON_ACTION_RETRY_MS).toBeGreaterThanOrEqual(60_000);
  });

  it("stops retrying when aborted", async () => {
    const controller = new AbortController();
    const action = vi.fn().mockRejectedValue(noDaemonError());

    const promise = sendWithDaemonWait({ action, signal: controller.signal });
    const assertion = expect(promise).rejects.toThrow();
    controller.abort();
    await vi.runAllTimersAsync();
    await assertion;
  });
});
