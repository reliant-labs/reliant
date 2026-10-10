/**
 * A message sent while the chat's machine is starting must read as QUEUED,
 * not stuck (prod report, 2026-10-09: "it just looks like it gets stuck.
 * There's nothing in the UI indicating 'will send once daemon connects'").
 *
 * The run is held for its machine with ChatActivity.WAITING_FOR_DAEMON. That
 * activity reaches the web through the activity store — the single source of
 * truth — not through the chat query, which is a snapshot from whenever it was
 * last fetched. And it is not RUNNING, so nothing keyed on "the chat is
 * running" lights up for it. Before this, the footer was gated on running and
 * the "waiting" check read the stale chat snapshot, so a held run showed
 * nothing at all under the user's message.
 */

import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatActivity, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { useActivityStore } from "../../../store/activityStore";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import type { Message } from "../../../api/client";
import { ChatPresenter } from "../ChatPresenter";

const state = vi.hoisted(() => ({
  chat: undefined as { id: string; workflowId?: string; activity?: number; launchKind?: string } | undefined,
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [], isLoading: false }),
  useResumeDaemon: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
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
// The transcript is stubbed down to the one thing under test: the footer it
// renders under the last message.
vi.mock("../thread-views", () => ({
  InterleavedTimeline: ({ footer }: { footer?: React.ReactNode }) => <div data-testid="timeline">{footer}</div>,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({ WorkflowViewerPanel: () => null }));

function message(id: string, role: MessageRole): Message {
  return { id, role, content: id, thread: "chat-1" } as unknown as Message;
}

function renderPresenter(messages: Message[]) {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={messages}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        // The activity is WAITING_FOR_DAEMON, which is not "running".
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
  // The chat query's snapshot predates the wait: it still says RUNNING.
  state.chat = { id: "chat-1", workflowId: "chat-1", activity: ChatActivity.RUNNING, launchKind: "chat.start" };
  useActivityStore.getState().setActivity("chat-1", ChatActivity.WAITING_FOR_DAEMON);
});

describe("ChatPresenter: a run held for its machine", () => {
  it("marks the user's unanswered message as queued until the machine connects", () => {
    renderPresenter([message("m1", MessageRole.ASSISTANT), message("m2", MessageRole.USER)]);
    expect(screen.getByText("Queued — will send when your machine connects")).toBeInTheDocument();
  });

  it("says it is waiting for the machine when the run already read the message (held mid-turn)", () => {
    renderPresenter([message("m1", MessageRole.USER), message("m2", MessageRole.ASSISTANT)]);
    expect(screen.getByText("Waiting for your machine")).toBeInTheDocument();
    expect(screen.queryByText(/Queued — will send/)).toBeNull();
  });

  it("says nothing about the machine once the run is running", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
    renderPresenter([message("m1", MessageRole.USER)]);
    expect(screen.queryByText(/your machine/)).toBeNull();
  });
});

// The run that held the message ended because the machine never came up —
// it failed to start (the prod workspace crash-looped for hours), was
// removed, or was still down after hours. The server keeps the message queued
// and sends it when the machine connects; the transcript must not leave it
// looking dropped under an error card.
describe("ChatPresenter: a message queued for a machine whose run ended", () => {
  beforeEach(() => {
    state.chat = { id: "chat-1", workflowId: "chat-1", activity: ChatActivity.ERROR, launchKind: "chat.start" };
    useActivityStore.getState().setActivity("chat-1", ChatActivity.QUEUED_FOR_MACHINE);
  });

  it("marks the unanswered message as queued until the machine is back, without a thinking indicator", () => {
    renderPresenter([message("m1", MessageRole.ASSISTANT), message("m2", MessageRole.USER)]);
    expect(screen.getByTestId("queued-for-machine-note")).toHaveTextContent(
      "Queued — will send when your machine is back",
    );
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });

  it("says nothing under a message that was already answered", () => {
    renderPresenter([message("m1", MessageRole.USER), message("m2", MessageRole.ASSISTANT)]);
    expect(screen.queryByTestId("queued-for-machine-note")).toBeNull();
  });
});
