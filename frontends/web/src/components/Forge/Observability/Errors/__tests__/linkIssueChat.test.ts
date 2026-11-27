// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { issueKeys } from "@/hooks/observability-queries";
import { mockTransport } from "@/lib/test-utils";
import { createTransportScope } from "@/lib/transportScope";

import { linkChatToIssue } from "../linkIssueChat";

/** A QueryClient whose transport answers LinkIssueChat with `link`: the write runs on the client it is handed. */
function clientAnswering(link: (req: { issueId: string; chatId: string; chatTitle: string }) => unknown) {
  const queryClient = new QueryClient();
  createTransportScope({ id: "link-issue-chat", queryClient, transport: mockTransport({ [`${IssuesService.typeName}/LinkIssueChat`]: link } as Parameters<typeof mockTransport>[0]) });
  return queryClient;
}

describe("recording a chat started from an issue", () => {
  it("links the chat to the issue captured at render and refreshes the timeline, with no hook and no title", async () => {
    const link = vi.fn(async (req: { issueId: string }) => ({ link: { issueId: req.issueId, chatId: "chat-9" }, created: true }));
    const queryClient = clientAnswering(link);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    linkChatToIssue(queryClient, "iss-1")("chat-9");
    await vi.waitFor(() => expect(link).toHaveBeenCalledWith(expect.objectContaining({ issueId: "iss-1", chatId: "chat-9", chatTitle: "" })));
    await vi.waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: issueKeys.timelines() }));
  });

  it("never throws into the composer when the link is refused, and still refreshes", async () => {
    const queryClient = clientAnswering(async () => { throw new ConnectError("merged into another issue", Code.FailedPrecondition); });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    expect(() => linkChatToIssue(queryClient, "iss-1")("chat-9")).not.toThrow();
    await vi.waitFor(() => expect(invalidate).toHaveBeenCalled());
  });
});
