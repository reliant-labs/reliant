// Copyright (c) 2025 Reliant Labs

/**
 * Un-adoption ("Move back to Runs", WORKFLOW_UI.md §6.3) goes through
 * ChatService.UnadoptChat by chat id, and on success the chat list refetches
 * (which is what removes the row), every run read refetches, and the detail
 * cache stops calling it adopted.
 */

import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { chatKeys } from "../chat-queries";
import { queryClient as appQueryClient } from "@/lib/query-client";
import { useUnadoptRun } from "../run-queries";
import { runServiceKeys } from "../run-service-hooks_gen";

afterEach(() => setTransport(null));

describe("useUnadoptRun", () => {
  it("calls UnadoptChat with the chat id, refreshes the chat and run lists, and patches the detail", async () => {
    const unadoptChat = vi.fn((_request: unknown) => ({ chat: { id: "chat-1", launchKind: "schedule" } }));
    setTransport(mockTransport({ "ChatService.UnadoptChat": (request) => unadoptChat(request) }));
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    const { result } = renderHook(() => useUnadoptRun(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      ),
    });

    await act(async () => {
      await result.current.mutateAsync({ chatId: "chat-1" });
    });

    expect(unadoptChat).toHaveBeenCalledWith(expect.objectContaining({ chatId: "chat-1" }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: chatKeys.lists() });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: runServiceKeys.all });
    // seedChatDetail writes the app's shared client, which the sidebar's
    // useChat reads.
    expect(appQueryClient.getQueryData<{ adoptedAt?: string }>(chatKeys.detail("chat-1"))?.adoptedAt).toBeUndefined();
    expect(appQueryClient.getQueryData(chatKeys.detail("chat-1"))).toMatchObject({ id: "chat-1" });
    expect(appQueryClient.getQueryData(chatKeys.detail("chat-1"))).not.toHaveProperty("$typeName");
  });
});
