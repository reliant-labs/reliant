/**
 * ChatPresenter renders the SAME desktop-shaped chrome (workflow viewer
 * side/inline panel, its mouse-drag resize handles) regardless of surface
 * unless explicitly gated. The workflow viewer can't fit an iPhone viewport
 * even at its resize floor (300px on a 390px screen), and the resize handles
 * have no touch equivalent — so this suite pins that mobile/embed render
 * neither, desktop is untouched, and no settingsSync write happens off
 * desktop (settingsSync is keyed per-user, not per-surface, so a
 * mobile-triggered write would pollute the desktop default).
 */

import { screen, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ChatPresenter } from "../ChatPresenter";
import type { WorkflowExecution } from "../ExecutionSidebar/types";

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
  ChatHeader: ({
    onToggleWorkflowViewer,
    isWorkflowViewerOpen,
  }: {
    onToggleWorkflowViewer?: () => void;
    isWorkflowViewerOpen?: boolean;
  }) => (
    <div data-testid="chat-header">
      {onToggleWorkflowViewer && (
        <button
          onClick={onToggleWorkflowViewer}
          data-testid="toggle-workflow-viewer"
        >
          {isWorkflowViewerOpen ? "Hide" : "Show"} Workflow
        </button>
      )}
    </div>
  ),
}));
vi.mock("../ResumeDaemonPill", () => ({
  ResumeDaemonPill: () => null,
}));
vi.mock("../OomKillBanner", () => ({
  OomKillBanner: () => null,
}));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: () => <div data-testid="timeline" />,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => <div data-testid="workflow-viewer-panel" />,
}));

const { setSettingMock } = vi.hoisted(() => ({ setSettingMock: vi.fn() }));
vi.mock("../../../services/settingsSync", async () => {
  const actual = await vi.importActual<
    typeof import("../../../services/settingsSync")
  >("../../../services/settingsSync");
  return {
    ...actual,
    settingsSync: {
      ...actual.settingsSync,
      getSetting: vi.fn(() => "side"),
      setSetting: setSettingMock,
    },
  };
});

const workflowExecution: WorkflowExecution = {
  id: "wf-1",
  workflowName: "test-workflow",
  thread: "main",
  status: "running",
  createdAt: Date.now(),
  messageCount: 0,
  children: [],
  steps: [],
};

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
  workflowExecution,
};

// ChatPresenter reads a selected side thread through React Query
// (useThreadMessages), so it needs a provider like it has in the app.
function renderPresenter(surface: "desktop" | "mobile" | "embed") {
  return renderWithQuery(
    <SurfaceProvider surface={surface}>
      <ChatPresenter {...baseProps} />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  setSettingMock.mockClear();
});

describe("ChatPresenter workflow viewer chrome across surfaces", () => {
  it("exposes the workflow viewer toggle and panel on desktop", async () => {
    renderPresenter("desktop");

    const toggle = screen.getByTestId("toggle-workflow-viewer");
    expect(toggle).toBeInTheDocument();

    fireEvent.click(toggle);

    expect(await screen.findByTestId("workflow-viewer-panel")).toBeInTheDocument();
  });

  it("omits the workflow viewer toggle on mobile", () => {
    renderPresenter("mobile");

    expect(screen.queryByTestId("toggle-workflow-viewer")).not.toBeInTheDocument();
    expect(screen.queryByTestId("workflow-viewer-panel")).not.toBeInTheDocument();
  });

  it("omits the workflow viewer toggle in an embed", () => {
    renderPresenter("embed");

    expect(screen.queryByTestId("toggle-workflow-viewer")).not.toBeInTheDocument();
    expect(screen.queryByTestId("workflow-viewer-panel")).not.toBeInTheDocument();
  });

  it("never writes to settingsSync on mobile", () => {
    renderPresenter("mobile");

    expect(setSettingMock).not.toHaveBeenCalled();
  });

  it("never writes to settingsSync in an embed", () => {
    renderPresenter("embed");

    expect(setSettingMock).not.toHaveBeenCalled();
  });

  it("never writes to settingsSync on desktop either — ChatPresenter only reads the default", () => {
    renderPresenter("desktop");

    expect(setSettingMock).not.toHaveBeenCalled();
  });
});
