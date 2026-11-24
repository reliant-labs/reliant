/**
 * The chat's Trace view: the header's tab swaps the transcript and composer
 * for the trace panel, back again on "Chat", and a chat opened next starts
 * on its conversation. A viewer's chat hands the panel the viewer flag.
 */

import { screen, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ChatPresenter } from "../ChatPresenter";

// ChatPresenter pulls in the full chat stack (input, messages container,
// header, timeline, workflow viewer). This suite is about the surface gate
// around the workflow viewer, so everything else renders a marker and
// nothing heavier.
vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: () => <div data-testid="chat-input" />,
}));
vi.mock("../ChatThinkingIndicator", () => ({
  ChatThinkingIndicator: () => null,
}));
vi.mock("../ChatMessagesContainer", () => ({
  ChatMessagesContainer: ({ children }: { children?: React.ReactNode }) => (
    <div data-testid="messages-container">{children}</div>
  ),
}));
vi.mock("../ScrollToBottomButton", () => ({
  ScrollToBottomButton: () => null,
}));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => (
    <>{children}</>
  ),
}));
vi.mock("../PermissionsPanel", () => ({
  PermissionsPanel: () => null,
}));
vi.mock("../ChatHeader", () => ({
  ChatHeader: ({ view, onViewChange }: { view?: string; onViewChange?: (view: "chat" | "trace") => void }) => (
    <div data-testid="chat-header" data-view={view}>
      <button onClick={() => onViewChange?.("trace")}>Trace</button>
      <button onClick={() => onViewChange?.("chat")}>Chat</button>
    </div>
  ),
}));
vi.mock("../Trace/ChatTracePanel", () => ({
  ChatTracePanel: ({ chatId, viewerChat }: { chatId: string; viewerChat: boolean }) => (
    <div data-testid="chat-trace-panel" data-chat-id={chatId} data-viewer={String(viewerChat)} />
  ),
}));
vi.mock("../ResumeDaemonPill", () => ({
  ResumeDaemonPill: () => null,
}));
// It reads the chat's pending question over the network.
vi.mock("../BackgroundWorkPill", () => ({
  BackgroundWorkPill: () => null,
}));

// ChatPresenter reads the chat's pending questions to tell whether the thread
// in view is waiting on the user; none is pending here.
vi.mock("../../../hooks/question-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/question-queries")
  >("../../../hooks/question-queries");
  return { ...actual, usePendingQuestions: () => ({ data: { questions: [] } }) };
});
// The presenter reads the chat row: its viewer flag decides the trace's shape.
const chatRow = vi.hoisted(() => ({ current: null as Record<string, unknown> | null }));
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return { ...actual, useChat: (id?: string) => ({ data: chatRow.current && id ? { ...chatRow.current, id } : undefined }) };
});
vi.mock("../OomKillBanner", () => ({
  OomKillBanner: () => null,
}));
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../MachineHealthBanner", () => ({ MachineHealthBanner: () => null }));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: () => <div data-testid="timeline" />,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => <div data-testid="workflow-viewer-panel" />,
}));

const baseProps = {
  messages: [],
  approvals: [],
  errorEvents: [],
  infoEvents: [],
  runOutputs: [],
  chatId: "chat-1",
  isChatBusy: false,
  pendingApprovals: [],
  connectionStatus: "connected",
  worktreeId: null,
  projectId: "project-1",
  onSendMessage: vi.fn(async () => {}),
  onStopStreaming: vi.fn(async () => {}),
};

function renderPresenter(chatId = "chat-1") {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter {...baseProps} chatId={chatId} />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  chatRow.current = { title: "Deploy fix" };
});

describe("ChatPresenter Trace view", () => {
  it("opens on the conversation", () => {
    renderPresenter();
    expect(screen.getByTestId("chat-header")).toHaveAttribute("data-view", "chat");
    expect(screen.getByTestId("messages-container")).toBeInTheDocument();
    expect(screen.getByTestId("chat-input")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-trace-panel")).not.toBeInTheDocument();
  });

  it("swaps the transcript and composer for the trace, and back", () => {
    renderPresenter();
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(screen.getByTestId("chat-header")).toHaveAttribute("data-view", "trace");
    expect(screen.getByTestId("chat-trace-panel")).toHaveAttribute("data-chat-id", "chat-1");
    expect(screen.getByTestId("chat-trace-panel")).toHaveAttribute("data-viewer", "false");
    expect(screen.queryByTestId("messages-container")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-input")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Chat" }));
    expect(screen.queryByTestId("chat-trace-panel")).not.toBeInTheDocument();
    expect(screen.getByTestId("messages-container")).toBeInTheDocument();
  });

  it("hands a viewer's chat to the panel as a viewer's", () => {
    chatRow.current = { title: "Shared", viewerAccess: true };
    renderPresenter();
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(screen.getByTestId("chat-trace-panel")).toHaveAttribute("data-viewer", "true");
  });

  it("opens the next chat on its conversation", () => {
    const view = renderPresenter("chat-1");
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(screen.getByTestId("chat-trace-panel")).toBeInTheDocument();
    view.rerender(
      <SurfaceProvider surface="desktop">
        <ChatPresenter {...baseProps} chatId="chat-2" />
      </SurfaceProvider>,
    );
    expect(screen.queryByTestId("chat-trace-panel")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-header")).toHaveAttribute("data-view", "chat");
  });
});
