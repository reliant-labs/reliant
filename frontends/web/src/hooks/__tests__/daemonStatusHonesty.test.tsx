import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, renderHook, screen } from "@testing-library/react";
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
// Prod 2026-10-11. The user's machine was attached and serving — their
// terminal ran `ls`, agent tool calls worked — while the new-chat composer
// said "Your machine disconnected / Trying to reach it again". The registry
// itself was wrong (its lease row was gone; fixed server-side), and the tab
// made it worse: woken from sleep, its 17 in-flight RPCs, ListDaemons among
// them, all timed out on a dead connection.
//
// Pinned here, on the web side:
//   - a failed daemon-list read is "can't reach Reliant", never news about
//     the machine;
//   - before the list has answered, nothing is known — a new chat must not
//     default to "no machine" (a one-way choice) because the first read is
//     still pending or failed;
//   - a heartbeat from a machine the cached list calls down makes the list
//     re-read itself, so the UI recovers without a reload.
// ---------------------------------------------------------------------------

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true },
}));

const listDaemons = vi.hoisted(() => vi.fn());
import { useDaemonWait } from "../useDaemonWait";
import {
  useDaemonStatus,
  HEARTBEAT_CONTRADICTION_REFETCH_MS,
  reconcileDaemonHeartbeat,
  resetDaemonHeartbeatReconcile,
} from "../useDaemonStatus";
import { CANT_REACH_RELIANT } from "@/lib/daemon-wait";
import { DAEMON_LIST_QUERY_KEY } from "../daemon-registry-queries";
import { daemonRegistryServiceKeys } from "../daemon-registry-service-hooks_gen";
import { ComposerWakeStatus } from "@/components/Chat/ComposerWakeStatus";
import { setTransport } from "@/lib/connect";
import { daemonRegistryTransport, seedDaemonList } from "@/test/daemonRegistry";

// The daemon list is read through connectClient(DaemonRegistryService).
beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons: () => listDaemons() })));
afterEach(() => setTransport(null));

function daemon(status: DaemonStatus, daemonId = "d1"): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId, status, hostname: "ws-ws-2aab1465" });
}

const timeout = Object.assign(new Error("[deadline_exceeded] ListDaemons timed out after 10000ms"), {
  code: 4,
});

let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  vi.useFakeTimers();
  client = createTestQueryClient();
  listDaemons.mockReset();
  resetDaemonHeartbeatReconcile();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("a daemon-list read that fails", () => {
  it("is 'can't reach Reliant', not the last machine status the list held", async () => {
    // Last good read: the machine was down (whatever the reason).
    seedDaemonList(client, [daemon(DaemonStatus.DISCONNECTED)]);
    // The tab wakes; every re-read times out.
    listDaemons.mockRejectedValue(timeout);

    const { result } = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });

    expect(listDaemons).toHaveBeenCalled();
    expect(result.current.state?.title).toBe(CANT_REACH_RELIANT);
    expect(result.current.state?.title).not.toMatch(/disconnected/i);
  });

  it("recovers on its own once a read succeeds", async () => {
    listDaemons.mockRejectedValueOnce(timeout);
    const { result } = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(result.current.state?.title).toBe(CANT_REACH_RELIANT);

    listDaemons.mockResolvedValue({ daemons: [daemon(DaemonStatus.PENDING)] });
    await act(async () => {
      result.current.retryNow();
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(result.current.state?.title).not.toBe(CANT_REACH_RELIANT);
  });

  it("does not tell the composer the chat's machine is offline", async () => {
    seedDaemonList(client, [daemon(DaemonStatus.DISCONNECTED)]);
    listDaemons.mockRejectedValue(timeout);
    await act(async () => {
      await client.refetchQueries({ queryKey: DAEMON_LIST_QUERY_KEY }).catch(() => {});
    });

    render(<ComposerWakeStatus sending={false} daemonId="d1" />, { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });

    const status = screen.getByTestId("composer-wake-status");
    expect(status.textContent).toContain(CANT_REACH_RELIANT);
    expect(status.textContent).not.toMatch(/offline|no longer available/i);
  });
});

describe("useDaemonStatus before the list has answered", () => {
  it("is loading, not an empty list, while the first read is in flight", async () => {
    listDaemons.mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(() => useDaemonStatus(), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    // An empty list here makes a new chat default to "no machine".
    expect(result.current.loading).toBe(true);
  });

  it("stays unknown (and says it cannot reach Reliant) when the first read fails", async () => {
    listDaemons.mockRejectedValue(timeout);
    const { result } = renderHook(() => useDaemonStatus(), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(result.current.answered).toBe(false);
    expect(result.current.unreachable).toBe(true);
  });
});

describe("a heartbeat from a machine the cached list calls down", () => {
  it("re-reads the list, so the UI recovers without a reload", async () => {
    seedDaemonList(client, [daemon(DaemonStatus.DISCONNECTED)]);
    const invalidate = vi.spyOn(client, "invalidateQueries");

    reconcileDaemonHeartbeat(client, "d1", 1_000);

    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: daemonRegistryServiceKeys.all }));
    expect(client.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated).toBe(true);
  });

  it("re-reads at most once per window while the registry keeps disagreeing", () => {
    seedDaemonList(client, [daemon(DaemonStatus.DISCONNECTED)]);
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const listReads = () =>
      invalidate.mock.calls.filter(([f]) => JSON.stringify(f?.queryKey) === JSON.stringify(daemonRegistryServiceKeys.all)).length;

    reconcileDaemonHeartbeat(client, "d1", 1_000);
    reconcileDaemonHeartbeat(client, "d1", 16_000);
    expect(listReads()).toBe(1);
    reconcileDaemonHeartbeat(client, "d1", 1_000 + HEARTBEAT_CONTRADICTION_REFETCH_MS);
    expect(listReads()).toBe(2);
  });

  it("leaves a list that already shows the machine connected alone", () => {
    seedDaemonList(client, [daemon(DaemonStatus.ACTIVE)]);
    const invalidate = vi.spyOn(client, "invalidateQueries");
    reconcileDaemonHeartbeat(client, "d1", 1_000);
    expect(invalidate).not.toHaveBeenCalled();
  });
});
