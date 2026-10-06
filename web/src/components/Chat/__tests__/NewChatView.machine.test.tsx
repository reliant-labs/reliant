/**
 * Where a new chat runs (research/NO_MACHINE_CHATS.md §2.1).
 *
 *   - The default is always the user's machine when they have a usable one,
 *     an asleep one included (sending wakes it).
 *   - It falls back to No machine only when they have none at all.
 *   - No machine is also an explicit choice in the picker, and the way out of
 *     the "waiting for your machine" state.
 *
 * A chat with no machine starts with no_machine and no daemon, in the main
 * workspace, and its composer is usable without a connected machine.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const daemonState = vi.hoisted(() => ({
  current: {
    activeDaemon: undefined as { daemonId: string; hostname: string; status: number } | undefined,
    daemons: [] as Array<{ daemonId: string; hostname: string; status: number }>,
    loading: false,
  },
}));
const startChat = vi.hoisted(() => vi.fn(async () => ({ id: "chat-new" })));

function storeMock(state: () => any) {
  return Object.assign((selector?: any) => (selector ? selector(state()) : state()), {
    getState: () => state(),
    setState: vi.fn(),
    subscribe: vi.fn(() => () => undefined),
  });
}

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isSuccess: true }),
}));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: storeMock(() => ({ hasLoaded: true, chats: new Map(), startChat, selectChat: vi.fn() })),
}));
vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: storeMock(() => ({
    currentWorktree: { id: "w-feature", branch: "feature", name: "feature" },
    worktrees: [
      { id: "w-main", branch: "main", name: "main", is_main: true },
      { id: "w-feature", branch: "feature", name: "feature" },
    ],
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: storeMock(() => ({ currentProject: { id: "p1", name: "proj", default_branch: "main" } })),
}));
vi.mock("../../../store/attachmentStore", () => ({
  useAttachmentStore: storeMock(() => ({ clearAttachments: vi.fn() })),
}));
vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: storeMock(() => ({ clearNewChatDraft: vi.fn() })),
}));
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: storeMock(() => ({ ensureApiKeyOrShowModal: vi.fn() })),
}));
vi.mock("../../../store/chatParamsStore", () => ({
  useChatParamsStore: storeMock(() => ({ transferTempToChat: vi.fn() })),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ ...daemonState.current, refresh: vi.fn() }),
}));
vi.mock("@/hooks/useDaemonWait", () => ({
  useDaemonWait: ({ waiting }: { waiting: boolean }) => ({
    state: waiting ? { tone: "waiting", title: "Waiting for your machine" } : null,
    retryNow: vi.fn(),
  }),
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({ useBundledDaemonPending: () => false }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: true } }));
// The composer is a Send button reporting whether it is enabled.
vi.mock("../ChatInput", () => ({
  ChatInput: ({ onSend, disabled }: { onSend: (c: string) => Promise<void>; disabled?: boolean }) => (
    <button type="button" disabled={disabled} onClick={() => void onSend("hello")}>
      Send
    </button>
  ),
}));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../../DaemonWaitState", () => ({
  DaemonWaitState: ({ state }: { state: { title: string } }) => <div role="status">{state.title}</div>,
}));
vi.mock("../../Layout/ConnectDaemonModal", () => ({ ConnectDaemonModal: () => null }));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({ CreateWorktreeModal: () => null }));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({ DiscoverWorktreesModal: () => null }));
vi.mock("../../ui/Tooltip", () => ({ Tooltip: ({ children }: any) => <>{children}</> }));
vi.mock("../../icons/ReliantIcon", () => ({ ReliantIcon: () => null }));
vi.mock("../../Onboarding/WorkflowStarterCards", () => ({ WorkflowStarterCards: () => null }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { NewChatView } from "../NewChatView";

// DaemonStatus: ACTIVE = 1, PENDING = 4, SUSPENDED = 5.
const laptop = { daemonId: "d-laptop", hostname: "laptop", status: 1 };

beforeEach(() => {
  startChat.mockClear();
  daemonState.current = { activeDaemon: laptop, daemons: [laptop], loading: false };
});

const machineArg = () => (startChat.mock.calls[0] as unknown[])[6];

describe("NewChatView: where the chat runs", () => {
  it("runs on the user's machine by default, as before", async () => {
    render(<NewChatView tabId="t1" />);
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("laptop");
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(machineArg()).toEqual({ daemonId: undefined, noMachine: undefined });
    expect(screen.queryByTestId("new-chat-no-machine-hint")).toBeNull();
  });

  it("falls back to No machine when the user has no usable machine, and can send without one", async () => {
    const provisioning = { daemonId: "d-cloud", hostname: "cloud", status: 4 };
    daemonState.current = { activeDaemon: undefined, daemons: [provisioning], loading: false };
    render(<NewChatView tabId="t1" />);

    expect(screen.getByTestId("machine-picker")).toHaveTextContent("No machine");
    expect(screen.getByTestId("new-chat-no-machine-hint")).toHaveTextContent("not the files in this project");
    // A workspace is a checkout on a machine; there is none to pick.
    expect(screen.queryByText("New workspace")).toBeNull();

    const send = screen.getByRole("button", { name: "Send" });
    expect(send).toBeEnabled();
    fireEvent.click(send);
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect((startChat.mock.calls[0] as unknown[])[0]).toBe("w-main");
    expect(machineArg()).toEqual({ daemonId: undefined, noMachine: true });
  });

  it("keeps an asleep machine as the default: the send wakes it", () => {
    const asleep = { ...laptop, status: 5 };
    daemonState.current = { activeDaemon: undefined, daemons: [asleep], loading: false };
    render(<NewChatView tabId="t1" />);
    expect(screen.getByTestId("machine-picker")).not.toHaveTextContent("No machine");
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for your machine");
  });

  it("offers Continue without machine while waiting for one", async () => {
    const asleep = { ...laptop, status: 5 };
    daemonState.current = { activeDaemon: undefined, daemons: [asleep], loading: false };
    render(<NewChatView tabId="t1" />);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    fireEvent.click(screen.getByTestId("new-chat-continue-without-machine"));
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("No machine");
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(machineArg()).toEqual({ daemonId: undefined, noMachine: true });
  });

  it("lets the user pick No machine, or a specific machine, explicitly", async () => {
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    fireEvent.click(screen.getByRole("option", { name: /No machine/ }));
    expect(screen.getByTestId("machine-picker")).toHaveTextContent("No machine");

    fireEvent.click(screen.getByTestId("machine-picker"));
    fireEvent.click(screen.getByRole("option", { name: /laptop/ }));
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(machineArg()).toEqual({ daemonId: "d-laptop", noMachine: undefined });
  });
});
