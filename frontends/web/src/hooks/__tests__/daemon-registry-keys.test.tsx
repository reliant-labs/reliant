/**
 * THE daemon list is one cache entry (hooks/daemon-registry-queries) on forge's
 * generated DaemonRegistryService keys. It used to be five hand-written keys
 * for the same ListDaemons({}) — the shared list, the onboarding gate's
 * per-attempt key and two Machines-page cadence keys — so a refetch reached
 * some readers and not others. These pin the one entry:
 *
 *   1. a `daemons` refetch on the user stream refetches every mounted reader,
 *      with one request;
 *   2. heartbeat contradiction reads the typed entry (the response, not a
 *      hand-shaped array);
 *   3. the onboarding gate ignores an answer read before its attempt began;
 *   4. a ClearMachineCaches (generated write) refetches the list.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, renderHook, screen } from "@testing-library/react";
import { QueryClientProvider, QueryObserver, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { EntityType, UserUpdateType } from "@/gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "@/types/streaming";

vi.mock("@/api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));

import { queryClient } from "@/lib/query-client";
import { setTransport } from "@/lib/connect";
import { daemonRegistryTransport, seedDaemonList } from "@/test/daemonRegistry";
import { DAEMON_LIST_QUERY_KEY, daemonListQuery } from "../daemon-registry-queries";
import { useDaemonList } from "../useOnboardingQueries";
import { reconcileDaemonHeartbeat, resetDaemonHeartbeatReconcile, useDaemonStatus } from "../useDaemonStatus";
import { useClearMachineCaches } from "../useMachineDisk";
import { useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { DaemonConnectingGate } from "@/components/OnboardingFlow/DaemonConnectingGate";

const listDaemons = vi.fn<() => Promise<{ daemons: Partial<DaemonInfo>[] }>>();
const clearMachineCaches = vi.fn(async () => ({}));

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** Let resolved fetches land and React re-render. */
async function settle() {
  await act(async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
  });
}

function refetchDaemons(): UserUpdate {
  return {
    id: "u-daemons",
    user_id: "user-1",
    sequence_number: 0,
    chat_id: "",
    project_id: "",
    update_type: UserUpdateType.REFETCH,
    entity_type: EntityType.SYSTEM,
    entity_id: "d1",
    data: { type: "daemons", daemon_id: "d1" },
    created_at: new Date().toISOString(),
  };
}

beforeEach(() => {
  queryClient.clear();
  resetDaemonHeartbeatReconcile();
  listDaemons.mockReset().mockResolvedValue({ daemons: [{ daemonId: "d1", status: DaemonStatus.ACTIVE }] });
  clearMachineCaches.mockClear();
  setTransport(daemonRegistryTransport({ listDaemons: () => listDaemons(), clearMachineCaches: () => clearMachineCaches() }));
});

afterEach(() => {
  cleanup();
  setTransport(null);
  queryClient.clear();
});

describe("the one daemon list", () => {
  it("a `daemons` refetch refetches every mounted reader with one request", async () => {
    // Three readers on three cadences — the status hook, the onboarding list
    // and a Machines-page style observer — share one entry.
    renderHook(
      () => {
        useDaemonStatus();
        useDaemonList();
        useQuery(daemonListQuery({ refetchInterval: 15_000 }));
      },
      { wrapper },
    );
    await settle();
    expect(listDaemons).toHaveBeenCalledTimes(1);

    listDaemons.mockResolvedValue({ daemons: [] });
    useGlobalUpdatesStore.getState().handleUpdate([refetchDaemons()]);
    await settle();

    expect(listDaemons).toHaveBeenCalledTimes(2);
    expect(queryClient.getQueryData(DAEMON_LIST_QUERY_KEY)?.daemons).toEqual([]);
  });

  it("heartbeat contradiction reads the typed entry", () => {
    // The cache holds the ListDaemons RESPONSE. Reading it as a bare array
    // (what the hand key held) would find no daemon and re-read every time.
    seedDaemonList(queryClient, [{ daemonId: "d1", status: DaemonStatus.ACTIVE }]);
    reconcileDaemonHeartbeat(queryClient, "d1", 1_000);
    expect(queryClient.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated).toBe(false);

    seedDaemonList(queryClient, [{ daemonId: "d1", status: DaemonStatus.DISCONNECTED }]);
    reconcileDaemonHeartbeat(queryClient, "d1", 1_000);
    expect(queryClient.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated).toBe(true);
  });

  it("the onboarding gate ignores an answer read before its attempt began", async () => {
    // The shared entry already says ACTIVE — read before onboarding's commit,
    // say. The gate must not take that as this attempt's answer.
    seedDaemonList(queryClient, [{ daemonId: "d1", status: DaemonStatus.ACTIVE }]);
    let resolveRead!: (v: { daemons: Partial<DaemonInfo>[] }) => void;
    listDaemons.mockReturnValue(new Promise((r) => (resolveRead = r)));
    // A tick later, so the seeded answer predates the attempt.
    await new Promise((r) => setTimeout(r, 2));

    render(<DaemonConnectingGate onContinue={vi.fn()} />, { wrapper });
    await settle();
    expect(screen.queryByTestId("daemon-gate-connected")).toBeNull();
    expect(screen.getByTestId("daemon-gate-connecting")).toBeInTheDocument();

    // This attempt's own read answers: still provisioning.
    resolveRead({ daemons: [{ daemonId: "d1", status: DaemonStatus.PENDING }] });
    await settle();
    expect(screen.getByTestId("daemon-gate-connecting")).toBeInTheDocument();
    expect(screen.queryByTestId("daemon-gate-connected")).toBeNull();
  });

  it("a ClearMachineCaches refetches the list", async () => {
    const observer = new QueryObserver(queryClient, daemonListQuery({ staleTime: Infinity }));
    const unsubscribe = observer.subscribe(() => {});
    await settle();
    expect(listDaemons).toHaveBeenCalledTimes(1);

    const { result } = renderHook(() => useClearMachineCaches(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ daemonId: "d1", kinds: ["go_build_cache"] });
    });
    await settle();

    expect(clearMachineCaches).toHaveBeenCalledTimes(1);
    expect(listDaemons).toHaveBeenCalledTimes(2);
    unsubscribe();
  });
});
