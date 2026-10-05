/**
 * Reopening a chat renders its cached transcript at once, then the stream
 * delivers what changed while it was closed. Without a cue, that catch-up
 * reads as the text silently jumping a few seconds later. The presenter shows
 * a small "Syncing…" status over the transcript for exactly that window.
 */

import { screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ChatPresenter } from "../ChatPresenter";
import type { Message } from "../../../api/client";

vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: () => <div data-testid="chat-input" />,
}));
vi.mock("../ChatThinkingIndicator", () => ({ ChatThinkingIndicator: () => null }));
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
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: () => <div data-testid="timeline" />,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => null,
}));

const cachedMessage = { id: "m1" } as Message;

function renderPresenter(props: { isChatSyncing?: boolean; messages?: Message[] }) {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={props.messages ?? [cachedMessage]}
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
        isChatSyncing={props.isChatSyncing}
      />
    </SurfaceProvider>,
  );
}

describe("ChatPresenter syncing indicator", () => {
  it("shows a status over the cached transcript while syncing", () => {
    renderPresenter({ isChatSyncing: true });

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Syncing…");
    // The transcript stays on screen underneath — this is not a loading state.
    expect(screen.getByTestId("timeline")).toBeInTheDocument();
  });

  it("does not obstruct the transcript", () => {
    renderPresenter({ isChatSyncing: true });

    expect(screen.getByTestId("chat-syncing-indicator")).toHaveClass(
      "pointer-events-none",
    );
  });

  it("is absent once synced", () => {
    renderPresenter({ isChatSyncing: false });

    expect(screen.queryByTestId("chat-syncing-indicator")).not.toBeInTheDocument();
  });

  it("is absent by default", () => {
    renderPresenter({});

    expect(screen.queryByTestId("chat-syncing-indicator")).not.toBeInTheDocument();
  });
});
