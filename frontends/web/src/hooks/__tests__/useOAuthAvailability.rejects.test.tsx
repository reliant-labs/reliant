import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { OAUTH_HELPER_POLL_INTERVAL_MS, useOAuthAvailability } from "../useOAuthAvailability";

/**
 * A check that REJECTS (not one that returns "unavailable") must not end the
 * poll. requestOAuthHelper normally catches its own RPC failures, but anything
 * that escapes it — a failed lazy import, a bug above the try — used to reject
 * the async tick before it could schedule the next one, stopping the check for
 * the rest of the panel's life with no state update.
 */
const mocks = vi.hoisted(() => ({ probe: vi.fn(), request: vi.fn(), release: vi.fn() }));

vi.mock("@/lib/oauth-local", () => ({
  probeOAuthHelper: () => mocks.probe(),
  requestOAuthHelper: () => mocks.request(),
  releaseOAuthHelper: () => mocks.release(),
}));

const ready = { ready: true, health: { status: "ok", service: "reliant", ready: true, version: "t", source: "daemon" } };

const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

describe("useOAuthAvailability — a rejecting check does not stop the poll", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    (window as unknown as { electronAPI?: unknown }).electronAPI = undefined;
    mocks.probe.mockReset().mockResolvedValue(null);
    mocks.release.mockReset().mockResolvedValue(undefined);
  });
  afterEach(() => vi.useRealTimers());

  it("when the first request rejects, it settles as unavailable and keeps polling", async () => {
    mocks.request.mockReset().mockRejectedValue(new Error("client import failed"));
    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);

    expect(result.current.loading).toBe(false);
    expect(result.current.available).toBe(false);
    const first = mocks.request.mock.calls.length;

    await advance(OAUTH_HELPER_POLL_INTERVAL_MS * 3);
    expect(mocks.request.mock.calls.length).toBeGreaterThan(first);
  });

  it("when a poll tick rejects, the next tick still runs and can recover", async () => {
    mocks.request.mockReset().mockResolvedValueOnce({ ready: false, reason: "machine_offline" }).mockRejectedValue(new Error("boom"));
    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));
    await advance(0);
    expect(result.current.problem).toBe("machine_offline");

    await advance(OAUTH_HELPER_POLL_INTERVAL_MS * 3);
    const afterRejections = mocks.request.mock.calls.length;
    expect(afterRejections).toBeGreaterThan(2);

    mocks.request.mockReset().mockResolvedValue(ready);
    await advance(60_000);
    expect(result.current.available).toBe(true);
  });
});
