import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { DAEMON_WAIT_SLOW_MS } from "@/lib/daemon-wait";

// ---------------------------------------------------------------------------
// The escalation measures how long the USER has been waiting on the machine.
// Each surface used to keep its own stopwatch, so a chat opened five minutes
// into a boot told the user "start it and this will connect on its own" with
// no way out, while the file tree beside it had long since escalated. Now that
// only one surface explains the wait, the speaker can be the newest surface,
// and its clock is the one the user reads.
// ---------------------------------------------------------------------------

// Self-hosted keeps the status poll disabled, so no RPC is needed to exercise
// the clock; the escalation tiers are the same shape either way.
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: false },
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons: vi.fn() }) },
}));

import { useDaemonWait } from "../useDaemonWait";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={createTestQueryClient()}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("useDaemonWait shares one clock across surfaces", () => {
  it("starts a surface that joins a long wait at the wait's real age", () => {
    const fileTree = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    expect(fileTree.result.current.state?.showRetry).toBe(false);

    act(() => {
      vi.advanceTimersByTime(DAEMON_WAIT_SLOW_MS + 5_000);
    });
    expect(fileTree.result.current.state?.showRetry).toBe(true);

    // A surface that starts waiting now is waiting on the same machine.
    const chat = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    expect(chat.result.current.elapsedMs).toBeGreaterThanOrEqual(DAEMON_WAIT_SLOW_MS);
    expect(chat.result.current.state?.showRetry).toBe(true);
  });

  it("starts the next outage from zero once every surface has stopped waiting", () => {
    const first = renderHook(({ waiting }) => useDaemonWait({ waiting }), {
      wrapper,
      initialProps: { waiting: true },
    });
    act(() => {
      vi.advanceTimersByTime(DAEMON_WAIT_SLOW_MS + 5_000);
    });

    first.rerender({ waiting: false });
    expect(first.result.current.state).toBeNull();

    const next = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    expect(next.result.current.elapsedMs).toBeLessThan(DAEMON_WAIT_SLOW_MS);
    expect(next.result.current.state?.showRetry).toBe(false);
  });

  it("restarts every surface's escalation together on a manual retry", () => {
    const fileTree = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    const terminal = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    act(() => {
      vi.advanceTimersByTime(DAEMON_WAIT_SLOW_MS + 5_000);
    });
    expect(terminal.result.current.state?.showRetry).toBe(true);

    act(() => {
      fileTree.result.current.retryNow();
    });
    // The other surface picks the reset up on its next tick.
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(terminal.result.current.elapsedMs).toBeLessThan(DAEMON_WAIT_SLOW_MS);
    expect(terminal.result.current.state?.showRetry).toBe(false);
  });
});
