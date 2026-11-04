// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";

import { issueKeys } from "@/hooks/observability-queries";

import { linkChatToIssue } from "../linkIssueChat";

describe("recording a chat started from an issue", () => {
  it("links the chat to the issue captured at render and refreshes the timeline, with no hook and no title", async () => {
    const linkIssueChat = vi.fn().mockResolvedValue({ created: true });
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    linkChatToIssue({ linkIssueChat } as never, queryClient, "iss-1")("chat-9");
    expect(linkIssueChat).toHaveBeenCalledWith({ issueId: "iss-1", chatId: "chat-9" });
    await vi.waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: issueKeys.timelines() }));
  });

  it("never throws into the composer when the link is refused, and still refreshes", async () => {
    const linkIssueChat = vi.fn().mockRejectedValue(new Error("merged into another issue"));
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    expect(() => linkChatToIssue({ linkIssueChat } as never, queryClient, "iss-1")("chat-9")).not.toThrow();
    await vi.waitFor(() => expect(invalidate).toHaveBeenCalled());
  });
});
