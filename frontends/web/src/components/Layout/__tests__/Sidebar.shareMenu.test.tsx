// Copyright (c) 2025 Reliant Labs

/**
 * A chat row's ⋯ menu and sharing: its owner gets the chat header's share
 * dialog ("Share…", or "Stop sharing…" once shared), and, for a chat started
 * from a saved workflow they can share, "Share workflow…" for that workflow.
 * A chat with no org, or no control plane to share through, offers neither.
 * Which saved workflow a chat runs is useChatWorkflowShare's (its own tests).
 */

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import { ChatActivity, ChatState } from "../../../gen/reliant/v1/chat_pb";
import { ResourceVisibility } from "../../../gen/reliant/v1/common_pb";
import type { MenuGroup } from "../../forge-ui/dropdown_menu";
import type { ShareableWorkflow } from "../../Sharing/shareTargets";
import { useChatStore } from "../../../store/chatStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useProjectStore } from "../../../store/projectStore";
import { useActivityStore } from "../../../store/activityStore";
import { useChatListPreferencesStore } from "../../../store/chatListPreferencesStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

const state = vi.hoisted(() => ({
  controlPlane: true,
  // The saved workflow behind each chat, as useChatWorkflowShare answers it.
  workflows: {} as Record<string, ShareableWorkflow>,
  // The chat each call of useChatWorkflowShare was asked about.
  asked: [] as Array<string | undefined>,
}));

vi.mock("../../../services/controlPlane/config", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../services/controlPlane/config")>();
  return {
    ...actual,
    get hasControlPlane() {
      return state.controlPlane;
    },
  };
});
vi.mock("../../Sharing/useChatWorkflowShare", () => ({
  useChatWorkflowShare: (chat: { id: string } | undefined) => {
    state.asked.push(chat?.id);
    return (chat && state.workflows[chat.id]) ?? null;
  },
}));
vi.mock("../../Sharing/ShareChatDialog", () => ({
  ShareChatDialog: ({ chatId }: { chatId: string }) => <div role="dialog" aria-label={`Share chat ${chatId}`} />,
}));
vi.mock("../../Sharing/ShareWorkflowDialog", () => ({
  ShareWorkflowDialog: ({ workflow }: { workflow: ShareableWorkflow | null }) =>
    workflow ? <div role="dialog" aria-label={`Share workflow ${workflow.draftId}`} /> : null,
}));
// The context menu as plain items, so a test can choose one.
vi.mock("../../forge-ui/context_menu", () => ({
  default: ({ groups, label }: { groups: MenuGroup[]; label: string }) => (
    <div role="menu" aria-label={label}>
      {groups.flatMap((g) => g.items).map((item) => (
        <button key={item.label} type="button" role="menuitem" onClick={item.onClick}>
          {item.label}
        </button>
      ))}
    </div>
  ),
}));
vi.mock("../../OnboardingFlow/useChatSetup", () => ({ useChatSetup: () => "ready" }));
vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("../../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));
vi.mock("../../Sharing/SharedWithOrgSection", () => ({ SharedWithOrgSection: () => null }));

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

const chat = (id: string, over: Record<string, unknown> = {}) => ({
  id,
  title: `Chat ${id}`,
  projectId: "p-api",
  worktreeId: "",
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
  unread: false,
  activity: ChatActivity.IDLE,
  workingThreads: 0,
  state: ChatState.IDLE,
  ...over,
});
const CHATS = vi.hoisted(() => [] as Array<Record<string, unknown>>);
const MAIN_WORKTREES = [{ id: "w-main", name: "main", branch: "main", path: "/p/api", isMain: true, projectId: "p-api" }];

vi.mock("../../../hooks/chat-queries", () => ({
  useChatListWithWorkspaces: () => ({
    data: { chats: CHATS, lastUserUpdateSequence: 0, mainWorktrees: MAIN_WORKTREES },
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
import { ALL_PROJECTS_PICK, useShellLastPick } from "../sidebar/shellProject";

function renderSidebar() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <Sidebar pathname="/" />
    </QueryClientProvider>,
  );
}

async function openRowMenu(id: string) {
  await userEvent.click(screen.getByRole("button", { name: `More options for Chat ${id}` }));
  return screen.findByRole("menu", { name: "Chat actions" });
}

beforeEach(() => {
  useShellLastPick.setState({ key: ALL_PROJECTS_PICK });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  useChatStore.setState({ activeChatId: null, selectChat: vi.fn() } as Partial<ReturnType<typeof useChatStore.getState>>);
  useWorktreeStore.setState({
    currentWorktreeId: null,
    currentWorktreeProjectId: null,
    switchWorktreeContext: vi.fn(async () => undefined),
  } as Partial<ReturnType<typeof useWorktreeStore.getState>>);
  useProjectStore.setState({ currentProject: null } as Partial<ReturnType<typeof useProjectStore.getState>>);
  useWorkspaceStateStore.setState({ projectFilterId: null });
  useChatListPreferencesStore.setState({ viewMode: "flat", sortOrder: "alphabetical_asc" });
  useActivityStore.setState({ entries: new Map(), activities: new Map(), workingThreads: new Map(), maxSeenSeq: 0 });
  state.controlPlane = true;
  state.asked = [];
  state.workflows = {
    flow: { draftId: "d-triage", name: "Triage", orgId: "acme", shared: false },
    "flow-shared": { draftId: "d-deploy", name: "Deploy", orgId: "acme", shared: true },
  };
  CHATS.splice(
    0,
    CHATS.length,
    chat("mine", { orgId: "acme", visibility: ResourceVisibility.PRIVATE }),
    chat("shared", { orgId: "acme", visibility: ResourceVisibility.ORG }),
    chat("orgless"),
    chat("flow", { orgId: "acme", visibility: ResourceVisibility.PRIVATE, workflowName: "triage" }),
    chat("flow-shared", { orgId: "acme", visibility: ResourceVisibility.PRIVATE, workflowName: "deploy" }),
  );
});

describe("Sidebar: a chat row's ⋯ menu shares", () => {
  it("opens the chat header's share dialog from Share…", async () => {
    renderSidebar();
    const menu = await openRowMenu("mine");
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Share…" }));
    expect(await screen.findByRole("dialog", { name: "Share chat mine" })).toBeInTheDocument();
    // No saved workflow behind this chat: nothing to share but the chat.
    expect(within(menu).queryByRole("menuitem", { name: /workflow/ })).toBeNull();
  });

  it("offers Stop sharing… on a shared chat", async () => {
    renderSidebar();
    const menu = await openRowMenu("shared");
    expect(within(menu).getByRole("menuitem", { name: "Stop sharing…" })).toBeInTheDocument();
    expect(within(menu).queryByRole("menuitem", { name: "Share…" })).toBeNull();
  });

  it("offers no share on a chat with no org, or with no control plane", async () => {
    renderSidebar();
    let menu = await openRowMenu("orgless");
    expect(within(menu).queryByRole("menuitem", { name: /Share|sharing/ })).toBeNull();
    state.controlPlane = false;
    await userEvent.keyboard("{Escape}");
    menu = await openRowMenu("mine");
    expect(within(menu).queryByRole("menuitem", { name: /Share|sharing/ })).toBeNull();
  });

  it("offers Share workflow… for the saved workflow the chat was started from", async () => {
    renderSidebar();
    const menu = await openRowMenu("flow");
    expect(state.asked).toContain("flow");
    expect(within(menu).getByRole("menuitem", { name: "Share…" })).toBeInTheDocument();
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Share workflow…" }));
    expect(await screen.findByRole("dialog", { name: "Share workflow d-triage" })).toBeInTheDocument();
  });

  it("offers Stop sharing workflow… when that workflow is shared", async () => {
    renderSidebar();
    const menu = await openRowMenu("flow-shared");
    expect(within(menu).getByRole("menuitem", { name: "Stop sharing workflow…" })).toBeInTheDocument();
  });
});
