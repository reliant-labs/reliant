// Copyright (c) 2025 Reliant Labs

/**
 * Un-adoption ("Move back to Runs", WORKFLOW_UI.md §6.3) goes through
 * ChatService.UnadoptChat by chat id, and on success the chat list refetches
 * (which is what removes the row) and the detail cache stops calling it
 * adopted.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { ChatSchema, UnadoptChatResponseSchema } from "@/gen/reliant/v1/chat_pb";

const unadoptChat = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { chat: () => ({ unadoptChat }) },
}));

import { chatKeys } from "../chat-queries";
import { queryClient as appQueryClient } from "@/lib/query-client";
import { runKeys, useUnadoptRun } from "../run-queries";

describe("useUnadoptRun", () => {
  it("calls UnadoptChat with the chat id, refreshes the chat and run lists, and patches the detail", async () => {
    unadoptChat.mockResolvedValue(
      create(UnadoptChatResponseSchema, {
        chat: create(ChatSchema, { id: "chat-1", launchKind: "schedule" }),
      }),
    );
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    const { result } = renderHook(() => useUnadoptRun(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      ),
    });

    await act(async () => {
      await result.current.mutateAsync("chat-1");
    });

    expect(unadoptChat).toHaveBeenCalledWith({ chatId: "chat-1" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: chatKeys.lists() });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: runKeys.lists() });
    // seedChatDetail writes the app's shared client, which the sidebar's
    // useChat reads.
    expect(appQueryClient.getQueryData<{ adoptedAt?: string }>(chatKeys.detail("chat-1"))?.adoptedAt).toBeUndefined();
    expect(appQueryClient.getQueryData(chatKeys.detail("chat-1"))).toMatchObject({ id: "chat-1" });
  });
});
