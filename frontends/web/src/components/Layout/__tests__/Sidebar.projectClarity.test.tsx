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

// A person whose chats can run (onboarded, with a machine): the chat list, not the Set up chats card.
vi.mock("../../OnboardingFlow/useChatSetup", () => ({ useChatSetup: () => "ready" }));
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
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
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
import { beforeEach as shellBeforeEach } from "vitest";
import { ALL_PROJECTS_PICK, useShellLastPick } from "../sidebar/shellProject";

// The project shell (U6b): these tests read the list across every project, as "All projects" shows it, on the chat home where Chats are open.
shellBeforeEach(() => useShellLastPick.setState({ key: ALL_PROJECTS_PICK }));

function renderSidebar() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <Sidebar pathname="/" />
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
  it("heads every project's chats with \"project · branch\", and marks the open chat on its row, not its project", () => {
    renderSidebar();
    const api = section("api");
    const web = section("web");
    expect(chatIn(api)).toEqual(["api-1", "api-2"]);
    expect(chatIn(web).sort()).toEqual(["web-branch", "web-main"]);

    // One small heading per workspace (U6d): the project, then the branch.
    const apiHeading = screen.getByTestId("sidebar-project-header-p-api-heading");
    expect(apiHeading).toHaveTextContent(/api\s*·\s*main/);
    expect(within(apiHeading).getByRole("button", { expanded: true })).toBeInTheDocument();
    // Pinned while its chats scroll under it.
    expect(apiHeading).toHaveClass("sticky");
    const open = within(section("web")).getAllByRole("button").find((b) => b.getAttribute("aria-current") === "true");
    expect(open?.closest("[data-chat-id]")).not.toBeNull();
  });

  it("lists every chat at the list's one left edge, under its heading (no deep indent)", () => {
    renderSidebar();
    const api = section("api");
    expect(within(api).getByText("main")).toBeInTheDocument();
    for (const row of api.querySelectorAll("[data-chat-row]")) expect(row.className).toContain("pl-2");
  });

  it("keeps branch workspaces as labels under the project when it has them", () => {
    renderSidebar();
    const web = section("web");
    expect(within(web).getByText("feature/login")).toBeInTheDocument();
    expect(within(web).getByText("main")).toBeInTheDocument();
    // A quiet label, not an uppercase heading competing with the project's.
    expect(within(web).getByText("feature/login")).not.toHaveClass("uppercase");
  });

  it("has one project filter, the nav's switcher: the Chats heading has none of its own", () => {
    renderSidebar();
    const heading = screen.getByTestId("sidebar-chats-heading");
    expect(heading).toHaveTextContent(/^Chats/);
    expect(screen.queryByTestId("chat-project-filter-trigger")).toBeNull();
    expect(screen.queryByTestId("project-filter-label")).toBeNull();
    expect(screen.queryByTestId("project-filter-clear")).toBeNull();
  });

  it("names no project on rows when the list is one project's", () => {
    useChatListPreferencesStore.setState({ viewMode: "flat" });
    useWorkspaceStateStore.setState({ projectFilterId: "p-web" });
    renderSidebar();
    expect(screen.queryByTestId("chat-project-web-main")).toBeNull();
  });

  it("puts a needs-you dot on the project of a chat that waits on the user", () => {
    useActivityStore.setState({ activities: new Map([["api-2", ChatActivity.AWAITING_INPUT]]) });
    renderSidebar();
    expect(screen.getByTestId("sidebar-project-needs-you-p-api")).toHaveAccessibleName("A chat needs you");
    expect(screen.queryByTestId("sidebar-project-needs-you-p-web")).toBeNull();
  });

  it("groups archived chats by project across projects, instead of one merged \"main\"", async () => {
    renderSidebar();
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(await screen.findByRole("menuitemcheckbox", { name: "Archived chats" }));
    expect(await screen.findByText("api")).toBeInTheDocument();
    expect(screen.getByText("web")).toBeInTheDocument();
    expect(screen.queryByText("main")).toBeNull();
  });
});
