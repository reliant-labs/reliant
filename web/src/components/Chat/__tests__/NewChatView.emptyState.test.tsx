/**
 * NewChatView — the new-chat screen goes straight to the composer.
 *
 * THE CHANGE THIS LOCKS IN: the space above the composer used to hold a grid
 * of workflow starter cards under "What are you building?" (Forge, landing
 * page, pitch deck, custom workflow, Claude Code migration, "Just chat"). The
 * grid is gone. Every one of those workflows is still chosen from the
 * composer's workflow selector; the screen itself is the welcome block, the
 * workspace controls and the composer, which starts a chat on its own.
 *
 * A host with something specific to suggest (the workflow editor) still
 * passes `emptyState`, and that is then the only thing rendered there.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// ── Mocks ────────────────────────────────────────────────────────────────
// Everything that is not the empty-state surface is stubbed: this test is
// about what fills the screen above the composer, not about daemons or
// worktrees.

const startChat = vi.hoisted(() => vi.fn(async () => ({ id: "chat-new" })));

const chatParamsState = vi.hoisted(() => ({
  current: {
    tempNewChatWorkflow: null as string | null,
    setTempNewChatWorkflow: vi.fn(),
    setTempNewChatParams: vi.fn(),
    setTempNewChatPresets: vi.fn(),
    transferTempToChat: vi.fn(),
  },
}));

// The starter grid waited on the chat list before rendering. It is resolved
// here, so if a grid comes back on that condition these tests see it.
vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [{ id: "c1" }], isSuccess: true }),
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
    startChat,
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
    activeDaemon: { daemonId: "d1", hostname: "laptop", status: 1 },
    // One connected machine (DaemonStatus.ACTIVE = 1): the chat defaults to it.
    daemons: [{ daemonId: "d1", hostname: "laptop", status: 1 }],
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

// The composer sends what the user typed, with no workflow chosen.
vi.mock("../ChatInput", () => ({
  ChatInput: ({ onSend }: { onSend: (content: string) => Promise<void> }) => (
    <button type="button" data-testid="chat-input" onClick={() => void onSend("hello")}>
      Send
    </button>
  ),
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

const STARTER_LABELS = [
  "What are you building?",
  "Build something new with Forge",
  "Create a landing page",
  "Create a pitch deck",
  "Create a custom workflow",
  "Migrate from Claude Code",
  "Just chat",
];

beforeEach(() => {
  vi.clearAllMocks();
  chatParamsState.current.tempNewChatWorkflow = null;
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("NewChatView — empty state", () => {
  it("shows the composer and no workflow starter grid", () => {
    render(<NewChatView tabId="t1" />);

    expect(screen.getByTestId("chat-input")).toBeInTheDocument();
    for (const label of STARTER_LABELS) {
      expect(screen.queryByText(label)).toBeNull();
    }
  });

  it("starts a chat straight from the composer, with no workflow picked first", async () => {
    render(<NewChatView tabId="t1" />);

    fireEvent.click(screen.getByTestId("chat-input"));

    await waitFor(() => expect(startChat).toHaveBeenCalledTimes(1));
    const [worktreeId, content, , , workflow] = startChat.mock.calls[0] as unknown[];
    expect(worktreeId).toBe("w1");
    expect(content).toBe("hello");
    // No starter seeded one: the composer's own (default) workflow applies.
    expect(workflow).toBeUndefined();
  });

  it("renders a host's own empty state, told whether the chat has a machine", () => {
    const emptyState = vi.fn(({ noMachine }: { noMachine: boolean }) => (
      <div data-testid="host-empty-state">{noMachine ? "no machine" : "on a machine"}</div>
    ));

    render(<NewChatView tabId="t1" emptyState={emptyState} />);

    expect(screen.getByTestId("host-empty-state")).toHaveTextContent("on a machine");
    for (const label of STARTER_LABELS) {
      expect(screen.queryByText(label)).toBeNull();
    }
  });
});
