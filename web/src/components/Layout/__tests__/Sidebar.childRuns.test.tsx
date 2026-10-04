// Copyright (c) 2025 Reliant Labs

/**
 * Decision 4's cost guard: the child-runs query belongs to the OPEN chat's
 * header, never to sidebar rows. A sidebar of N chats must not issue N
 * ListRuns({parent_chat_id}) calls.
 *
 * The real Sidebar renders three chats, one of them selected; the RPC client
 * is the only boundary mocked, so any component the sidebar renders that
 * reaches for child runs would show up as a ListRuns call here.
 */

import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatState } from "../../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../../store/chatStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useProcessStore } from "../../../store/processStore";
import { useProjectStore } from "../../../store/projectStore";

vi.mock("../../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("../../ui/ContextMenu", () => ({ ContextMenu: () => null }));
vi.mock("../../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));

const listRuns = vi.fn(async () => ({ runs: [], nextPageToken: "" }));
vi.mock("../../../api/grpc-client", async () => {
  const actual = await vi.importActual<typeof import("../../../api/grpc-client")>("../../../api/grpc-client");
  return {
    ...actual,
    grpcClient: new Proxy(actual.grpcClient, {
      get(target, prop) {
        if (prop === "run") return () => ({ listRuns });
        return Reflect.get(target, prop);
      },
    }),
  };
});

const chats = ["chat-1", "chat-2", "chat-3"].map((id) => ({
  id,
  title: `Chat ${id}`,
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
  unread: false,
  state: ChatState.ACTIVE,
  worktreeId: "worktree-1",
  projectId: "project-1",
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: chats, isLoading: false }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));
vi.mock("../../../hooks/message-queries", () => ({ useMarkUnread: () => ({ mutateAsync: vi.fn() }) }));

import { Sidebar } from "../Sidebar";

describe("Sidebar and child runs", () => {
  beforeEach(() => {
    listRuns.mockClear();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
    useChatStore.setState({ activeChatId: "chat-1", selectChat: vi.fn() } as Partial<
      ReturnType<typeof useChatStore.getState>
    >);
    useWorktreeStore.setState({
      worktrees: [
        {
          id: "worktree-1",
          name: "Workspace",
          path: "/tmp/worktree",
          branch: "feature/test",
          base_branch: "main",
          project_id: "project-1",
          status: WorktreeStatus.ACTIVE,
          is_main: false,
          created_at: "2024-01-01T00:00:00.000Z",
          updated_at: "2024-01-01T00:00:00.000Z",
          last_active: "2024-01-01T00:00:00.000Z",
        },
      ],
      currentWorktree: null,
      switchWorktreeContext: vi.fn(async () => undefined),
    } as Partial<ReturnType<typeof useWorktreeStore.getState>>);
    useProcessStore.setState({ fetchProcesses: vi.fn() } as Partial<ReturnType<typeof useProcessStore.getState>>);
    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        worktree_count: 1,
        created_at: "2024-01-01T00:00:00.000Z",
        updated_at: "2024-01-01T00:00:00.000Z",
        last_active: "2024-01-01T00:00:00.000Z",
      },
    } as Partial<ReturnType<typeof useProjectStore.getState>>);
  });

  it("renders every row without asking for any chat's child runs", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <Sidebar />
      </QueryClientProvider>,
    );
    for (const chat of chats) expect(await screen.findByText(chat.title)).toBeInTheDocument();
    // Let any mounted queries fire.
    await new Promise((resolve) => setTimeout(resolve, 50));

    const childQueries = listRuns.mock.calls.filter(
      (call) => (call as unknown as [{ parentChatId?: string }])[0]?.parentChatId,
    );
    expect(childQueries).toEqual([]);
  });
});
