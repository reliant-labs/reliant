import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import {
  OAUTH_HELPER_POLL_INTERVAL_MS,
  useOAuthAvailability,
} from "../useOAuthAvailability";

// The daemon is unreachable throughout: the helper in these tests is a
// standalone `reliant auth serve`, which is the setup the reported bug was
// filed against. Mocking this explicitly also keeps the Connect client graph
// (and the auth interceptor's own lazy Supabase import) out of a hook test.
vi.mock("@/api/daemon-grpc", () => ({
  openOAuthHelper: vi.fn().mockRejectedValue(new Error("daemon unreachable")),
  closeOAuthHelper: vi.fn().mockResolvedValue(undefined),
}));

/**
 * THE REPORTED BUG: `reliant auth serve` is killed mid-session. The panel keeps
 * showing "Login with Codex", the user clicks it, and the flow dies with a raw
 * "Failed to fetch" — no explanation, no way back to the install instructions.
 *
 * The cause is that the availability poll stopped once the helper was seen
 * once: its effect returned early on `available`, so nothing ever re-checked.
 * `available` was a latch, not a live signal.
 *
 * # Why this runs on a fake clock
 *
 * These tests used to run on real timers and wait with `waitFor`, which made
 * them a race against the wall clock rather than a check of the hook. The step
 * that lost was not even the poll: the hook's FIRST check goes through a lazy
 * `import('@/api/daemon-grpc')`, which on first use loaded the whole Connect
 * client graph — about 300ms on an idle machine, and past `waitFor`'s 1000ms
 * default whenever the suite shared the CPU (10 of 36 runs failed with twelve
 * copies running at once). The hook was fine; the deadline was measuring how
 * busy the box was.
 *
 * So the test owns time now. The poll interval runs on vitest's fake clock and
 * advances exactly one interval at a time, and the module the hook imports
 * lazily is loaded once up front, so the hook's own import is a cache hit and
 * nothing in the path is real-time.
 *
 * `AbortSignal.timeout()` in the health probe is NOT driven by the fake clock —
 * jsdom schedules it on a timer captured before the globals are faked. It does
 * not need to be: the mocked fetch settles immediately and never waits on the
 * signal. Only a fetch mock that never settles would hang on it.
 */
describe("useOAuthAvailability — helper disappears mid-session", () => {
  const originalFetch = global.fetch;

  // probeOAuthHelper reads the body and requires `service === "reliant"`,
  // because any other dev server holding port 19284 also answers a health
  // probe — and the UI would then offer an OAuth flow that silently fails.
  // A bare `{ ok: true }` therefore reads as "not reliant" and the hook stays
  // unavailable, so the mock has to answer like the real helper does.
  const healthyHelperResponse = () =>
    ({
      ok: true,
      json: async () => ({
        status: "ok",
        service: "reliant",
        ready: true,
        version: "test",
        source: "test",
      }),
    }) as unknown as Response;

  /** Flush the in-flight check's promise chain without moving the clock. */
  const settle = async () => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
  };

  /** Run exactly one poll tick, including the probe and re-ask it awaits. */
  const runOnePoll = async () => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(OAUTH_HELPER_POLL_INTERVAL_MS);
    });
  };

  beforeAll(async () => {
    // Load the hook's lazily imported dependency before any clock starts, so
    // the import inside the hook resolves from the module cache.
    await import("@/api/daemon-grpc");
  });

  beforeEach(() => {
    vi.useFakeTimers();
    (window as unknown as { electronAPI?: unknown }).electronAPI = undefined;
  });

  afterEach(() => {
    vi.useRealTimers();
    global.fetch = originalFetch;
  });

  it("flips back to unavailable when the helper stops responding", async () => {
    // Healthy at first.
    const fetchMock = vi.fn().mockResolvedValue(healthyHelperResponse());
    global.fetch = fetchMock as unknown as typeof fetch;

    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));

    await settle();
    expect(result.current.available).toBe(true);

    // The user Ctrl-Cs `reliant auth serve`. Every probe now fails.
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));

    // The very next poll must notice: a dead helper may stay on screen for at
    // most one interval, not until something else happens to re-check.
    await runOnePoll();
    expect(result.current.available).toBe(false);
  });

  it("recovers on its own when the helper is restarted", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    global.fetch = fetchMock as unknown as typeof fetch;

    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));

    await settle();
    expect(result.current.available).toBe(false);

    fetchMock.mockResolvedValue(healthyHelperResponse());

    await runOnePoll();
    expect(result.current.available).toBe(true);
  });

  it("does not probe at all while disabled", async () => {
    const fetchMock = vi.fn().mockResolvedValue(healthyHelperResponse());
    global.fetch = fetchMock as unknown as typeof fetch;

    renderHook(() => useOAuthAvailability({ enabled: false }));

    // Several poll intervals' worth of time: a disabled hook must not have
    // started one.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(OAUTH_HELPER_POLL_INTERVAL_MS * 3);
    });

    // Probing from a deployed origin triggers Chrome's Local Network Access
    // prompt, so a disabled panel must stay silent.
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
