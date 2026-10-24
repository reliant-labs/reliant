/**
 * MCPService reads and writes share the generated key scope: a write
 * refetches the mounted server list (so the settings page shows what the
 * server recorded, not a patched copy), and the stream's `config_health`
 * refetch — a server connecting, failing, or its config changing on disk —
 * invalidates `mcpServiceKeys.all` through globalUpdatesStore.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

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
import { mcpServiceKeys, useUninstallServer } from "../mcp-service-hooks_gen";
import { useMcpRecommended, useMcpServers } from "../mcp-queries";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

function mcpTransport() {
  const installed = ["github", "linear"];
  const calls: Record<string, number> = {};
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1);
  const transport = mockTransport({
    "MCPService.ListServers": () => {
      count("ListServers");
      return { servers: installed.map((name) => ({ name, enabled: true })), total: installed.length };
    },
    "MCPService.ListRecommended": () => {
      count("ListRecommended");
      return { recommended: [{ name: "github", installed: installed.includes("github") }] };
    },
    "MCPService.UninstallServer": (req) => {
      const { name } = req as { name: string };
      installed.splice(installed.indexOf(name), 1);
      return { success: true, name };
    },
  });
  return { transport, calls: (name: string) => calls[name] ?? 0 };
}

function refetch(type: string, seq: number): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    project_id: "p1",
    update_type: UserUpdateType.REFETCH,
    entity_type: EntityType.UNSPECIFIED,
    entity_id: "",
    data: { type },
    created_at: new Date().toISOString(),
  };
}

describe("MCPService keys", () => {
  it("uninstalling a server refetches the mounted server list and recommendations", async () => {
    const { transport, calls } = mcpTransport();
    setTransport(transport);

    const servers = renderHook(() => useMcpServers("p1"), { wrapper });
    const recommended = renderHook(() => useMcpRecommended("p1"), { wrapper });
    await waitFor(() => {
      expect(servers.result.current.data?.map((s) => s.name)).toEqual(["github", "linear"]);
      expect(recommended.result.current.data?.[0]?.installed).toBe(true);
    });

    const uninstall = renderHook(() => useUninstallServer(), { wrapper });
    await uninstall.result.current.mutateAsync({ projectId: "p1", name: "github" });

    await waitFor(() => {
      expect(servers.result.current.data?.map((s) => s.name)).toEqual(["linear"]);
      expect(recommended.result.current.data?.[0]?.installed).toBe(false);
    });
    expect(calls("ListServers")).toBe(2);
    expect(calls("ListRecommended")).toBe(2);
  });

  it("the stream's config_health refetch invalidates mcpServiceKeys.all", () => {
    const listKey = mcpServiceKeys.listServers({ projectId: "p1" });
    const recommendedKey = mcpServiceKeys.listRecommended({ projectId: "p1" });
    queryClient.setQueryData(listKey, { servers: [], total: 0 });
    queryClient.setQueryData(recommendedKey, { recommended: [], total: 0 });

    useGlobalUpdatesStore.getState().handleUpdate([refetch("config_health", 1)]);

    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(recommendedKey)?.isInvalidated).toBe(true);
  });

  it("another refetch type leaves MCP reads alone", () => {
    const listKey = mcpServiceKeys.listServers({ projectId: "p1" });
    queryClient.setQueryData(listKey, { servers: [], total: 0 });

    useGlobalUpdatesStore.getState().handleUpdate([refetch("file_tree", 2)]);

    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
  });
});
