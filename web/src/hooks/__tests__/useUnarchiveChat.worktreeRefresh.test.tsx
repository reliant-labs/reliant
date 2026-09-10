import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const unarchiveMock = vi.hoisted(() => vi.fn());
const refreshWorktreesMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/client", () => ({
  api: {
    chatsV2: {
      unarchive: unarchiveMock,
    },
  },
}));

vi.mock("../../store/worktreeStore", () => ({
  useWorktreeStore: {
    getState: () => ({
      refreshWorktrees: refreshWorktreesMock,
    }),
  },
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: {
    getState: () => ({
      currentProject: { id: "project-1" },
    }),
  },
}));

import { useUnarchiveChat } from "../chat-queries";

function wrapper({ children }: { children: ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

describe("useUnarchiveChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    unarchiveMock.mockResolvedValue({ chat: { id: "chat-1" } });
    refreshWorktreesMock.mockResolvedValue(undefined);
  });

  // The server unarchives the chat's worktree alongside the chat
  // (chat_crud.go). Worktrees live in Zustand, which React Query
  // invalidation cannot reach — without an explicit refresh the sidebar
  // drops the restored chat because its worktree is still archived locally.
  it("refreshes the worktree store so the restored chat's workspace reappears", async () => {
    const { result } = renderHook(() => useUnarchiveChat(), { wrapper });

    await result.current.mutateAsync("chat-1");

    await waitFor(() => {
      expect(refreshWorktreesMock).toHaveBeenCalledWith("project-1");
    });
  });
});
