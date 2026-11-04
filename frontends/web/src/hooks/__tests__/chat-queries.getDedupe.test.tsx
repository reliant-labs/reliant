import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../chat-actions", async () => {
  const actual = await vi.importActual<typeof import("../chat-actions")>("../chat-actions");
  return { ...actual, getChat: getMock };
});

import type { Chat } from "../../types/chat";
import { queryClient } from "../../lib/query-client";
import {
  refetchChatSelection,
  seedChatDetail,
  useChat,
  useChatSelection,
} from "../chat-queries";

const SELECTION = { workflowName: "agent", version: 3 } as never;

function buildChat(overrides: Partial<Chat> = {}): Chat {
  return { id: "c1", title: "t", selection: SELECTION, ...overrides } as Chat;
}

const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

beforeEach(() => {
  queryClient.clear();
  getMock.mockReset();
  getMock.mockResolvedValue(buildChat());
});

afterEach(() => {
  queryClient.clear();
});

describe("opening a chat reads GetChat once", () => {
  it("does not refetch the chat selectChat just seeded", async () => {
    seedChatDetail(buildChat());

    const { result } = renderHook(
      () => ({ chat: useChat("c1"), selection: useChatSelection("c1") }),
      { wrapper },
    );

    await waitFor(() => expect(result.current.chat.data?.id).toBe("c1"));
    expect(result.current.selection.data).toBe(SELECTION);
    expect(getMock).not.toHaveBeenCalled();
  });

  it("shares one request between the header's chat and the composer's selection on a cold chat", async () => {
    const { result } = renderHook(
      () => ({ chat: useChat("c1"), selection: useChatSelection("c1") }),
      { wrapper },
    );

    await waitFor(() => {
      expect(result.current.chat.data?.id).toBe("c1");
      expect(result.current.selection.data).toBe(SELECTION);
    });
    expect(getMock).toHaveBeenCalledTimes(1);
  });

  it("reads null for a chat that has no selection", async () => {
    getMock.mockResolvedValue(buildChat({ selection: undefined }));
    const { result } = renderHook(() => useChatSelection("c1"), { wrapper });
    await waitFor(() => expect(result.current.data).toBeNull());
    expect(getMock).toHaveBeenCalledTimes(1);
  });

  it("does not refetch the chat on a remount while it is fresh", async () => {
    const first = renderHook(() => useChat("c1"), { wrapper });
    await waitFor(() => expect(first.result.current.data?.id).toBe("c1"));
    first.unmount();

    const second = renderHook(() => useChat("c1"), { wrapper });
    await waitFor(() => expect(second.result.current.data?.id).toBe("c1"));
    expect(getMock).toHaveBeenCalledTimes(1);
  });

  it("refetchChatSelection still reaches the server past a fresh detail", async () => {
    const { result } = renderHook(() => useChatSelection("c1"), { wrapper });
    await waitFor(() => expect(result.current.data).toBe(SELECTION));
    expect(getMock).toHaveBeenCalledTimes(1);

    const newer = { workflowName: "agent", version: 4 } as never;
    getMock.mockResolvedValue(buildChat({ selection: newer }));
    await refetchChatSelection("c1");

    expect(getMock).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(result.current.data).toEqual(newer));
  });
});
