/**
 * NewChatView — the first-run starter question is asked inline, never as a
 * blocking modal.
 *
 * THE CHANGE THIS LOCKS IN: a project with no chats used to portal a
 * full-screen "What are you building?" dialog over the whole new-chat view,
 * and the inline starter cards were suppressed while it was up. That asked
 * the same question the screen already asks, and — because the dialog
 * portals to document.body, above any spotlight — it was also what forced
 * the onboarding tour to sit and wait (see OnboardingWizard.starterGate).
 *
 * The cards themselves stay. The user should still be offered a starting
 * point on the new-chat screen; they just should not be trapped behind it.
 */
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// ── Mocks ────────────────────────────────────────────────────────────────
// Everything that is not the starter-card surface is stubbed: this test is
// about which starter UI renders, not about chat input, daemons or worktrees.

const chatListState = vi.hoisted(() => ({
  current: { data: [] as any[], isSuccess: true },
}));

const chatParamsState = vi.hoisted(() => ({
  current: {
    tempNewChatWorkflow: null as string | null,
    setTempNewChatWorkflow: vi.fn(),
    setTempNewChatParams: vi.fn(),
    setTempNewChatPresets: vi.fn(),
    transferTempToChat: vi.fn(),
  },
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => chatListState.current,
}));

function storeMock(state: () => any) {
  return Object.assign(
    (selector?: any) => (selector ? selector(state()) : state()),
    {
      getState: () => state(),
      setState: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    },
  );
}

vi.mock("../../../store/chatStore", () => ({
  useChatStore: storeMock(() => ({
    hasLoaded: true,
    chats: new Map(),
    createChat: vi.fn(),
    selectChat: vi.fn(),
  })),
}));

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: storeMock(() => ({
    currentWorktree: { id: "w1", branch: "main", name: "main" },
    worktrees: [{ id: "w1", branch: "main", name: "main", is_main: true }],
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: storeMock(() => ({
    currentProject: { id: "p1", name: "proj", default_branch: "main" },
  })),
}));

vi.mock("../../../store/attachmentStore", () => ({
  useAttachmentStore: storeMock(() => ({ clearAttachments: vi.fn() })),
}));

vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: storeMock(() => ({ clearNewChatDraft: vi.fn() })),
}));

vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: storeMock(() => ({
    ensureApiKeyOrShowModal: vi.fn(),
  })),
}));

vi.mock("../../../store/chatParamsStore", () => ({
  useChatParamsStore: storeMock(() => chatParamsState.current),
}));

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    activeDaemon: { id: "d1" },
    loading: false,
    refresh: vi.fn(),
  }),
}));

vi.mock("@/hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, retryNow: vi.fn() }),
}));

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: false },
}));

vi.mock("../ChatInput", () => ({
  ChatInput: () => <div data-testid="chat-input" />,
}));

vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../../DaemonWaitState", () => ({ DaemonWaitState: () => null }));
vi.mock("../../Layout/ConnectDaemonModal", () => ({
  ConnectDaemonModal: () => null,
}));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({
  CreateWorktreeModal: () => null,
}));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({
  DiscoverWorktreesModal: () => null,
}));
vi.mock("../../ui/Tooltip", () => ({
  Tooltip: ({ children }: any) => <>{children}</>,
}));
vi.mock("../../icons/ReliantIcon", () => ({ ReliantIcon: () => null }));

vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { NewChatView } from "../NewChatView";

beforeEach(() => {
  vi.clearAllMocks();
  chatListState.current = { data: [], isSuccess: true };
  chatParamsState.current.tempNewChatWorkflow = null;
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("NewChatView — first-run starter picker", () => {
  // The empty-state first run: zero chats, no starter picked. This is exactly
  // the condition that used to raise the blocking dialog.
  it("does not trap a first-run user behind a blocking starter dialog", async () => {
    render(<NewChatView tabId="t1" />);

    await waitFor(() =>
      expect(screen.getByText("What are you building?")).toBeInTheDocument(),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("still asks the starter question inline on the new-chat screen", async () => {
    render(<NewChatView tabId="t1" />);

    await waitFor(() =>
      expect(screen.getByText("What are you building?")).toBeInTheDocument(),
    );
    // Exactly one copy — the inline set. A second would mean the modal is
    // back alongside it.
    expect(screen.getAllByText("What are you building?")).toHaveLength(1);
  });

  it("keeps showing the cards for a project that already has chats", async () => {
    chatListState.current = { data: [{ id: "c1" }], isSuccess: true };

    render(<NewChatView tabId="t1" />);

    await waitFor(() =>
      expect(screen.getByText("What are you building?")).toBeInTheDocument(),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // Don't flash the cards before we know whether this project has chats.
  it("waits for the chat list before rendering the cards", () => {
    chatListState.current = { data: undefined as any, isSuccess: false };

    render(<NewChatView tabId="t1" />);

    expect(screen.queryByText("What are you building?")).toBeNull();
  });
});
