// Copyright (c) 2025 Reliant Labs

/**
 * The sidebar's machine filter (owner: "when selecting all machines in the
 * left filter, I can select which ones I want to show").
 *
 * It narrows the list the project filter scopes, by each chat's machine (its
 * pinned daemon, else the default machine); it is hidden, and ignored, while
 * the user has one machine; and when it hides every chat the list says so and
 * offers the way back.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { ChatActivity, ChatState } from "../../../gen/reliant/v1/chat_pb";
import { DaemonStatus } from "../../../gen/reliant/v1/daemon_registry_pb";
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
]);
vi.mock("../../../hooks/project-queries", async (original) => ({
  ...(await original<typeof import("../../../hooks/project-queries")>()),
  useProjects: () => ({ projects }),
}));

// "laptop" is the default machine; "cloud" a second one.
const machines = vi.hoisted(() => ({
  list: [] as { daemonId: string; hostname: string; name: string; status: number; daemonType: string; isDefault: boolean }[],
}));
vi.mock("../../../hooks/useOnboardingQueries", async (original) => ({
  ...(await original<typeof import("../../../hooks/useOnboardingQueries")>()),
  useDaemonList: () => ({ data: machines.list }),
}));
const LAPTOP = { daemonId: "laptop", hostname: "laptop", name: "Laptop", status: DaemonStatus.ACTIVE, daemonType: "self_hosted", isDefault: true };
const CLOUD = { daemonId: "cloud", hostname: "cloud", name: "Cloud box", status: DaemonStatus.ACTIVE, daemonType: "managed", isDefault: false };

const MAIN = { id: "w-main", name: "main", branch: "main", path: "/p/api", isMain: true, projectId: "p-api", status: WorktreeStatus.ACTIVE };

const chat = (id: string, activeDaemonId?: string) => ({
  id,
  title: `Chat ${id}`,
  projectId: "p-api",
  worktreeId: "",
  activeDaemonId,
  noMachine: false,
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
  unread: false,
  activity: ChatActivity.IDLE,
  workingThreads: 0,
  state: ChatState.IDLE,
});
// Two chats pinned to the cloud box, one unpinned (the default machine's).
const CHATS = [chat("on-cloud-1", "cloud"), chat("on-cloud-2", "cloud"), chat("unpinned")];

vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: CHATS, lastUserUpdateSequence: 0, mainWorktrees: [MAIN] },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
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

function renderSidebar() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <Sidebar />
    </QueryClientProvider>,
  );
}

const listedChats = () =>
  Array.from(document.querySelectorAll("[data-chat-id]")).map((row) => row.getAttribute("data-chat-id"));

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  machines.list = [LAPTOP, CLOUD];
  useChatStore.setState({ activeChatId: null, selectChat: vi.fn() } as Partial<ReturnType<typeof useChatStore.getState>>);
  useWorktreeStore.setState({
    currentWorktreeId: null,
    currentWorktreeProjectId: null,
    switchWorktreeContext: vi.fn(async () => undefined),
  } as Partial<ReturnType<typeof useWorktreeStore.getState>>);
  useProjectStore.setState({ currentProject: null } as Partial<ReturnType<typeof useProjectStore.getState>>);
  useWorkspaceStateStore.setState({ projectFilterId: null, machineFilterIds: null });
  useChatListPreferencesStore.setState({ viewMode: "flat", sortOrder: "alphabetical_asc" });
  useActivityStore.setState({ entries: new Map(), activities: new Map(), workingThreads: new Map(), maxSeenSeq: 0 });
});

describe("Sidebar: the machine filter", () => {
  it("lists every machine's chats by default", () => {
    renderSidebar();
    expect(screen.getByTestId("machine-filter-trigger")).toHaveAccessibleName("Filter by machine. Showing All machines");
    expect(listedChats()).toEqual(["on-cloud-1", "on-cloud-2", "unpinned"]);
  });

  it("lists only the chosen machines' chats, counting an unpinned chat as the default machine's", () => {
    useWorkspaceStateStore.setState({ machineFilterIds: ["laptop"] });
    renderSidebar();
    expect(listedChats()).toEqual(["unpinned"]);
    expect(screen.getByTestId("machine-filter-label")).toHaveTextContent("Laptop");
    expect(screen.getByTestId("machine-filter-hidden")).toHaveTextContent("2 chats on other machines hidden");
  });

  it("persists a choice made in the menu", async () => {
    renderSidebar();
    await userEvent.click(screen.getByTestId("machine-filter-trigger"));
    await userEvent.click(screen.getByRole("button", { name: "Show only Cloud box" }));
    expect(useWorkspaceStateStore.getState().machineFilterIds).toEqual(["cloud"]);
    expect(listedChats()).toEqual(["on-cloud-1", "on-cloud-2"]);
  });

  it("says so when the filter hides every chat, and Show all machines undoes it", async () => {
    machines.list = [LAPTOP, CLOUD, { ...CLOUD, daemonId: "idle-box", name: "Idle box", isDefault: false }];
    useWorkspaceStateStore.setState({ machineFilterIds: ["idle-box"] });
    renderSidebar();
    expect(listedChats()).toEqual([]);
    const empty = screen.getByTestId("machine-filter-empty");
    expect(empty).toHaveTextContent("No active chats on Idle box");
    expect(empty).toHaveTextContent("3 chats on other machines are hidden by the machine filter.");

    await userEvent.click(screen.getByRole("button", { name: "Show all machines" }));
    expect(useWorkspaceStateStore.getState().machineFilterIds).toBeNull();
    expect(listedChats()).toEqual(["on-cloud-1", "on-cloud-2", "unpinned"]);
  });

  it("ignores a persisted machine the user no longer has", () => {
    useWorkspaceStateStore.setState({ machineFilterIds: ["gone"] });
    renderSidebar();
    expect(listedChats()).toEqual(["on-cloud-1", "on-cloud-2", "unpinned"]);
    expect(screen.queryByTestId("machine-filter-label")).toBeNull();
  });

  it("is hidden, and filters nothing, while the user has one machine", () => {
    machines.list = [LAPTOP];
    useWorkspaceStateStore.setState({ machineFilterIds: ["laptop"] });
    renderSidebar();
    expect(screen.queryByTestId("machine-filter-trigger")).toBeNull();
    expect(listedChats()).toEqual(["on-cloud-1", "on-cloud-2", "unpinned"]);
  });
});
