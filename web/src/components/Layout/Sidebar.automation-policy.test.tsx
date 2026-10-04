/**
 * The sidebar's half of the chat-list policy (WORKFLOW_UI.md §6.2, §6.3):
 *
 *  - The OPEN chat keeps its row even when ListChats({sidebarOnly}) omits it
 *    (rule 4, the L2 bug). An automation chat opened from Runs or the Inbox is
 *    never in the server list, and one that was listed only while awaiting
 *    input drops out of the next refetch once the user answers. Either way the
 *    row must not vanish under the cursor.
 *  - An adopted automation row carries an origin glyph naming its automation.
 *  - "Move back to Runs" un-adopts, and the row leaves — even while open.
 */
import { act, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { Sidebar } from "./Sidebar";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { useChatStore } from "../../store/chatStore";
import { useActivityStore } from "../../store/activityStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProcessStore } from "../../store/processStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";
import { useSidebarPinStore } from "../../store/sidebarPinStore";
import type { ContextMenuItem } from "../ui/ContextMenu";

vi.mock("../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("../ui/Button", () => ({
  Button: ({ children, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props}>{children}</button>
  ),
}));

// The menu renders its items as plain buttons so a test can pick one.
vi.mock("../ui/ContextMenu", () => ({
  ContextMenu: ({ items }: { items: ContextMenuItem[] }) => (
    <div data-testid="context-menu">
      {items
        .filter((item) => !item.separator)
        .map((item) => (
          <button key={item.label} type="button" onClick={item.onClick}>
            {item.label}
          </button>
        ))}
    </div>
  ),
}));

vi.mock("../../hooks/useDebounce", () => ({
  useDebounce: <T,>(value: T) => value,
}));

type ChatFixture = Record<string, unknown> & { id: string };

const fixtures = vi.hoisted(() => ({
  listed: [] as Array<Record<string, unknown> & { id: string }>,
  detail: undefined as (Record<string, unknown> & { id: string }) | undefined,
  unadopt: vi.fn(),
  adopt: vi.fn(),
}));

vi.mock("../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: fixtures.listed, isLoading: false }),
  useChat: (chatId?: string) => ({ data: fixtures.detail?.id === chatId ? fixtures.detail : undefined }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn(), mutate: vi.fn() }),
}));

vi.mock("../../hooks/run-queries", () => ({
  useAdoptRun: () => ({ mutate: fixtures.adopt }),
  useUnadoptRun: () => ({ mutate: fixtures.unadopt }),
  useLiveRuns: () => ({ data: undefined }),
  summarizeLiveAutomations: () => ({ running: 0, needsYou: 0, truncated: false }),
}));

vi.mock("../../hooks/trigger-queries", () => ({
  useTriggerName: (triggerId?: string) => (triggerId === "trg-nightly" ? "Nightly triage" : undefined),
}));

vi.mock("../../hooks/message-queries", () => ({
  useMarkUnread: () => ({ mutate: vi.fn() }),
}));

const WORKTREE_ID = "worktree-main";
const PROJECT_ID = "project-1";

function chat(overrides: Partial<ChatFixture> & { id: string }): ChatFixture {
  return {
    title: overrides.id,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    lastMessageAt: "2024-01-01T00:00:00.000Z",
    unread: false,
    state: ChatState.ACTIVE,
    worktreeId: WORKTREE_ID,
    projectId: PROJECT_ID,
    ...overrides,
  };
}

const interactive = chat({ id: "chat-interactive", title: "Refactor auth" });
const scheduledRun = chat({
  id: "chat-scheduled",
  title: "Nightly triage run",
  launchKind: "schedule",
  triggerId: "trg-nightly",
});
const adoptedRun = chat({
  id: "chat-adopted",
  title: "Adopted triage",
  launchKind: "schedule",
  triggerId: "trg-nightly",
  adoptedAt: "2024-01-02T00:00:00.000Z",
});

function renderSidebar() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <Sidebar />
    </QueryClientProvider>,
  );
}

function openChat(id: string | null) {
  useChatStore.setState({ activeChatId: id } as Partial<ReturnType<typeof useChatStore.getState>>);
}

function rowFor(id: string) {
  return document.querySelector(`[data-chat-id="${id}"]`);
}

beforeEach(() => {
  vi.clearAllMocks();
  fixtures.listed = [interactive];
  fixtures.detail = undefined;
  useSidebarPinStore.setState({ releasedChatId: null });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });

  useChatStore.setState({ activeChatId: null, selectChat: vi.fn() } as Partial<
    ReturnType<typeof useChatStore.getState>
  >);
  useActivityStore.setState({ activities: new Map() } as Partial<ReturnType<typeof useActivityStore.getState>>);
  useWorktreeStore.setState({
    worktrees: [
      {
        id: WORKTREE_ID,
        name: "main",
        path: "/tmp/project",
        branch: "main",
        base_branch: "main",
        project_id: PROJECT_ID,
        status: WorktreeStatus.ACTIVE,
        is_main: true,
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
      id: PROJECT_ID,
      name: "Project",
      path: "/tmp/project",
      is_git_repo: true,
      worktree_count: 1,
      created_at: "2024-01-01T00:00:00.000Z",
      updated_at: "2024-01-01T00:00:00.000Z",
      last_active: "2024-01-01T00:00:00.000Z",
    },
  } as Partial<ReturnType<typeof useProjectStore.getState>>);
  useChatListPreferencesStore.setState({ viewMode: "grouped", sortOrder: "recent_activity" });
});

describe("the open chat stays in the sidebar (L2)", () => {
  it("keeps an open automation chat after a refresh that omits it", () => {
    // Listed while it awaited input; the user is in it.
    fixtures.listed = [interactive, scheduledRun];
    fixtures.detail = scheduledRun;
    openChat(scheduledRun.id);
    const { rerender } = renderSidebar();
    expect(rowFor(scheduledRun.id)).not.toBeNull();

    // They answer; the next ListChats refetch no longer returns it.
    fixtures.listed = [interactive];
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <Sidebar />
      </QueryClientProvider>,
    );

    expect(rowFor(scheduledRun.id)).not.toBeNull();
    expect(rowFor(interactive.id)).not.toBeNull();
  });

  it("shows an open chat the server never listed (opened from Runs or the Inbox)", () => {
    fixtures.listed = [interactive];
    fixtures.detail = scheduledRun;
    openChat(scheduledRun.id);
    renderSidebar();

    expect(rowFor(scheduledRun.id)).not.toBeNull();
  });

  it("drops the pinned row once a different chat is open", () => {
    fixtures.detail = scheduledRun;
    openChat(scheduledRun.id);
    const { rerender } = renderSidebar();
    expect(rowFor(scheduledRun.id)).not.toBeNull();

    fixtures.detail = interactive;
    act(() => openChat(interactive.id));
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <Sidebar />
      </QueryClientProvider>,
    );

    expect(rowFor(scheduledRun.id)).toBeNull();
  });

  it("does not pin a chat from another project", () => {
    fixtures.detail = { ...scheduledRun, projectId: "project-other" };
    openChat(scheduledRun.id);
    renderSidebar();

    expect(rowFor(scheduledRun.id)).toBeNull();
  });
});

describe("origin glyph", () => {
  it("marks an adopted automation row with its launch kind and automation", () => {
    fixtures.listed = [interactive, adoptedRun];
    renderSidebar();

    const glyph = within(rowFor(adoptedRun.id) as HTMLElement).getByTestId(`chat-origin-glyph-${adoptedRun.id}`);
    expect(glyph).toHaveAttribute("data-launch-kind", "schedule");
    expect(glyph).toHaveAccessibleName("Started by schedule Nightly triage");
  });

  it("leaves an interactive chat unmarked", () => {
    fixtures.listed = [interactive, adoptedRun];
    renderSidebar();

    expect(screen.queryByTestId(`chat-origin-glyph-${interactive.id}`)).toBeNull();
  });
});

describe("Move back to Runs", () => {
  function openMenuFor(id: string) {
    const row = rowFor(id) as HTMLElement;
    expect(row).not.toBeNull();
    act(() => {
      row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 }));
    });
    return screen.getByTestId("context-menu");
  }

  it("is offered on an adopted run and not on an interactive chat", () => {
    fixtures.listed = [interactive, adoptedRun];
    renderSidebar();

    expect(within(openMenuFor(adoptedRun.id)).getByText("Move back to Runs")).toBeInTheDocument();
    expect(within(openMenuFor(interactive.id)).queryByText("Move back to Runs")).toBeNull();
  });

  it("calls UnadoptChat, and the open row leaves once the list drops it", () => {
    fixtures.listed = [interactive, adoptedRun];
    fixtures.detail = adoptedRun;
    openChat(adoptedRun.id);
    const { rerender } = renderSidebar();

    const moveBack = within(openMenuFor(adoptedRun.id)).getByText("Move back to Runs");
    act(() => moveBack.click());
    expect(fixtures.unadopt).toHaveBeenCalledWith(adoptedRun.id, expect.anything());

    // The server no longer lists it, and it is still the open chat: without
    // the release it would stay pinned and the action would look broken.
    fixtures.listed = [interactive];
    fixtures.detail = { ...adoptedRun, adoptedAt: undefined };
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <Sidebar />
      </QueryClientProvider>,
    );

    expect(rowFor(adoptedRun.id)).toBeNull();
  });
});
