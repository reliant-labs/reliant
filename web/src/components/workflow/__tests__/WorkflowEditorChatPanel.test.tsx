import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const newChatViewMounts = vi.fn();
vi.mock("../../Chat/NewChatView", async () => {
  const React = await import("react");
  return {
    NewChatView: () => {
      React.useEffect(() => {
        newChatViewMounts();
      }, []);
      return <div data-testid="new-chat-view" />;
    },
  };
});
vi.mock("../../Chat/ChatContainer", () => ({
  ChatContainer: ({ tabId }: { tabId: string }) => <div data-testid="chat-container">{tabId}</div>,
}));
vi.mock("../../runs/RunRouteLoader", () => ({
  useRunRoute: (chatId: string) => ({ status: "ready", chat: { id: chatId } }),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) => selector({ currentProject: { id: "p1" } }),
}));

import { WorkflowEditorChatPanel, workflowReferencePrefill } from "../WorkflowEditorChatPanel";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

function panel(chatId?: string, workflowSlug: string | null = "swift-fox-a1b2", isStreamYielded = false) {
  return (
    <WorkflowEditorChatPanel
      workflowSlug={workflowSlug ?? undefined}
      isStreamYielded={isStreamYielded}
      chatId={chatId}
      onChatIdChange={vi.fn()}
      isOpen
      onOpenChange={vi.fn()}
      panelSize="normal"
      onPanelSizeChange={vi.fn()}
    />
  );
}

function renderPanel(chatId?: string) {
  return render(panel(chatId));
}

describe("WorkflowEditorChatPanel", () => {
  it("renders the new-chat view when there is no chat param", () => {
    renderPanel(undefined);
    expect(screen.getByTestId("new-chat-view")).toBeTruthy();
    expect(screen.queryByTestId("chat-container")).toBeNull();
  });

  it("prefills the composer draft with a visible workflow reference", () => {
    useWorkspaceStateStore.getState().clearNewChatDraft("p1");
    renderPanel(undefined);
    expect(useWorkspaceStateStore.getState().getNewChatDraft("p1")).toBe(
      workflowReferencePrefill("swift-fox-a1b2"),
    );
  });

  it("renders the chat container for the chat in the search param", () => {
    renderPanel("chat-123");
    expect(screen.getByTestId("chat-container").textContent).toBe("chat-123");
    expect(screen.queryByTestId("new-chat-view")).toBeNull();
  });

  it("waits for the workflow slug, then mounts the composer with the reference in the draft", () => {
    useWorkspaceStateStore.getState().clearNewChatDraft("p1");
    newChatViewMounts.mockClear();
    const { rerender } = render(panel(undefined, null));
    expect(screen.queryByTestId("new-chat-view")).toBeNull();
    expect(newChatViewMounts).not.toHaveBeenCalled();

    rerender(panel(undefined, "swift-fox-a1b2"));
    expect(screen.getByTestId("new-chat-view")).toBeTruthy();
    expect(newChatViewMounts).toHaveBeenCalledTimes(1);
    expect(useWorkspaceStateStore.getState().getNewChatDraft("p1")).toBe(
      workflowReferencePrefill("swift-fox-a1b2"),
    );
  });

  it("re-mounts the composer when the slug changes so it re-reads the draft", () => {
    useWorkspaceStateStore.getState().clearNewChatDraft("p1");
    newChatViewMounts.mockClear();
    const { rerender } = render(panel(undefined, "first-a1"));
    rerender(panel(undefined, "second-b2"));
    expect(newChatViewMounts).toHaveBeenCalledTimes(2);
    expect(useWorkspaceStateStore.getState().getNewChatDraft("p1")).toBe(workflowReferencePrefill("second-b2"));
  });

  it("keeps a draft the user typed", () => {
    useWorkspaceStateStore.getState().setNewChatDraft("p1", "my own words");
    renderPanel(undefined);
    expect(useWorkspaceStateStore.getState().getNewChatDraft("p1")).toBe("my own words");
  });

  it("shows a stub instead of the chat while a test run holds the stream, and remounts it after", () => {
    const { rerender } = render(panel("chat-123", "swift-fox-a1b2", true));
    expect(screen.queryByTestId("chat-container")).toBeNull();
    expect(screen.getByText(/Chat paused while the test run streams/)).toBeTruthy();

    rerender(panel("chat-123", "swift-fox-a1b2", false));
    expect(screen.getByTestId("chat-container").textContent).toBe("chat-123");
    expect(screen.queryByText(/Chat paused/)).toBeNull();
  });
});
