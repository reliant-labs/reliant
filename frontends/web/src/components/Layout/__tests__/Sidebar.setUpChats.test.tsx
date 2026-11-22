/**
 * The nav's Chats section for a person who cannot run a chat yet (U6, the
 * daemonless home): the connect-a-sandbox call to action in place of the list (U6d), linking to
 * the step that fixes it, and New chat going there too. A person who can run
 * chats — or whose setup is still being checked, or failed to load — keeps
 * the list: no card on a guess.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ChatSetup } from "../../OnboardingFlow/onboardingGate";

const state = vi.hoisted(() => ({ setup: "ready" as string, openNewChat: vi.fn() }));

vi.mock("../../OnboardingFlow/useChatSetup", () => ({ useChatSetup: () => state.setup }));
vi.mock("../../../store/openChat", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../store/openChat")>()),
  openNewChat: state.openNewChat,
}));
vi.mock("../../../hooks/useOnboardingQueries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/useOnboardingQueries")>()),
  useDaemonList: () => NO_MACHINES,
}));
const NO_MACHINES = { data: [] };
vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("../../forge-ui/context_menu", () => ({ default: () => null }));
vi.mock("../../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));

const NO_MAIN_WORKTREES: never[] = [];
const CHATS = [
  {
    id: "chat-1",
    title: "Fix the build",
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    unread: false,
    worktreeId: "w-1",
    projectId: "project-1",
  },
];
vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: CHATS, lastUserUpdateSequence: 0, mainWorktrees: NO_MAIN_WORKTREES },
    isLoading: false,
  }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  usePermanentlyDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
  useChat: () => ({ data: undefined }),
}));
vi.mock("../../../hooks/inbox-queries", () => ({
  useInboxCounts: () => ({ data: { blockingCount: 0, hasInformational: false }, isError: false }),
  useInboxProjectId: () => undefined,
}));
vi.mock("../../../hooks/run-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/run-queries")>()),
  useLiveRuns: () => ({ data: undefined }),
}));
vi.mock("../../../hooks/message-queries", () => ({ useMarkUnread: () => ({ mutateAsync: vi.fn() }) }));

import { Sidebar } from "../Sidebar";
import { beforeEach as shellBeforeEach } from "vitest";
import { ALL_PROJECTS_PICK, useShellLastPick } from "../sidebar/shellProject";

// The project shell (U6b): these tests read the list across every project, as "All projects" shows it, on the chat home where Chats are open.
shellBeforeEach(() => useShellLastPick.setState({ key: ALL_PROJECTS_PICK }));

function renderSidebar() {
  const onNavigate = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <Sidebar pathname="/" onNavigate={onNavigate} />
    </QueryClientProvider>,
  );
  return onNavigate;
}

beforeEach(() => {
  state.setup = "ready";
  state.openNewChat.mockReset();
  state.openNewChat.mockResolvedValue(undefined);
});

describe("the nav's Chats section with no usable machine", () => {
  it.each([
    ["onboard", "/onboarding"],
    ["connect-machine", "/settings/machines"],
  ] as const)("is the connect-a-sandbox call to action for %s; New chat goes to %s", (setup: ChatSetup, href) => {
    state.setup = setup;
    const onNavigate = renderSidebar();

    const card = screen.getByTestId("connect-sandbox-cta");
    expect(card).toHaveTextContent("Connect an agent sandbox to start chats");
    expect(card).toHaveAttribute("data-variant", "nav");
    // Both ways open the one Add sandbox sheet, in place (Machines/AddMachineSheet).
    expect(within(card).getByTestId("connect-sandbox-own")).toHaveTextContent("Use your own machine");
    expect(within(card).getByTestId("connect-sandbox-cloud")).toHaveTextContent("Provision a Reliant sandbox");
    // The list is gone, chats and all.
    expect(screen.queryByTestId("sidebar-scroller")).toBeNull();
    expect(screen.queryByText("Fix the build")).toBeNull();

    // New chat stays, and goes where the card does instead of opening a chat:
    // its link says so too.
    const newChat = screen.getByRole("link", { name: "New chat" });
    expect(newChat).toHaveAttribute("href", href);
    fireEvent.click(newChat);
    expect(onNavigate).toHaveBeenLastCalledWith(href);
    expect(state.openNewChat).not.toHaveBeenCalled();
  });

  it.each(["ready", "checking", "unknown"] as const)("is the chat list for %s, and New chat opens a chat", (setup) => {
    state.setup = setup;
    const onNavigate = renderSidebar();

    expect(screen.queryByTestId("connect-sandbox-cta")).toBeNull();
    expect(screen.getByTestId("sidebar-scroller")).toBeInTheDocument();
    const newChat = screen.getByRole("link", { name: "New chat" });
    // A link to U5's new-chat view; a plain click runs openNewChat.
    expect(newChat).toHaveAttribute("href", "/new");
    fireEvent.click(newChat);
    expect(state.openNewChat).toHaveBeenCalledTimes(1);
    expect(onNavigate).not.toHaveBeenCalled();
  });
});
