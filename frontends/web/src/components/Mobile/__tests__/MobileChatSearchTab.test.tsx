/**
 * MobileChatSearchTab — chat title search via the same server-side FTS
 * (ChatService.SearchChats, useChatSearch) desktop's `ChatSearch` history
 * mode uses.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { SearchChatsRequest } from "@/gen/reliant/v1/chat_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

/** The fake server: `search(projectId, query)` answers the matching chats. */
const search = vi.fn();

// The chat list's scope (the /m/projects filter): one project here.
vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: (selector: (s: unknown) => unknown) => selector({ projectFilterId: "proj-1" }),
}));

// The real Link needs a mounted router; this test only cares about the
// resolved href, matching the pattern in MobileDaemonScreen.test.tsx.
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    params,
    ...props
  }: {
    children?: React.ReactNode;
    to: string;
    params?: Record<string, string>;
  }) => {
    const href = params
      ? Object.entries(params).reduce(
          (path, [key, value]) => path.replace(`$${key}`, value),
          to,
        )
      : to;
    return (
      <a href={href} {...props}>
        {children}
      </a>
    );
  },
}));

const LISTED = {
  chats: [
    { id: "c1", title: "Fix login bug", updatedAt: "2024-01-01T00:00:00Z" },
    { id: "c2", title: "Add search screen", updatedAt: "2024-01-02T00:00:00Z" },
  ],
  mainWorktrees: [],
};
vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({ data: LISTED }),
}));

const { MobileChatSearchTab } = await import("../MobileChatSearchTab");

function renderTab(query: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MobileChatSearchTab query={query} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  search.mockReset();
  setTransport(
    mockTransport({
      "ChatService.SearchChats": async (request: unknown) => {
        const { projectId, query } = request as SearchChatsRequest;
        return { chats: (await search(projectId, query)) ?? [] };
      },
    }),
  );
});

afterEach(() => setTransport(null));

describe("MobileChatSearchTab", () => {
  it("shows recent chats when the query is empty", async () => {
    renderTab("");
    expect(await screen.findByText("Add search screen")).toBeInTheDocument();
    expect(screen.getByText("Fix login bug")).toBeInTheDocument();
  });

  it("calls the server-side search for a non-empty query", async () => {
    search.mockResolvedValue([
      { id: "c1", title: "Fix login bug", updatedAt: "2024-01-01T00:00:00Z" },
    ]);
    renderTab("login");
    await waitFor(() => expect(search).toHaveBeenCalledWith("proj-1", "login"));
    expect(await screen.findByText("Fix login bug")).toBeInTheDocument();
  });

  it("links each result to its chat", async () => {
    renderTab("");
    const link = await screen.findByText("Fix login bug");
    expect(link.closest("a")).toHaveAttribute("href", "/m/chats/c1");
  });

  it("shows a no-results message for an empty search", async () => {
    search.mockResolvedValue([]);
    renderTab("nonexistent");
    expect(await screen.findByText(/no chats found/i)).toBeInTheDocument();
  });
});
