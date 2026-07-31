import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { renderHook, cleanup } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import type { Chat } from "../../types/chat";

// The read hooks now delegate to the React Query cache. The stream write path
// (globalUpdatesStore) pulls in the streaming service + OS-notification libs at
// module scope, but these tests never import it — they exercise the hooks
// against the shared query-client singleton directly, which is exactly what
// patchChatCaches / seedChatDetail write to in production.

import { chatKeys, patchChatCaches, seedChatDetail } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";
import { useChatStore } from "../chatStore";
import { useActiveChat, useChat } from "../chatStoreHooks";

const now = "2026-01-01T00:00:00.000Z";

function buildChat(overrides: Partial<Chat>): Chat {
  return {
    id: "c1",
    userId: "user-1",
    title: "Old",
    projectId: "p1",
    worktreeId: "wt-1",
    state: ChatState.IDLE,
    createdAt: now,
    updatedAt: now,
    lastActive: now,
    selectedPresets: {},
    needsRecovery: false,
    activity: 0,
    unread: false,
    ...overrides,
  } as Chat;
}

function wrapper({ children }: { children: ReactNode }) {
  return React.createElement(QueryClientProvider, { client: queryClient }, children);
}

beforeEach(() => {
  queryClient.clear();
  useChatStore.setState({ activeChatId: null });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  useChatStore.setState({ activeChatId: null });
});

describe("useChat (React-Query-sourced)", () => {
  it("returns undefined for an unknown chat", () => {
    const { result } = renderHook(() => useChat("missing"), { wrapper });
    expect(result.current).toBeUndefined();
  });

  it("returns undefined when chatId is undefined", () => {
    const { result } = renderHook(() => useChat(undefined), { wrapper });
    expect(result.current).toBeUndefined();
  });

  it("observes a chat seeded into the detail cache", () => {
    seedChatDetail(buildChat({ id: "c1", worktreeId: "wt-9" }));
    const { result } = renderHook(() => useChat("c1"), { wrapper });
    expect(result.current?.worktreeId).toBe("wt-9");
  });

  it("re-renders when patchChatCaches updates the detail cache", () => {
    queryClient.setQueryData(chatKeys.list("p1"), {
      chats: [buildChat({ id: "c1" })],
      total: 1,
      lastUserUpdateSequence: 1,
    });
    seedChatDetail(buildChat({ id: "c1", title: "Old" }));

    const { result, rerender } = renderHook(() => useChat("c1"), { wrapper });
    expect(result.current?.title).toBe("Old");

    patchChatCaches("p1", "c1", { title: "New" });
    rerender();
    expect(result.current?.title).toBe("New");
  });
});

describe("useActiveChat (React-Query-sourced)", () => {
  it("returns null when there is no active chat id", () => {
    const { result } = renderHook(() => useActiveChat(), { wrapper });
    expect(result.current).toBeNull();
  });

  it("returns the chat for the active id from the detail cache", () => {
    seedChatDetail(buildChat({ id: "c1", title: "Active" }));
    useChatStore.setState({ activeChatId: "c1" });

    const { result } = renderHook(() => useActiveChat(), { wrapper });
    expect(result.current?.title).toBe("Active");
  });

  it("returns null when the active chat is not yet in the cache", () => {
    useChatStore.setState({ activeChatId: "c1" });
    const { result } = renderHook(() => useActiveChat(), { wrapper });
    expect(result.current).toBeNull();
  });

  it("reflects a title patch to the active chat", () => {
    seedChatDetail(buildChat({ id: "c1", title: "Active" }));
    useChatStore.setState({ activeChatId: "c1" });

    const { result, rerender } = renderHook(() => useActiveChat(), { wrapper });
    expect(result.current?.title).toBe("Active");

    patchChatCaches(undefined, "c1", { title: "Renamed" });
    rerender();
    expect(result.current?.title).toBe("Renamed");
  });
});