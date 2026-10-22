/**
 * MobileChatSearchTab — chat title search via the same server-side FTS
 * (`api.chatsV2.search`) desktop's `ChatSearch` history mode uses.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const search = vi.fn();
vi.mock("../../../api/client", () => ({
  api: { chatsV2: { search: (...args: unknown[]) => search(...args) } },
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "proj-1" } }),
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

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({
    data: [
      { id: "c1", title: "Fix login bug", updatedAt: "2024-01-01T00:00:00Z" },
      { id: "c2", title: "Add search screen", updatedAt: "2024-01-02T00:00:00Z" },
    ],
  }),
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
});

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
