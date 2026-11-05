/**
 * ChatPresenter under the mobile chat screen's floating chrome (§4.3): the
 * band from the status slot down to the composer floats over the transcript
 * as one glass panel the frame measures, and docks exactly as before
 * everywhere else.
 */

import { screen } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ChatPresenter } from "../ChatPresenter";
import { FloatingChromeFrame } from "../floatingChrome";

// The full chat stack is beside the point here: markers for everything but
// the layout this suite is about.
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
// The presenter reads the chat row; this suite is about the surface gate.
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return { ...actual, useChat: () => ({ data: undefined }) };
});
vi.mock("../OomKillBanner", () => ({
  OomKillBanner: () => null,
}));
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: () => <div data-testid="timeline" />,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({
  WorkflowViewerPanel: () => <div data-testid="workflow-viewer-panel" />,
}));


class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
const originalResizeObserver = globalThis.ResizeObserver;
const originalRect = HTMLElement.prototype.getBoundingClientRect;
beforeAll(() => {
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
  // The band is 132px tall: a one-line composer plus a status line.
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    const height = this.getAttribute("data-testid") === "floating-composer-band" ? 132 : 0;
    return { top: 0, bottom: height, height, left: 0, right: 0, width: 0, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
  };
});
afterAll(() => {
  globalThis.ResizeObserver = originalResizeObserver;
  HTMLElement.prototype.getBoundingClientRect = originalRect;
});

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
  hideHeader: true,
};

describe("ChatPresenter floating chrome", () => {
  it("floats the composer band over the transcript inside the frame", () => {
    const { container } = renderWithQuery(
      <SurfaceProvider surface="mobile">
        <FloatingChromeFrame>
          <ChatPresenter {...baseProps} />
        </FloatingChromeFrame>
      </SurfaceProvider>,
    );

    const band = screen.getByTestId("floating-composer-band");
    expect(band).toContainElement(screen.getByTestId("chat-input"));
    expect(band).toHaveClass("absolute", "inset-x-0", "bottom-0");
    // Glass, no hairline at rest; opaque with the hairline under reduced
    // transparency.
    expect(band).toHaveClass("bg-background/85", "backdrop-blur-xl", "border-t", "border-transparent");
    expect(band).toHaveClass("reduce-transparency:bg-background", "reduce-transparency:border-border/60");
    // The band, not the screen, keeps the composer clear of the home indicator.
    expect(band.style.paddingBottom).toBe("var(--mobile-safe-bottom, env(safe-area-inset-bottom))");
    // Measured and published as the transcript's bottom inset.
    const frame = container.querySelector<HTMLElement>("[data-floating-chrome]");
    expect(frame?.style.getPropertyValue("--chat-bottom-inset")).toBe("132px");
    // The transcript frame is no longer what the band sits under in flow: it
    // is the column's only flex child taking height.
    expect(band.parentElement).toBe(screen.getByTestId("chat-interface"));
  });

  it("docks the composer in the column outside a frame", () => {
    renderWithQuery(
      <SurfaceProvider surface="desktop">
        <ChatPresenter {...baseProps} />
      </SurfaceProvider>,
    );

    expect(screen.queryByTestId("floating-composer-band")).toBeNull();
    // `contents`: the composer is laid out as the column's own flex child.
    const wrapper = screen.getByTestId("chat-input").parentElement?.parentElement;
    expect(wrapper).toHaveClass("contents");
  });
});
