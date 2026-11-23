// Copyright (c) 2025 Reliant Labs

/**
 * "Shared with the org" on every tab of the chat list: the shared list is
 * not filtered by the viewer's archive, so it shows under the archived chats
 * as well as the active ones (control-plane docs/design/chat-collaboration.md
 * §13, C0). The section itself (its rows, its count) has its own tests
 * (Sharing/__tests__/SharedWithOrgSection).
 */

import { render, screen } from "@testing-library/react";
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
vi.mock("../../Sharing/SharedWithOrgSection", () => ({
  SharedWithOrgSection: () => <section data-testid="shared-with-org" />,
}));

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

describe("Sidebar: shared with the org", () => {
  it("shows the shared chats on the active tab and on the archived tab", async () => {
    renderSidebar();
    expect(screen.getByRole("region", { name: "Chats" })).toContainElement(screen.getByTestId("shared-with-org"));
    await userEvent.click(screen.getByTestId("project-filter-trigger"));
    await userEvent.click(await screen.findByRole("menuitemcheckbox", { name: "Archived chats" }));
    const archived = await screen.findByRole("region", { name: "Archived chats" });
    expect(archived).toContainElement(screen.getByTestId("shared-with-org"));
  });
});
