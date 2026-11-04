/**
 * BackgroundService and PackageCommandsService — the namespaced pair whose
 * generated files both export useListProcesses / useKillProcess — read and
 * write through their generated keys, and the stream keeps their process
 * lists live:
 *
 *   - a write refetches the mounted list (KillProcess, RunCommand), so the
 *     status shown is the server's;
 *   - a favorite toggles at once and survives the refetch;
 *   - process_started / _completed / _port_changed patch the cached lists the
 *     components read — the generated entries, not a zustand copy.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { BackgroundProcessStatus } from "@/gen/reliant/v1/common_pb";
import { EntityType, UserUpdateType } from "@/gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "@/types/streaming";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

vi.mock("@/api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { queryClient } from "@/lib/query-client";
import { useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { useBackgroundProcesses, useKillBackgroundProcess } from "../background-queries";
import {
  runCommandRequest,
  useCommandFavorites,
  usePackageProcesses,
  useRunCommand,
  useToggleCommandFavorite,
} from "../package-queries";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

const START = "2026-10-01T10:00:00Z";

function daemon() {
  const background = [{ id: "bg-1", command: "sleep 100", status: BackgroundProcessStatus.RUNNING, startTime: START, workingDir: "/w", sessionId: "s" }];
  const packages: Array<{ id: string; command: string; status: BackgroundProcessStatus; worktreeId: string; startTime: string; daemonId?: string }> = [];
  let favorites = ["npm:dev:"];
  const calls: Record<string, number> = {};
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1);
  const transport = mockTransport({
    "BackgroundService.ListProcesses": () => {
      count("bg.ListProcesses");
      return { processes: background };
    },
    "BackgroundService.KillProcess": (req) => {
      const { processId } = req as { processId: string };
      const process = background.find((p) => p.id === processId);
      if (process) process.status = BackgroundProcessStatus.KILLED;
      return {};
    },
    "PackageCommandsService.ListProcesses": (req) => {
      count("pkg.ListProcesses");
      const { worktreeId } = req as { worktreeId?: string };
      return { processes: packages.filter((p) => !worktreeId || p.worktreeId === worktreeId) };
    },
    "PackageCommandsService.RunCommand": (req) => {
      const { worktreeId, commandName } = req as { worktreeId: string; commandName: string };
      packages.push({ id: `pkg-${packages.length + 1}`, command: `npm run ${commandName}`, status: BackgroundProcessStatus.RUNNING, worktreeId, startTime: START });
      return { processId: `pkg-${packages.length}`, command: commandName, startTime: START };
    },
    "PackageCommandsService.GetCommandFavorites": () => {
      count("pkg.GetCommandFavorites");
      return { commandKeys: favorites };
    },
    "PackageCommandsService.SetCommandFavorite": (req) => {
      const { commandKey, isFavorite } = req as { commandKey: string; isFavorite: boolean };
      favorites = isFavorite ? [...favorites, commandKey] : favorites.filter((k) => k !== commandKey);
      return {};
    },
  });
  return { transport, calls: (name: string) => calls[name] ?? 0, packages };
}

function processUpdate(type: UserUpdateType, data: Record<string, unknown>, seq: number): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    project_id: "p1",
    worktree_id: "wt-1",
    update_type: type,
    entity_type: EntityType.BACKGROUND_PROCESS,
    entity_id: String(data.process_id),
    data,
    created_at: new Date().toISOString(),
  };
}

describe("BackgroundService + PackageCommandsService keys", () => {
  it("killing a background process refetches the mounted list", async () => {
    const { transport, calls } = daemon();
    setTransport(transport);

    const list = renderHook(() => useBackgroundProcesses(), { wrapper });
    await waitFor(() => expect(list.result.current.data.map((p) => p.status)).toEqual([BackgroundProcessStatus.RUNNING]));

    const kill = renderHook(() => useKillBackgroundProcess(), { wrapper });
    await kill.result.current.mutateAsync({ processId: "bg-1" });

    await waitFor(() => expect(list.result.current.data[0]?.status).toBe(BackgroundProcessStatus.KILLED));
    expect(calls("bg.ListProcesses")).toBe(2);
  });

  it("running a command refetches the mounted package process lists", async () => {
    const { transport, calls } = daemon();
    setTransport(transport);

    const all = renderHook(() => usePackageProcesses(), { wrapper });
    const mine = renderHook(() => usePackageProcesses("wt-1"), { wrapper });
    await waitFor(() => {
      expect(all.result.current.isSuccess).toBe(true);
      expect(mine.result.current.isSuccess).toBe(true);
    });

    const run = renderHook(() => useRunCommand(), { wrapper });
    const result = await run.result.current.mutateAsync(runCommandRequest({ worktreeId: "wt-1", commandName: "dev", packageType: "npm" }));
    expect(result.processId).toBe("pkg-1");

    await waitFor(() => {
      expect(all.result.current.data.map((p) => p.id)).toEqual(["pkg-1"]);
      expect(mine.result.current.data.map((p) => p.worktree_id)).toEqual(["wt-1"]);
    });
    expect(calls("pkg.ListProcesses")).toBe(4);
  });

  it("a favorite toggles at once, then the refetch agrees", async () => {
    const { transport, calls } = daemon();
    setTransport(transport);

    const favorites = renderHook(() => useCommandFavorites("p1"), { wrapper });
    await waitFor(() => expect([...favorites.result.current.data]).toEqual(["npm:dev:"]));

    const toggle = renderHook(() => useToggleCommandFavorite(), { wrapper });
    act(() => toggle.result.current.mutate({ projectId: "p1", commandKey: "make:test:", isFavorite: true }));

    await waitFor(() => expect(favorites.result.current.data.has("make:test:")).toBe(true));
    await waitFor(() => expect(calls("pkg.GetCommandFavorites")).toBe(2));
    expect([...favorites.result.current.data].sort()).toEqual(["make:test:", "npm:dev:"]);
  });

  it("the stream's process events patch the cached background and package lists", async () => {
    const { transport, calls } = daemon();
    setTransport(transport);

    const background = renderHook(() => useBackgroundProcesses(), { wrapper });
    const packages = renderHook(() => usePackageProcesses("wt-1"), { wrapper });
    await waitFor(() => {
      expect(background.result.current.data).toHaveLength(1);
      expect(packages.result.current.isSuccess).toBe(true);
    });

    act(() =>
      useGlobalUpdatesStore.getState().handleUpdate([
        processUpdate(UserUpdateType.PROCESS_STARTED, { process_id: "p-new", command: "npm run dev", working_dir: "/w", start_time: START, worktree_id: "wt-1", daemon_id: "machine-a" }, 1),
      ]),
    );
    await waitFor(() => {
      expect(background.result.current.data.map((p) => p.id)).toContain("p-new");
      expect(packages.result.current.data.map((p) => p.id)).toEqual(["p-new"]);
      expect(packages.result.current.data[0]?.daemon_id).toBe("machine-a");
    });

    act(() =>
      useGlobalUpdatesStore.getState().handleUpdate([
        processUpdate(UserUpdateType.PROCESS_PORT_CHANGED, { process_id: "p-new", ports: [{ port: 5173, protocol: "tcp", state: "listen", address: "127.0.0.1" }] }, 2),
        processUpdate(UserUpdateType.PROCESS_COMPLETED, { process_id: "p-new", command: "npm run dev", working_dir: "/w", status: "completed", exit_code: 0, start_time: START, end_time: START, worktree_id: "wt-1" }, 3),
      ]),
    );
    await waitFor(() => {
      const started = background.result.current.data.find((p) => p.id === "p-new");
      expect(started?.status).toBe(BackgroundProcessStatus.COMPLETED);
      // The completion event names no ports, so the ones the port event set stay.
      expect(started?.ports?.map((p) => p.port)).toEqual([5173]);
      expect(packages.result.current.data[0]?.status).toBe(BackgroundProcessStatus.COMPLETED);
    });
    // Patched in place: the stream refetched nothing.
    expect(calls("bg.ListProcesses")).toBe(1);
    expect(calls("pkg.ListProcesses")).toBe(1);
  });

  it("a process first seen on the stream with no machine is placed by the server, not on the default machine", async () => {
    const { transport, calls, packages: server } = daemon();
    setTransport(transport);

    const all = renderHook(() => usePackageProcesses(), { wrapper });
    const mine = renderHook(() => usePackageProcesses("wt-1"), { wrapper });
    await waitFor(() => {
      expect(all.result.current.isSuccess).toBe(true);
      expect(mine.result.current.isSuccess).toBe(true);
    });

    // The process runs on machine B; the event that announces it says nothing
    // about where. A row without daemon_id would read its logs and kill it on
    // the default machine.
    server.push({ id: "p-b", command: "npm run dev", status: BackgroundProcessStatus.RUNNING, worktreeId: "wt-1", startTime: START, daemonId: "machine-b" });
    act(() =>
      useGlobalUpdatesStore.getState().handleUpdate([
        processUpdate(UserUpdateType.PROCESS_STARTED, { process_id: "p-b", command: "npm run dev", working_dir: "/w", start_time: START, worktree_id: "wt-1" }, 1),
      ]),
    );

    await waitFor(() => {
      expect(all.result.current.data.map((p) => [p.id, p.daemon_id])).toEqual([["p-b", "machine-b"]]);
      expect(mine.result.current.data.map((p) => [p.id, p.daemon_id])).toEqual([["p-b", "machine-b"]]);
    });
    // Both lists were refetched rather than patched with an unplaced row.
    expect(calls("pkg.ListProcesses")).toBe(4);
  });
});
