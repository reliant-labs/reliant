import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const unarchiveMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/client", () => ({
  api: {
    chatsV2: {
      unarchive: unarchiveMock,
    },
  },
}));

import { useUnarchiveChat, chatKeys } from "../chat-queries";

let queryClient: QueryClient;

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

describe("useUnarchiveChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    unarchiveMock.mockResolvedValue({ chat: { id: "chat-1" } });
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
  });

  // The server unarchives the chat's worktree alongside the chat, and the chat
  // list carries each chat's workspace — so refetching the lists is all the
  // restored chat needs to reappear in the right group. No worktree store is
  // involved.
  it("refetches the chat lists, which carry the restored chat's workspace", async () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useUnarchiveChat(), { wrapper });

    await result.current.mutateAsync("chat-1");

    await waitFor(() => {
      expect(invalidate).toHaveBeenCalledWith({ queryKey: chatKeys.lists() });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: chatKeys.archived() });
    });
  });
});
