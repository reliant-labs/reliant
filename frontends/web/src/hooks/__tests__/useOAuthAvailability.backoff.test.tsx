import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  OAUTH_HELPER_MAX_POLL_INTERVAL_MS,
  OAUTH_HELPER_NO_MACHINE_STOP_AFTER,
  OAUTH_HELPER_POLL_INTERVAL_MS,
  useOAuthAvailability,
} from "../useOAuthAvailability";

/**
 * THE REPORTED BUG: a browser user with no machine on their account sat on the
 * model step's sign-in panel while the app asked the daemon to open its helper
 * every 2 seconds, was refused every time (`no machine is connected to your
 * account yet`, 400), and told them nothing. Up to ~250 refused calls in one
 * session, and one user stayed 30 minutes.
 *
 * The refusal is about the ACCOUNT, not about timing, so the hook must (a) back
 * off while failures last, (b) stop once the same "no machine" answer repeats,
 * (c) say WHY, and (d) start again when the user presses Retry.
 */

const mocks = vi.hoisted(() => ({
  open: vi.fn(),
  close: vi.fn(),
}));

// The two daemon RPCs are the only network the hook reaches besides the
// localhost probe; answering them here keeps the test on the hook's own logic.
vi.mock("@/lib/connect", () => ({
  connectClient: () => ({
    openOAuthHelper: mocks.open,
    closeOAuthHelper: mocks.close,
  }),
}));

const refusedNoMachine = () =>
  new ConnectError(
    "no daemon available: no machine is connected to your account yet",
    Code.FailedPrecondition,
    new Headers({ "x-forge-error-reason": "no_daemon" }),
  );

const refusedOffline = () =>
  new ConnectError(
    "no daemon connected for user",
    Code.Unavailable,
    new Headers({ "x-forge-error-reason": "daemon_not_connected" }),
  );

const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

describe("useOAuthAvailability — a refused helper does not poll forever", () => {
  const originalFetch = global.fetch;

  beforeAll(async () => {
    // The hook reaches the daemon client through lazy imports; load them before
    // any fake clock starts so they are cache hits and nothing is real-time.
    await Promise.all([import("@/lib/connect"), import("@/gen/reliant/v1/daemon_pb")]);
  });

  beforeEach(() => {
    vi.useFakeTimers();
    (window as unknown as { electronAPI?: unknown }).electronAPI = undefined;
    mocks.open.mockReset();
    mocks.close.mockReset().mockResolvedValue({});
    // Nothing answers on localhost.
    global.fetch = vi.fn().mockRejectedValue(new TypeError("Failed to fetch")) as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.useRealTimers();
    global.fetch = originalFetch;
  });

  it("stops asking after the same 'no machine' answer repeats, and says why", async () => {
    mocks.open.mockRejectedValue(refusedNoMachine());

    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);
    expect(result.current.available).toBe(false);
    expect(result.current.problem).toBe("no_machine");
    expect(result.current.stopped).toBe(false);

    // The one allowed re-check (the stop threshold is two answers)…
    await advance(OAUTH_HELPER_POLL_INTERVAL_MS);
    expect(result.current.stopped).toBe(true);
    const callsWhenStopped = mocks.open.mock.calls.length;
    expect(callsWhenStopped).toBe(OAUTH_HELPER_NO_MACHINE_STOP_AFTER);

    // …and then silence, however long the user stays.
    await advance(10 * 60_000);
    expect(mocks.open).toHaveBeenCalledTimes(callsWhenStopped);
  });

  it("Retry starts it again from a clean slate", async () => {
    mocks.open.mockRejectedValue(refusedNoMachine());
    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(OAUTH_HELPER_POLL_INTERVAL_MS);
    expect(result.current.stopped).toBe(true);
    const before = mocks.open.mock.calls.length;

    act(() => result.current.recheck());
    await advance(0);

    expect(result.current.stopped).toBe(false);
    expect(mocks.open.mock.calls.length).toBeGreaterThan(before);
  });

  it("a machine that exists but is not answering backs off instead of hammering every 2s", async () => {
    mocks.open.mockRejectedValue(refusedOffline());
    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);
    expect(result.current.problem).toBe("machine_offline");

    // Five minutes. At a fixed 2s cadence this was 150 attempts; doubling to a
    // 30s ceiling is a dozen at most.
    await advance(5 * 60_000);
    expect(mocks.open.mock.calls.length).toBeLessThanOrEqual(15);
    expect(mocks.open.mock.calls.length).toBeGreaterThan(3);
    // It keeps trying (a machine that is merely restarting will come back), but
    // never faster than the first interval and never slower than the ceiling.
    expect(result.current.stopped).toBe(false);
    expect(OAUTH_HELPER_MAX_POLL_INTERVAL_MS).toBeGreaterThan(OAUTH_HELPER_POLL_INTERVAL_MS);
  });

  it("recovers on its own when the machine comes back", async () => {
    mocks.open.mockRejectedValue(refusedOffline());
    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);
    expect(result.current.available).toBe(false);

    mocks.open.mockResolvedValue({});
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok", service: "reliant", ready: true, version: "t", source: "daemon" }),
    }) as unknown as typeof fetch;

    await advance(OAUTH_HELPER_MAX_POLL_INTERVAL_MS);
    expect(result.current.available).toBe(true);
    expect(result.current.problem).toBeNull();
  });

  it("a daemon on another computer is reported as such, not as 'not running'", async () => {
    mocks.open.mockResolvedValue({});
    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);

    expect(result.current.problem).toBe("not_colocated");
  });

  it("releases the port only if one was opened", async () => {
    mocks.open.mockRejectedValue(refusedNoMachine());
    const refused = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);
    refused.unmount();
    await advance(0);
    expect(mocks.close).not.toHaveBeenCalled();
  });
});
