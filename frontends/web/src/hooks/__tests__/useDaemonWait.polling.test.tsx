import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { createTestQueryClient } from "@/test/renderWithQuery";
import {
  DaemonInfoSchema,
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";

// ---------------------------------------------------------------------------
// ListDaemons was the busiest RPC in prod: 2,750 calls in 24h (2026-10-08),
// ~1,700 of them exactly 2s apart from one open tab. A terminal waiting on a
// SUSPENDED machine polled the daemon list every 2s, on its own cache key that
// the gateway's push never invalidated, for as long as the tab was visible —
// for a machine that will not change state until the user acts.
// ---------------------------------------------------------------------------

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true },
}));

const listDaemons = vi.hoisted(() => vi.fn());
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons }) },
}));

import { useDaemonWait } from "../useDaemonWait";
import { DAEMON_LIST_QUERY_KEY, invalidateDaemonList } from "../useDaemonStatus";

function daemon(status: DaemonStatus): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId: "d1", status });
}

let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  vi.useFakeTimers();
  client = createTestQueryClient();
  listDaemons.mockReset();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("useDaemonWait's status reads", () => {
  it("does not poll a machine that is waiting on the user", async () => {
    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.SUSPENDED)] });
    const { result } = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(result.current.state?.title).toMatch(/suspended/i);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    // One read when the wait began. The old 2s poll made thirty.
    expect(listDaemons).toHaveBeenCalledTimes(1);
  });

  it("reads the shared, push-invalidated daemon list", async () => {
    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.SUSPENDED)] });
    renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(client.getQueryData(DAEMON_LIST_QUERY_KEY)).toHaveLength(1);

    // The gateway's `daemons` refetch reaches the waiting surface.
    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.ACTIVE)] });
    await act(async () => {
      invalidateDaemonList(client);
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(listDaemons).toHaveBeenCalledTimes(2);
  });

  it("retries the caller the moment the machine attaches", async () => {
    const onRetry = vi.fn();
    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    renderHook(() => useDaemonWait({ waiting: true, onRetry }), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    onRetry.mockClear();

    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.ACTIVE)] });
    await act(async () => {
      invalidateDaemonList(client);
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("backs the status poll off to a safety net while a machine is starting", async () => {
    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    // 1 initial + a 15s backstop. Push, not this poll, keeps it current.
    expect(listDaemons.mock.calls.length).toBeLessThanOrEqual(5);
    expect(listDaemons.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});
