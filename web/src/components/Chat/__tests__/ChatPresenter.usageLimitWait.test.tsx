/**
 * A run held until a provider's usage limit resets (server-side timer; it
 * retries by itself). The transcript footer says who it waits on, when it
 * carries on, with a live countdown, and offers Retry now and Stop. The reset
 * time rides on the chat; the activity says whether it applies.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatActivity, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { useActivityStore } from "../../../store/activityStore";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import type { Message } from "../../../api/client";
import { ChatPresenter } from "../ChatPresenter";

const state = vi.hoisted(() => ({
  chat: undefined as Record<string, unknown> | undefined,
  retry: vi.fn(async (_chatId: string) => true),
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
}));
vi.mock("../../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../api/client")>();
  return { ...actual, api: { ...actual.api, chatsV2: { ...actual.api.chatsV2, retryUsageLimitWait: state.retry } } };
});
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
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: ({ footer }: { footer?: React.ReactNode }) => <div data-testid="timeline">{footer}</div>,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({ WorkflowViewerPanel: () => null }));

function message(id: string, role: MessageRole): Message {
  return { id, role, content: id, thread: "chat-1" } as unknown as Message;
}

const onStop = vi.fn(async () => {});

function renderPresenter() {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={[message("m1", MessageRole.ASSISTANT), message("m2", MessageRole.USER)]}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        // WAITING_FOR_USAGE_LIMIT is not "running".
        isChatBusy={false}
        pendingApprovals={[]}
        connectionStatus="connected"
        projectId="project-1"
        onSendMessage={vi.fn(async () => {})}
        onStopStreaming={onStop}
      />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  state.retry.mockClear();
  onStop.mockClear();
  state.chat = {
    id: "chat-1",
    workflowId: "chat-1",
    activity: ChatActivity.RUNNING,
    launchKind: "chat.start",
    usageLimitResumesAt: new Date(Date.now() + 72 * 60_000).toISOString(),
    usageLimitProvider: "Anthropic",
    usageLimitProbing: false,
  };
  useActivityStore.getState().setActivity("chat-1", ChatActivity.WAITING_FOR_USAGE_LIMIT);
});

describe("ChatPresenter: a run held for a usage limit", () => {
  it("says who it waits on and when it carries on, instead of thinking", () => {
    renderPresenter();
    const note = screen.getByTestId("usage-limit-wait-note");
    expect(note).toHaveTextContent("Waiting for Anthropic usage limit");
    expect(screen.getByTestId("usage-limit-resume-line")).toHaveTextContent(/resumes .+ \(in 1h 1[12]m\)/);
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });

  it("Retry now wakes the server's timer for this chat", async () => {
    renderPresenter();
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }));
    await waitFor(() => expect(state.retry).toHaveBeenCalledWith("chat-1"));
  });

  it("Stop is the run's ordinary stop", () => {
    renderPresenter();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("does not promise a reset the provider never named", () => {
    state.chat = { ...state.chat, usageLimitProbing: true, usageLimitProvider: "" };
    renderPresenter();
    expect(screen.getByTestId("usage-limit-wait-note")).toHaveTextContent("Waiting for usage limit");
    expect(screen.getByTestId("usage-limit-resume-line")).toHaveTextContent("no reset time given");
  });

  it("says nothing once the run is running again", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
    renderPresenter();
    expect(screen.queryByTestId("usage-limit-wait-note")).toBeNull();
  });

  it("shows no stale countdown when the activity moved on but the chat snapshot did not", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
    renderPresenter();
    expect(screen.queryByTestId("usage-limit-wait-note")).toBeNull();
  });
});
