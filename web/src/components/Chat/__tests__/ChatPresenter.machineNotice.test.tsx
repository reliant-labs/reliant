/**
 * The chat says what its machine is doing whenever work waits on it — not
 * only once the run reaches the machine wait.
 *
 * Prod, 2026-10-10 (chat 66a045ce): the user's machine was asleep and being
 * resumed ("Starting a machine for your workspace…") while their two
 * "continue" messages sat under "Processing •••". The run had been resumed
 * onto a build that could not replay it, so it was RUNNING (retrying its
 * workflow task) and never reached WAITING_FOR_DAEMON — the only state the
 * footer or the composer would speak about the machine in.
 */

import { fireEvent, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatActivity, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "../../../gen/reliant/v1/daemon_registry_pb";
import { useActivityStore } from "../../../store/activityStore";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { clearWaking } from "../../../lib/machineWake";
import type { Message } from "../../../api/client";
import { ChatPresenter } from "../ChatPresenter";

const state = vi.hoisted(() => ({
  chat: undefined as
    | { id: string; workflowId?: string; activeDaemonId?: string; noMachine?: boolean; launchKind?: string }
    | undefined,
  daemons: [] as DaemonInfo[],
  resumed: [] as string[],
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons, isLoading: false }),
  useResumeDaemon: (callbacks?: { onSuccess?: (id: string) => void }) => ({
    mutate: (id: string) => {
      state.resumed.push(id);
      callbacks?.onSuccess?.(id);
    },
    isPending: false,
  }),
}));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
// The run's own footer, stubbed to the line the user saw.
vi.mock("../ChatThinkingIndicator", () => ({
  ChatThinkingIndicator: () => <div data-testid="thinking-indicator">Processing</div>,
}));
vi.mock("../ChatInputWrapper", () => ({ ChatInputWrapper: () => null }));
vi.mock("../ChatMessagesContainer", () => ({
  ChatMessagesContainer: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("../ScrollToBottomButton", () => ({ ScrollToBottomButton: () => null }));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../PermissionsPanel", () => ({ PermissionsPanel: () => null }));
vi.mock("../ChatHeader", () => ({ ChatHeader: () => null }));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: ({ footer }: { footer?: React.ReactNode }) => <div data-testid="timeline">{footer}</div>,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({ WorkflowViewerPanel: () => null }));
// The no-machine composer hint's dialog reads the machine list on its own.
vi.mock("../ConnectMachineDialog", () => ({ ConnectMachineDialog: () => null }));

const RESUMING = "Starting a machine for your workspace. A first start usually takes a few minutes.";

function message(id: string, role: MessageRole, sentAgoMs = 0): Message {
  return {
    id,
    role,
    content: id,
    thread: "chat-1",
    contentBlocks: [],
    createdAt: new Date(Date.now() - sentAgoMs).toISOString(),
  } as unknown as Message;
}

function machine(status: DaemonStatus, extra: Partial<DaemonInfo> = {}): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId: "d-1", hostname: "Cloud box", status, ...extra });
}

/** The user's two "continue"s, the latest one 42 seconds ago. */
const unansweredContinue = () => [
  message("m1", MessageRole.ASSISTANT, 600_000),
  message("m2", MessageRole.USER, 90_000),
  message("m3", MessageRole.USER, 42_000),
];

function renderPresenter(messages: Message[], { isChatBusy }: { isChatBusy: boolean }) {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={messages}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        isChatBusy={isChatBusy}
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
  state.chat = { id: "chat-1", workflowId: "chat-1", activeDaemonId: "d-1", launchKind: "chat.start" };
  state.daemons = [machine(DaemonStatus.PENDING, { lastStatusMessage: RESUMING })];
  state.resumed = [];
  clearWaking("d-1");
  // The run was resumed and is retrying its workflow task: RUNNING, not
  // WAITING_FOR_DAEMON.
  useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
});

describe("ChatPresenter: the machine's state under a wedged or retrying run", () => {
  it("says the machine is waking and the message will send, instead of Processing", () => {
    renderPresenter(unansweredContinue(), { isChatBusy: true });

    const notice = screen.getByTestId("chat-machine-notice");
    expect(notice).toHaveTextContent("Waking your machine — your message will send when it connects");
    expect(notice).toHaveTextContent(RESUMING);
    expect(screen.getByTestId("chat-machine-notice-elapsed").textContent).toMatch(/^· 4[2-4]s$/);
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });

  it("names the default machine for an unpinned chat", () => {
    state.chat = { id: "chat-1", workflowId: "chat-1", launchKind: "chat.start" };
    renderPresenter(unansweredContinue(), { isChatBusy: true });
    expect(screen.getByTestId("chat-machine-notice")).toHaveTextContent(
      "Waking your machine — your message will send when it connects",
    );
  });

  it("speaks for a run held for its machine just the same", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.WAITING_FOR_DAEMON);
    renderPresenter(unansweredContinue(), { isChatBusy: false });
    expect(screen.getByTestId("chat-machine-notice")).toHaveTextContent(
      "Waking your machine — your message will send when it connects",
    );
  });

  it("says a reconnecting machine is reconnecting, for a run already past the message", () => {
    state.daemons = [machine(DaemonStatus.DISCONNECTED)];
    renderPresenter([message("m1", MessageRole.USER), message("m2", MessageRole.ASSISTANT)], { isChatBusy: true });
    expect(screen.getByTestId("chat-machine-notice")).toHaveTextContent(
      "Reconnecting to your machine — the run continues when it connects",
    );
  });

  it("says a failed machine failed and offers Try again; the composer keeps only the way out", () => {
    state.daemons = [machine(DaemonStatus.FAILED, { lastStatusMessage: "image pull failed: manifest unknown" })];
    renderPresenter(unansweredContinue(), { isChatBusy: true });

    const notice = screen.getByTestId("chat-machine-notice");
    expect(notice).toHaveTextContent("Your machine failed to start");
    expect(notice).toHaveTextContent("image pull failed: manifest unknown");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(state.resumed).toEqual(["d-1"]);

    expect(screen.queryByTestId("composer-machine-failed")).toBeNull();
    expect(screen.getByTestId("composer-machine-exit")).toHaveTextContent("Continue without machine");
  });

  it("offers to start an asleep machine nothing is waking", () => {
    state.daemons = [machine(DaemonStatus.SUSPENDED)];
    renderPresenter(unansweredContinue(), { isChatBusy: true });

    expect(screen.getByTestId("chat-machine-notice")).toHaveTextContent("Your machine is asleep");
    fireEvent.click(screen.getByRole("button", { name: "Start it" }));
    expect(state.resumed).toEqual(["d-1"]);
    // Resume recorded the wake: it now reads as starting.
    expect(screen.getByTestId("chat-machine-notice")).toHaveTextContent("Your machine is asleep — starting it");
  });
});

describe("ChatPresenter: nothing to say about the machine", () => {
  it("leaves the run's own footer when the machine is up", () => {
    state.daemons = [machine(DaemonStatus.ACTIVE)];
    renderPresenter(unansweredContinue(), { isChatBusy: true });
    expect(screen.getByTestId("thinking-indicator")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-machine-notice")).toBeNull();
  });

  it("says nothing when no work waits on the machine", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
    renderPresenter([message("m1", MessageRole.USER), message("m2", MessageRole.ASSISTANT)], { isChatBusy: false });
    expect(screen.queryByTestId("chat-machine-notice")).toBeNull();
  });

  it("never speaks for a chat with no machine", () => {
    state.chat = { id: "chat-1", workflowId: "chat-1", noMachine: true, launchKind: "chat.start" };
    renderPresenter(unansweredContinue(), { isChatBusy: true });
    expect(screen.queryByTestId("chat-machine-notice")).toBeNull();
  });
});
