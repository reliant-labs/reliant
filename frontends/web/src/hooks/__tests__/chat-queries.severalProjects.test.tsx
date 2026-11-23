/**
 * The chat list of several projects (the chat filter, lib/chatProjectFilter):
 * one cache entry per set, ListChats' `project_ids`, an older server's answer
 * narrowed, and the stream's writes reaching it.
 */
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import type { Chat } from "@/api/client";
import type { ListChatsRequest } from "@/gen/reliant/v1/chat_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";

import { chatKeys, getCachedMainWorktree, upsertChatInListCache, useChatListWithWorkspaces, type ChatListEnvelope } from "../chat-queries";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

let requests: ListChatsRequest[] = [];

beforeEach(() => {
  requests = [];
  setTransport(
    mockTransport({
      // An older server: it ignores project_ids and answers every project.
      "ChatService.ListChats": (req) => {
        requests.push(req as ListChatsRequest);
        return {
          chats: ["a", "b", "c"].map((id) => ({ id: `chat-${id}`, title: id, projectId: id })),
          mainWorktrees: ["a", "b", "c"].map((id) => ({ id: `wt-${id}`, projectId: id, isMain: true })),
        };
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

describe("the chat list of several projects", () => {
  it("asks for the projects in one request, and lists only them even from a server that answers every project", async () => {
    const view = renderHook(() => useChatListWithWorkspaces(["c", "a"]), { wrapper });
    await waitFor(() => expect(view.result.current.data?.chats.map((c) => c.projectId)).toEqual(["a", "c"]));
    expect([...requests[0].projectIds].sort()).toEqual(["a", "c"]);
    expect(requests[0].projectId).toBe("");
    expect(view.result.current.data?.mainWorktrees.map((w) => w.projectId)).toEqual(["a", "c"]);
    // One entry per set, whatever the order.
    expect(queryClient.getQueryData(chatKeys.list("projects:a,c"))).toBeDefined();
    expect(getCachedMainWorktree("c")?.id).toBe("wt-c");
  });

  it("one project in a list is that project's list", async () => {
    const view = renderHook(() => useChatListWithWorkspaces(["b"]), { wrapper });
    await waitFor(() => expect(view.result.current.data).toBeDefined());
    expect(requests[0]).toMatchObject({ projectId: "b", projectIds: [] });
  });

  it("a new chat goes into each loaded list of several that holds its project, and no other", () => {
    const empty: ChatListEnvelope = { chats: [], lastUserUpdateSequence: 0, mainWorktrees: [] };
    queryClient.setQueryData(chatKeys.list("projects:a,b", null), empty);
    queryClient.setQueryData(chatKeys.list("projects:c,d", null), empty);
    upsertChatInListCache("a", { id: "new", projectId: "a", title: "new" } as Chat, null);
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("projects:a,b", null))?.chats.map((c) => c.id)).toEqual(["new"]);
    expect(queryClient.getQueryData<ChatListEnvelope>(chatKeys.list("projects:c,d", null))?.chats).toEqual([]);
  });
});
