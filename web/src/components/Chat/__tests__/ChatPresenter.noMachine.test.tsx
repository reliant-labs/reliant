/**
 * The composer of a chat with no machine, and of a chat whose machine is
 * unavailable (research/NO_MACHINE_CHATS.md §2.3–2.4).
 *
 *   - No machine by design: a one-line hint with "Connect a machine", and none
 *     of the machine surfaces (no wake line, no resume nudge).
 *   - On a machine that is offline, or that the run is waiting on: the status
 *     line offers "Continue without machine", which branches at the latest
 *     stored main-thread message.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "../../../gen/reliant/v1/daemon_registry_pb";
import { ChatActivity, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { useActivityStore } from "../../../store/activityStore";
import type { Message } from "../../../api/client";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";

const state = vi.hoisted(() => ({
  chat: undefined as
    | { id: string; workflowId?: string; activeDaemonId?: string; noMachine?: boolean; activity?: number; launchKind?: string }
    | undefined,
  daemons: [] as DaemonInfo[],
}));
const store = vi.hoisted(() => ({
  branchChatWithoutMachine: vi.fn(async () => ({ id: "branch-1" })),
  connectChatToMachine: vi.fn(),
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons, isLoading: false }),
}));
vi.mock("@/store/chatStore", () => ({
  useChatStore: Object.assign(vi.fn(), { getState: () => store }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: () => <div data-testid="composer" />,
}));
vi.mock("../ChatThinkingIndicator", () => ({ ChatThinkingIndicator: () => null }));
vi.mock("../ChatMessagesContainer", () => ({
  ChatMessagesContainer: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("../ScrollToBottomButton", () => ({ ScrollToBottomButton: () => null }));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../PermissionsPanel", () => ({ PermissionsPanel: () => null }));
vi.mock("../ChatHeader", () => ({ ChatHeader: () => null }));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => <div data-testid="resume-daemon-pill" /> }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../thread-views", () => ({ InterleavedTimeline: () => <div data-testid="timeline" /> }));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({ WorkflowViewerPanel: () => null }));
// The dialog is covered by its own test; here it only needs to exist.
vi.mock("../ConnectMachineDialog", () => ({
  ConnectMachineDialog: ({ open }: { open: boolean }) => (open ? <div role="dialog">Connect a machine</div> : null),
}));

const { ChatPresenter } = await import("../ChatPresenter");

function daemon(daemonId: string, hostname: string, status: DaemonStatus): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId, hostname, status });
}

function message(id: string, thread: string): Message {
  return { id, thread, chatId: "chat-1", role: MessageRole.ASSISTANT, contentBlocks: [] } as unknown as Message;
}

function renderPresenter(messages: Message[] = []) {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={messages}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        isChatBusy={false}
        pendingApprovals={[]}
        connectionStatus="connected"
        projectId="project-1"
        onSendMessage={vi.fn(async () => {})}
        onStopStreaming={vi.fn(async () => {})}
      />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  store.branchChatWithoutMachine.mockClear();
  state.daemons = [daemon("d-1", "MacBook", DaemonStatus.ACTIVE)];
  useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
});

describe("ChatPresenter: a chat with no machine", () => {
  beforeEach(() => {
    state.chat = { id: "chat-1", workflowId: "chat-1", noMachine: true, launchKind: "chat.start" };
  });

  it("says what the chat can do and offers to connect a machine", () => {
    renderPresenter();
    const hint = screen.getByTestId("no-machine-composer-hint");
    expect(hint).toHaveTextContent("can use the web and your integrations, not the files in this project");

    fireEvent.click(screen.getByRole("button", { name: "Connect a machine" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("Connect a machine");
  });

  it("shows none of the machine surfaces", () => {
    renderPresenter();
    expect(screen.queryByTestId("resume-daemon-pill")).toBeNull();
    expect(screen.queryByTestId("composer-wake-status")).toBeNull();
    expect(screen.queryByTestId("continue-without-machine")).toBeNull();
  });
});

describe("ChatPresenter: a chat whose machine is unavailable", () => {
  it("offers Continue without machine when the pinned machine is offline, branching at the latest stored message", async () => {
    state.chat = { id: "chat-1", workflowId: "chat-1", activeDaemonId: "d-1", launchKind: "chat.start" };
    state.daemons = [daemon("d-1", "MacBook", DaemonStatus.DISCONNECTED)];
    renderPresenter([message("m-1", "chat-1"), message("m-2", "chat-1"), message("s-1", "spawn-1")]);

    expect(screen.getByRole("status", { name: "MacBook is offline" })).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("continue-without-machine"));
    await waitFor(() => expect(store.branchChatWithoutMachine).toHaveBeenCalledWith("chat-1", "m-2"));
  });

  it("offers it while the run waits on an asleep machine", () => {
    state.chat = {
      id: "chat-1",
      workflowId: "chat-1",
      activeDaemonId: "d-1",
      launchKind: "chat.start",
    };
    // The wait is read from the activity store, which streams activity
    // changes; the chat query is only a snapshot from its last fetch.
    useActivityStore.getState().setActivity("chat-1", ChatActivity.WAITING_FOR_DAEMON);
    state.daemons = [daemon("d-1", "MacBook", DaemonStatus.SUSPENDED)];
    renderPresenter([message("m-1", "chat-1")]);

    expect(screen.getByRole("status", { name: "Waking MacBook…" })).toBeInTheDocument();
    expect(screen.getByTestId("continue-without-machine")).toBeInTheDocument();
  });

  it("offers nothing while the machine is online", () => {
    state.chat = { id: "chat-1", workflowId: "chat-1", activeDaemonId: "d-1", launchKind: "chat.start" };
    renderPresenter([message("m-1", "chat-1")]);
    expect(screen.queryByTestId("continue-without-machine")).toBeNull();
    expect(screen.queryByTestId("no-machine-composer-hint")).toBeNull();
  });
});
