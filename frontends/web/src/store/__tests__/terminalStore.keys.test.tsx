/**
 * Closing a terminal invalidates the generated TerminalService reads, from
 * both paths that close one: the store's killSession (a tab's X button) and
 * the generated useCloseSession hook. Either way a cached ListSessions answer
 * is refetched rather than left listing the session that just closed.
 */
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import {
  terminalServiceKeys,
  useCloseSession,
  useListSessions,
} from "@/hooks/terminal-service-hooks_gen";

vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() },
}));
vi.mock("../workspaceStateStore", () => ({
  useWorkspaceStateStore: { getState: () => ({}) },
}));

import { useTerminalStore } from "../terminalStore";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let open: string[];
let listCalls: number;

beforeEach(() => {
  open = ["daemon-a", "daemon-b"];
  listCalls = 0;
  setTransport(
    mockTransport({
      "TerminalService.ListSessions": () => {
        listCalls++;
        return { sessions: open.map((id) => ({ id })), total: open.length };
      },
      "TerminalService.CloseSession": (req) => {
        const { sessionId } = req as { sessionId: string };
        open = open.filter((id) => id !== sessionId);
        return { success: true, message: "" };
      },
    }),
  );
  useTerminalStore.setState({ sessions: [], activeSessionId: null, activeSessionPerWorktree: {} });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

describe("TerminalService keys", () => {
  it("killSession refetches a cached ListSessions", async () => {
    const list = renderHook(() => useListSessions({}, { staleTime: Infinity }), { wrapper });
    await waitFor(() => expect(list.result.current.data?.sessions).toHaveLength(2));

    const localId = useTerminalStore.getState().createSession("/home/workspace");
    useTerminalStore.getState().setDaemonSessionId(localId, "daemon-a");
    useTerminalStore.getState().killSession(localId);

    await waitFor(() => expect(list.result.current.data?.sessions.map((s) => s.id)).toEqual(["daemon-b"]));
    expect(listCalls).toBe(2);
  });

  it("useCloseSession refetches a cached ListSessions", async () => {
    const list = renderHook(() => useListSessions({}, { staleTime: Infinity }), { wrapper });
    await waitFor(() => expect(list.result.current.isSuccess).toBe(true));

    const close = renderHook(() => useCloseSession(), { wrapper });
    await close.result.current.mutateAsync({ sessionId: "daemon-b" });

    await waitFor(() => expect(list.result.current.data?.sessions.map((s) => s.id)).toEqual(["daemon-a"]));
  });

  it("terminalServiceKeys.all covers every ListSessions entry", async () => {
    const list = renderHook(() => useListSessions({}, { staleTime: Infinity }), { wrapper });
    await waitFor(() => expect(list.result.current.isSuccess).toBe(true));

    await queryClient.invalidateQueries({ queryKey: terminalServiceKeys.all });
    await waitFor(() => expect(listCalls).toBe(2));
  });
});
