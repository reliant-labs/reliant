import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const newChatViewMounts = vi.fn();
const newChatViewProps = vi.fn();
vi.mock("../../Chat/NewChatView", async () => {
  const React = await import("react");
  return {
    NewChatView: (props: { composerPrefill?: { text: string } }) => {
      newChatViewProps(props);
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

/** The text the composer was last told to show. */
const composerPrefill = () => newChatViewProps.mock.lastCall?.[0]?.composerPrefill?.text;

describe("WorkflowEditorChatPanel", () => {
  it("renders the new-chat view when there is no chat param", () => {
    renderPanel(undefined);
    expect(screen.getByTestId("new-chat-view")).toBeTruthy();
    expect(screen.queryByTestId("chat-container")).toBeNull();
  });

  it("prefills the composer with a visible workflow reference", () => {
    useWorkspaceStateStore.getState().clearNewChatDraft("p1");
    renderPanel(undefined);
    expect(composerPrefill()).toBe(workflowReferencePrefill("swift-fox-a1b2"));
  });

  it("renders the chat container for the chat in the search param", () => {
    renderPanel("chat-123");
    expect(screen.getByTestId("chat-container").textContent).toBe("chat-123");
    expect(screen.queryByTestId("new-chat-view")).toBeNull();
  });

  it("waits for the workflow slug, then mounts the composer with the reference", () => {
    useWorkspaceStateStore.getState().clearNewChatDraft("p1");
    newChatViewMounts.mockClear();
    const { rerender } = render(panel(undefined, null));
    expect(screen.queryByTestId("new-chat-view")).toBeNull();
    expect(newChatViewMounts).not.toHaveBeenCalled();

    rerender(panel(undefined, "swift-fox-a1b2"));
    expect(screen.getByTestId("new-chat-view")).toBeTruthy();
    expect(newChatViewMounts).toHaveBeenCalledTimes(1);
    expect(composerPrefill()).toBe(workflowReferencePrefill("swift-fox-a1b2"));
  });

  it("re-mounts the composer with the new reference when the slug changes", () => {
    useWorkspaceStateStore.getState().clearNewChatDraft("p1");
    newChatViewMounts.mockClear();
    const { rerender } = render(panel(undefined, "first-a1"));
    // The composer saves what it shows as the draft.
    useWorkspaceStateStore.getState().setNewChatDraft("p1", workflowReferencePrefill("first-a1"));
    rerender(panel(undefined, "second-b2"));
    expect(newChatViewMounts).toHaveBeenCalledTimes(2);
    expect(composerPrefill()).toBe(workflowReferencePrefill("second-b2"));
    // The stale reference is not left behind for the app's new-chat screen.
    expect(useWorkspaceStateStore.getState().getNewChatDraft("p1")).toBe("");
  });

  it("keeps a draft the user typed", () => {
    useWorkspaceStateStore.getState().setNewChatDraft("p1", "my own words");
    newChatViewProps.mockClear();
    renderPanel(undefined);
    expect(composerPrefill()).toBeUndefined();
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
