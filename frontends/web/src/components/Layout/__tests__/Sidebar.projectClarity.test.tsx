// Copyright (c) 2025 Reliant Labs

/**
 * Which project each chat is in, at a glance (owner, 2026-10-31: "we need to
 * do a better job about showing which chat is in which project").
 *
 * All projects, grouped: each project is a section whose header is the
 * strongest thing in the list — a project mark and its name, pinned while
 * its chats scroll — and a project whose only workspace is main lists its
 * chats straight under that header, with no "MAIN" heading that outweighed
 * the project's. Branch workspaces stay, as quiet labels under the project.
 * Flat and archived lists, which have no project headers, name the project on
 * the row; the archive groups by project across projects instead of merging
 * every project's "main" into one group.
 */

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { ChatActivity, ChatState } from "../../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../../store/chatStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useProjectStore } from "../../../store/projectStore";
import { useActivityStore } from "../../../store/activityStore";
import { useChatListPreferencesStore } from "../../../store/chatListPreferencesStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("../../forge-ui/context_menu", () => ({ default: () => null }));
vi.mock("../../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));

beforeEach(() =>
  setTransport(mockTransport({ "RunService.ListRuns": () => ({ runs: [], nextPageToken: "" }) })),
);
afterEach(() => setTransport(null));

const projects = vi.hoisted(() => [
  { id: "p-api", name: "api", path: "/p/api", last_active: "2024-01-02T00:00:00.000Z", is_git_repo: true, is_forge: false },
  { id: "p-web", name: "web", path: "/p/web", last_active: "2024-01-01T00:00:00.000Z", is_git_repo: true, is_forge: false },
]);
vi.mock("../../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/project-queries")>()),
  useProjects: () => ({ projects }),
}));

const mainOf = (projectId: string) => ({
  id: `w-main-${projectId}`,
  name: "main",
  branch: "main",
  path: `/p/${projectId}`,
  isMain: true,
  projectId,
  status: WorktreeStatus.ACTIVE,
});
const feature = {
  id: "w-web-feature",
  name: "feature",
  branch: "feature/login",
  path: "/p/web-feature",
  isMain: false,
  projectId: "p-web",
  status: WorktreeStatus.ACTIVE,
};
const MAIN_WORKTREES = [mainOf("p-api"), mainOf("p-web")];

const chat = (id: string, projectId: string, worktree?: typeof feature) => ({
  id,
  title: `Chat ${id}`,
  projectId,
  worktree,
  worktreeId: worktree?.id ?? "",
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
  unread: false,
  activity: ChatActivity.IDLE,
  workingThreads: 0,
  state: ChatState.IDLE,
});
// api: two chats on main only. web: one on main, one on a branch workspace.
const CHATS = [chat("api-1", "p-api"), chat("api-2", "p-api"), chat("web-main", "p-web"), chat("web-branch", "p-web", feature)];
const ARCHIVED = [
  { ...chat("api-old", "p-api"), state: ChatState.ARCHIVED, worktreeName: "main" },
  { ...chat("web-old", "p-web"), state: ChatState.ARCHIVED, worktreeName: "main" },
];

vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: CHATS, lastUserUpdateSequence: 0, mainWorktrees: MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: ARCHIVED, isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));
vi.mock("../../../hooks/inbox-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/inbox-queries")>("../../../hooks/inbox-queries");
  return {
    ...actual,
    useInboxCounts: () => ({ data: { blockingCount: 0, hasInformational: false }, isError: false }),
    useInboxProjectId: () => undefined,
  };
});
vi.mock("../../../hooks/message-queries", () => ({ useMarkUnread: () => ({ mutateAsync: vi.fn() }) }));

import { Sidebar } from "../Sidebar";

function renderSidebar() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <Sidebar />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  useChatStore.setState({ activeChatId: "web-branch", selectChat: vi.fn() } as Partial<
    ReturnType<typeof useChatStore.getState>
  >);
  useWorktreeStore.setState({
    currentWorktreeId: null,
    currentWorktreeProjectId: null,
    switchWorktreeContext: vi.fn(async () => undefined),
  } as Partial<ReturnType<typeof useWorktreeStore.getState>>);
  useProjectStore.setState({ currentProject: null } as Partial<ReturnType<typeof useProjectStore.getState>>);
  useWorkspaceStateStore.setState({ projectFilterId: null });
  useChatListPreferencesStore.setState({ viewMode: "grouped", sortOrder: "alphabetical_asc" });
  useActivityStore.setState({ entries: new Map(), activities: new Map(), workingThreads: new Map(), maxSeenSeq: 0 });
});

const section = (name: string) => screen.getByRole("region", { name });
const chatIn = (region: HTMLElement) =>
  Array.from(region.querySelectorAll("[data-chat-id]")).map((row) => row.getAttribute("data-chat-id"));

describe("Sidebar: which project a chat is in", () => {
  it("heads every project's chats with its mark and name, the project holding the open chat in primary", () => {
    renderSidebar();
    const api = section("api");
    const web = section("web");
    expect(chatIn(api)).toEqual(["api-1", "api-2"]);
    expect(chatIn(web).sort()).toEqual(["web-branch", "web-main"]);

    const apiHeader = screen.getByTestId("sidebar-project-header-p-api");
    expect(within(apiHeader).getByTestId("project-mark")).toHaveTextContent("A");
    expect(within(apiHeader).getByRole("button", { name: "api" })).toHaveAttribute("aria-expanded", "true");
    // Pinned while its chats scroll under it.
    expect(apiHeader).toHaveClass("sticky");
    // The open chat is in web.
    expect(within(screen.getByTestId("sidebar-project-header-p-web")).getByTestId("project-mark")).toHaveClass("text-primary");
  });

  it("lists a main-only project's chats straight under the project, with no MAIN heading", () => {
    renderSidebar();
    const api = section("api");
    expect(within(api).queryByText("main")).toBeNull();
    expect(within(api).getByTestId("sidebar-workspace-chats")).toBeInTheDocument();
  });

  it("keeps branch workspaces as labels under the project when it has them", () => {
    renderSidebar();
    const web = section("web");
    expect(within(web).getByText("feature/login")).toBeInTheDocument();
    expect(within(web).getByText("main")).toBeInTheDocument();
    // A quiet label, not an uppercase heading competing with the project's.
    expect(within(web).getByText("feature/login")).not.toHaveClass("uppercase");
  });

  it("names the project on each row of the flat list, and the branch only off main", () => {
    useChatListPreferencesStore.setState({ viewMode: "flat" });
    renderSidebar();
    expect(screen.getByTestId("chat-project-api-1")).toHaveTextContent("api");
    expect(screen.getByTestId("chat-project-web-branch")).toHaveTextContent("web");
    const branchRow = document.querySelector('[data-chat-id="web-branch"]') as HTMLElement;
    expect(branchRow).toHaveTextContent("feature/login");
    const mainRow = document.querySelector('[data-chat-id="web-main"]') as HTMLElement;
    expect(within(mainRow).queryByText("main")).toBeNull();
  });

  it("names no project on rows when the list is one project's", () => {
    useChatListPreferencesStore.setState({ viewMode: "flat" });
    useWorkspaceStateStore.setState({ projectFilterId: "p-web" });
    renderSidebar();
    expect(screen.queryByTestId("chat-project-web-main")).toBeNull();
  });

  it("groups archived chats by project across projects, instead of one merged \"main\"", async () => {
    renderSidebar();
    await userEvent.click(screen.getByRole("button", { name: "Sort chats" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Archived chats" }));
    expect(await screen.findByText("api")).toBeInTheDocument();
    expect(screen.getByText("web")).toBeInTheDocument();
    expect(screen.queryByText("main")).toBeNull();
  });
});
