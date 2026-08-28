import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useOAuthAvailability } from "../useOAuthAvailability";

/**
 * THE REPORTED BUG: `reliant auth serve` is killed mid-session. The panel keeps
 * showing "Login with Codex", the user clicks it, and the flow dies with a raw
 * "Failed to fetch" — no explanation, no way back to the install instructions.
 *
 * The cause is that the availability poll stopped once the helper was seen
 * once: its effect returned early on `available`, so nothing ever re-checked.
 * `available` was a latch, not a live signal.
 */
describe("useOAuthAvailability — helper disappears mid-session", () => {
  const originalFetch = global.fetch;

  beforeEach(() => {
    // REAL timers: pingHealth uses AbortSignal.timeout(), which is backed by
    // the platform's timer and does not advance under vi.useFakeTimers() —
    // every probe would hang instead of resolving.
    (window as unknown as { electronAPI?: unknown }).electronAPI = undefined;
  });

  afterEach(() => {
    global.fetch = originalFetch;
  });

  it("flips back to unavailable when the helper stops responding", async () => {
    // Healthy at first.
    const fetchMock = vi.fn().mockResolvedValue({ ok: true } as Response);
    global.fetch = fetchMock as unknown as typeof fetch;

    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));

    await waitFor(() => {
      expect(result.current.available).toBe(true);
    });

    // The user Ctrl-Cs `reliant auth serve`. Every probe now fails.
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));

    await waitFor(
      () => {
        expect(result.current.available).toBe(false);
      },
      { timeout: 6000 },
    );
  });

  it("recovers on its own when the helper is restarted", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    global.fetch = fetchMock as unknown as typeof fetch;

    const { result } = renderHook(() => useOAuthAvailability({ enabled: true }));

    await waitFor(() => {
      expect(result.current.available).toBe(false);
    });

    fetchMock.mockResolvedValue({ ok: true } as Response);

    await waitFor(
      () => {
        expect(result.current.available).toBe(true);
      },
      { timeout: 6000 },
    );
  });

  it("does not probe at all while disabled", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true } as Response);
    global.fetch = fetchMock as unknown as typeof fetch;

    renderHook(() => useOAuthAvailability({ enabled: false }));
    await new Promise((resolve) => setTimeout(resolve, 100));

    // Probing from a deployed origin triggers Chrome's Local Network Access
    // prompt, so a disabled panel must stay silent.
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
