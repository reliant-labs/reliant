/**
 * The single-chat mobile screen: its one top bar (title, status, back, the
 * workspace drill-in) and the thread selection it owns on ChatContainer's
 * behalf. `ChatContainer`, the Chat sheet and `MobileWorkspaceSheet` are
 * mocked out; their own behavior is covered by their own test files.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkflowState } from "../../../gen/reliant/v1/chat_pb";
import type { WorkflowExecutionData } from "../../../types/chat";
import type { ThreadInfo } from "../../Chat/thread-views";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useParams: () => ({ chatId: "chat-1" }),
}));

let chatMock: { title?: string; worktreeId?: string; projectId?: string; noMachine?: boolean } | undefined = {
  title: "My chat",
  worktreeId: "wt-1",
};

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => chatMock,
  useContextUsage: () => null,
}));

let executionMock: Partial<WorkflowExecutionData> | null = null;
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ data: executionMock, allWorkflows: [] }),
}));
vi.mock("../../../hooks/message-queries", () => ({
  useMessages: () => ({ data: { messages: [] } }),
}));

const thread = (overrides: Partial<ThreadInfo>): ThreadInfo => ({
  id: "t",
  name: "Thread",
  messageCount: 1,
  isMain: false,
  isActive: false,
  isSpawn: false,
  color: "#888",
  ...overrides,
});
let threadsMock: ThreadInfo[] = [];
vi.mock("../../Chat/thread-views", () => ({
  useThreads: () => threadsMock,
}));

const projectState = vi.hoisted(() => ({
  currentProject: { id: "p1", path: "/repo" } as { id: string; path: string } | null,
  // The project list as the cache holds it (getCachedProjects), and what a
  // fetch of it answers (fetchProjects).
  projects: [] as Array<{ id: string; path: string }>,
  fetchProjects: vi.fn(async (): Promise<Array<{ id: string; path: string }>> => projectState.projects),
  selectProject: vi.fn(async () => {}),
}));

vi.mock("../../../hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/project-queries")>()),
  getCachedProjects: () => projectState.projects,
  fetchProjects: () => projectState.fetchProjects(),
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector: (s: unknown) => unknown) => selector(projectState),
    { getState: () => projectState },
  ),
}));

vi.mock("../../../hooks/worktree-queries", () => ({
  useMainWorktree: () => ({ id: "wt-main", is_main: true, name: "main", deleted_at: null }),
}));
vi.mock("../../../hooks/worktree-actions", () => ({ loadMainWorktree: vi.fn() }));

// Stands in for the container: shows the thread it was told to display and
// lets a test pick one, the way the timeline's spawn rows do.
vi.mock("../../Chat/ChatContainer", () => ({
  ChatContainer: ({
    tabId,
    hideHeader,
    selectedThreadId,
    onSelectThread,
  }: {
    tabId?: string;
    hideHeader?: boolean;
    selectedThreadId?: string | null;
    onSelectThread?: (id: string | null) => void;
  }) => (
    <div data-testid="chat-container" data-hide-header={String(hideHeader)} data-thread={String(selectedThreadId)}>
      {tabId}
      <button type="button" onClick={() => onSelectThread?.("t-spawn")}>
        open spawn
      </button>
    </div>
  ),
}));

vi.mock("../MobileChatSheet", () => ({
  MobileChatSheet: () => <div data-testid="chat-sheet" />,
}));

vi.mock("../MobileWorkspaceSheet", () => ({
  MobileWorkspaceSheet: ({ isOpen }: { isOpen: boolean }) =>
    isOpen ? <div data-testid="workspace-sheet" /> : null,
}));

const { MobileChatScreen } = await import("../MobileChatScreen");

describe("MobileChatScreen", () => {
  it("shows the chat title and a container with no header row of its own", () => {
    render(<MobileChatScreen />);
    expect(screen.getByText("My chat")).toBeInTheDocument();
    expect(screen.getByTestId("chat-container")).toHaveTextContent("chat-1");
    expect(screen.getByTestId("chat-container")).toHaveAttribute("data-hide-header", "true");
  });

  it("opens the Chat sheet from the title", async () => {
    render(<MobileChatScreen />);
    expect(screen.queryByTestId("chat-sheet")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /chat actions/ }));
    expect(screen.getByTestId("chat-sheet")).toBeInTheDocument();
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

describe("MobileChatScreen status line", () => {
  afterEach(() => {
    executionMock = null;
  });

  it("shows a workflow only while it runs", () => {
    executionMock = { workflowName: "builtin://forge-one-shot", state: WorkflowState.ACTIVE };
    const { unmount } = render(<MobileChatScreen />);
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("Running · Forge One Shot");
    unmount();

    executionMock = { workflowName: "builtin://forge-one-shot", state: WorkflowState.STOPPED };
    render(<MobileChatScreen />);
    expect(screen.queryByTestId("mobile-chat-status")).not.toBeInTheDocument();
  });

  it("says when the chat has no machine", () => {
    chatMock = { title: "My chat", noMachine: true };
    render(<MobileChatScreen />);
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("No machine");
    chatMock = { title: "My chat", worktreeId: "wt-1" };
  });
});

describe("MobileChatScreen thread back", () => {
  afterEach(() => {
    threadsMock = [];
  });

  it("walks back through the parent thread before leaving the chat", async () => {
    const user = userEvent.setup();
    threadsMock = [
      thread({ id: "chat-1", name: "Main", isMain: true }),
      thread({ id: "t-review", name: "Reviewer", parentThread: "chat-1" }),
      thread({ id: "t-spawn", name: "Helper", isSpawn: true, parentThread: "t-review" }),
    ];
    render(<MobileChatScreen />);
    const container = screen.getByTestId("chat-container");
    expect(screen.getByLabelText("Back to chats")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "open spawn" }));
    expect(container).toHaveAttribute("data-thread", "t-spawn");
    expect(screen.getByTestId("mobile-chat-status")).toHaveTextContent("Thread: Helper");

    await user.click(screen.getByRole("button", { name: "Back to Reviewer" }));
    expect(container).toHaveAttribute("data-thread", "t-review");

    // Reviewer's parent is main, so back now returns to the default view.
    await user.click(screen.getByRole("button", { name: "Back to main thread" }));
    expect(container).toHaveAttribute("data-thread", "null");
    expect(screen.getByLabelText("Back to chats")).toBeInTheDocument();
  });
});

describe("MobileChatScreen project alignment", () => {
  const p1 = { id: "p1", path: "/repo" };
  const p2 = { id: "p2", path: "/other" };

  beforeEach(() => {
    projectState.currentProject = p1;
    projectState.projects = [p1, p2];
    projectState.fetchProjects.mockClear();
    projectState.selectProject.mockClear();
    chatMock = { title: "My chat", worktreeId: "wt-1", projectId: "p1" };
  });

  it("switches to the chat's own project when it differs from the current one", async () => {
    chatMock = { title: "My chat", worktreeId: "wt-1", projectId: "p2" };
    render(<MobileChatScreen />);
    await waitFor(() => expect(projectState.selectProject).toHaveBeenCalledWith(p2, { restoreWorkspace: false, restoreChat: false }));
    expect(projectState.selectProject).toHaveBeenCalledTimes(1);
  });

  it("loads projects first when the chat's project is not known yet", async () => {
    chatMock = { title: "My chat", worktreeId: "wt-1", projectId: "p2" };
    projectState.projects = [p1];
    projectState.fetchProjects.mockImplementationOnce(async () => [p1, p2]);
    render(<MobileChatScreen />);
    await waitFor(() => expect(projectState.selectProject).toHaveBeenCalledWith(p2, { restoreWorkspace: false, restoreChat: false }));
    expect(projectState.fetchProjects).toHaveBeenCalledTimes(1);
  });

  it("leaves the project alone when the chat is already in it", () => {
    render(<MobileChatScreen />);
    expect(projectState.selectProject).not.toHaveBeenCalled();
  });

  it("does not select anything for a chat whose project the user cannot see", async () => {
    chatMock = { title: "My chat", worktreeId: "wt-1", projectId: "gone" };
    render(<MobileChatScreen />);
    await waitFor(() => expect(projectState.fetchProjects).toHaveBeenCalled());
    expect(projectState.selectProject).not.toHaveBeenCalled();
  });
});
