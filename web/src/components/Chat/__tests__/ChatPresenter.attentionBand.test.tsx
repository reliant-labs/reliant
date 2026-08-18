/**
 * The band of attention surfaces between the transcript and the composer.
 *
 * Two things live there and they are NOT interchangeable:
 *
 *  - the queued-message strip — what you already sent and can still take back;
 *  - the background-work pill — an ambient readout of spawns and commands that
 *    are running on their own.
 *
 * The queue outranks the pill, so it renders ABOVE it and nearer the
 * transcript. That order cannot be expressed from inside the composer, which
 * is why ChatPresenter owns the mailbox read: rendered from ChatInput the
 * strip could only ever appear below the pill, since the pill is a sibling
 * mounted before it.
 *
 * The order is asserted by document position rather than by snapshot so it
 * survives styling churn in either component.
 */

import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";

const CHAT_ID = "chat-abc";
const MAIN_THREAD_ID = "workflow-xyz";
const HUMAN = 5;

const { listQueuedAgentMessagesMock } = vi.hoisted(() => ({
  listQueuedAgentMessagesMock: vi.fn(),
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: { listQueuedAgentMessages: listQueuedAgentMessagesMock },
  QUEUED_SENDER_KIND_HUMAN: 5,
}));

// chat.workflowId is the chat's main thread id — the mailbox the composer
// queues into, and therefore the one the strip must read.
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return {
    ...actual,
    useChat: () => ({
      data: { id: CHAT_ID, workflowId: MAIN_THREAD_ID },
      isLoading: false,
    }),
  };
});

// ChatPresenter pulls in the whole chat stack. This suite is about the order
// of two surfaces, so everything else renders a marker and nothing heavier —
// except BackgroundWorkPill and QueuedMessages, which are the subjects.
vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: () => <div data-testid="chat-input" />,
}));
vi.mock("../ChatThinkingIndicator", () => ({ ChatThinkingIndicator: () => null }));
vi.mock("../ChatMessagesContainer", () => ({
  ChatMessagesContainer: ({ children }: { children?: React.ReactNode }) => (
    <div data-testid="messages-container">{children}</div>
  ),
}));
vi.mock("../ScrollToBottomButton", () => ({ ScrollToBottomButton: () => null }));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => (
    <>{children}</>
  ),
}));
vi.mock("../PermissionsPanel", () => ({ PermissionsPanel: () => null }));
vi.mock("../ChatHeader", () => ({ ChatHeader: () => <div data-testid="chat-header" /> }));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: () => <div data-testid="timeline" />,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => <div data-testid="workflow-viewer-panel" />,
}));

import { useThreadActivityStore } from "../../../store/threadActivityStore";
import { ChatPresenter } from "../ChatPresenter";

const baseProps = {
  messages: [],
  approvals: [],
  errorEvents: [],
  infoEvents: [],
  runOutputs: [],
  chatId: CHAT_ID,
  isChatBusy: true,
  pendingApprovals: [],
  connectionStatus: "connected",
  worktreeId: null,
  projectId: "project-1",
  onSendMessage: vi.fn(async () => {}),
  onStopStreaming: vi.fn(async () => {}),
};

/** A running spawn, which is what makes the background-work pill render. */
function seedRunningSpawn() {
  useThreadActivityStore.getState().setThreads(CHAT_ID, [
    {
      update_type: "thread",
      id: "t-1",
      chat_id: CHAT_ID,
      thread: "thread-42",
      workflow_id: "wf-42",
      origin: "spawn",
      status: "running",
      is_planning_mode: false,
      thread_title: "researcher",
      current_activity: "CallLLM",
      created_at: new Date().toISOString(),
    },
  ]);
}

function renderPresenter(surface: "desktop" | "mobile") {
  return renderWithQuery(
    <SurfaceProvider surface={surface}>
      <ChatPresenter {...baseProps} />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  useThreadActivityStore.getState().clearAll();
  listQueuedAgentMessagesMock.mockResolvedValue({
    messages: [
      {
        id: "queued-1",
        body: "check the migration file",
        created_at: new Date(Date.now() - 3_000).toISOString(),
        sender_kind: HUMAN,
        attachments: [],
      },
    ],
  });
});

describe.each(["desktop", "mobile"] as const)(
  "the attention band above the composer (%s)",
  (surface) => {
    it("renders the queued strip above the background-work pill", async () => {
      seedRunningSpawn();
      renderPresenter(surface);

      const strip = await screen.findByTestId("queued-messages");
      const pill = screen.getByTestId("background-work-pill");

      // DOCUMENT_POSITION_FOLLOWING: the pill comes after the strip.
      expect(
        strip.compareDocumentPosition(pill) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
    });

    it("shows the queued message even with no background work at all", async () => {
      renderPresenter(surface);

      expect(await screen.findByText("check the migration file")).toBeTruthy();
      expect(screen.queryByTestId("background-work-pill")).toBeNull();
    });

    it("reads the mailbox of the chat's main thread", async () => {
      renderPresenter(surface);

      await waitFor(() => {
        expect(listQueuedAgentMessagesMock).toHaveBeenCalledWith(
          CHAT_ID,
          MAIN_THREAD_ID,
        );
      });
    });
  },
);

/**
 * `isChatBusy` is RUNNING *or* AWAITING_INPUT, so it stays true while a
 * question waits on the user — but the agent is parked, not working. The strip
 * must read "actually working", which is what the composer computed before the
 * strip moved out of it. Getting this wrong offers "Interrupt & send now" when
 * there is no work in flight to stop: the interrupt cancels the thread's
 * executing tool calls and signals the workflow, so firing it against a parked
 * agent is a state change with nothing to cancel.
 *
 * The queued messages stay VISIBLE throughout — being unable to interrupt is
 * not the same as being unable to see, or take back, what you queued.
 */
describe("the interrupt affordance tracks work in flight, not chat busy-ness", () => {
  it("offers interrupt while the agent is genuinely working", async () => {
    renderPresenter("desktop");

    expect(await screen.findByText("check the migration file")).toBeTruthy();
    expect(
      screen.getByLabelText("Interrupt the agent and deliver queued messages now"),
    ).toBeTruthy();
  });

  it("withholds interrupt while a question waits on the user, but still shows the queue", async () => {
    renderWithQuery(
      <SurfaceProvider surface="desktop">
        <ChatPresenter {...baseProps} hasPendingQuestion={true} />
      </SurfaceProvider>,
    );

    expect(await screen.findByText("check the migration file")).toBeTruthy();
    expect(
      screen.queryByLabelText("Interrupt the agent and deliver queued messages now"),
    ).toBeNull();
  });

  it("withholds interrupt in discuss mode, but still shows the queue", async () => {
    renderWithQuery(
      <SurfaceProvider surface="desktop">
        <ChatPresenter {...baseProps} isDiscussMode={true} />
      </SurfaceProvider>,
    );

    expect(await screen.findByText("check the migration file")).toBeTruthy();
    expect(
      screen.queryByLabelText("Interrupt the agent and deliver queued messages now"),
    ).toBeNull();
  });
});
