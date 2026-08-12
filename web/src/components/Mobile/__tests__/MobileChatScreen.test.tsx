/**
 * The single-chat mobile screen: the header back link, title, and — new here
 * — the workspace drill-in entry point. `ChatContainer` and
 * `MobileWorkspaceSheet` are mocked out; their own behavior is covered by
 * their own test files.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useParams: () => ({ chatId: "chat-1" }),
}));

let chatMock: { title?: string; worktreeId?: string } | undefined = {
  title: "My chat",
  worktreeId: "wt-1",
};

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => chatMock,
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "p1", path: "/repo" } }),
}));

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({
      worktrees: [{ id: "wt-main", is_main: true, name: "main", deleted_at: null }],
      loadWorktrees: vi.fn(),
    }),
}));

vi.mock("../../Chat/ChatContainer", () => ({
  ChatContainer: ({ tabId }: { tabId?: string }) => (
    <div data-testid="chat-container">{tabId}</div>
  ),
}));

// Mocked for the same reason as MobileWorkspaceSheet below: it reaches for a
// React Query client (useWorkflowExecutions) that this suite has no provider
// for, and its own behavior is covered in MobileWorkflowExecutionEntry.test.
vi.mock("../MobileWorkflowExecutionEntry", () => ({
  MobileWorkflowExecutionEntry: () => null,
}));

vi.mock("../MobileWorkspaceSheet", () => ({
  MobileWorkspaceSheet: ({ isOpen }: { isOpen: boolean }) =>
    isOpen ? <div data-testid="workspace-sheet" /> : null,
}));

const { MobileChatScreen } = await import("../MobileChatScreen");

describe("MobileChatScreen", () => {
  it("shows the chat title and container", () => {
    render(<MobileChatScreen />);
    expect(screen.getByText("My chat")).toBeInTheDocument();
    expect(screen.getByTestId("chat-container")).toHaveTextContent("chat-1");
  });

  it("shows a workspace button when the chat has a worktree", () => {
    render(<MobileChatScreen />);
    expect(screen.getByRole("button", { name: "Open workspace" })).toBeInTheDocument();
  });

  it("opens the workspace sheet when the button is pressed", async () => {
    const user = userEvent.setup();
    render(<MobileChatScreen />);

    expect(screen.queryByTestId("workspace-sheet")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open workspace" }));
    expect(screen.getByTestId("workspace-sheet")).toBeInTheDocument();
  });

  it("falls back to the main worktree when the chat has none", () => {
    chatMock = { title: "New chat", worktreeId: undefined };
    render(<MobileChatScreen />);
    expect(screen.getByRole("button", { name: "Open workspace" })).toBeInTheDocument();
    chatMock = { title: "My chat", worktreeId: "wt-1" };
  });
});
