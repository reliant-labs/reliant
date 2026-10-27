/**
 * The single-chat mobile screen: the header back link, title, and — new here
 * — the workspace drill-in entry point. `ChatContainer` and
 * `MobileWorkspaceSheet` are mocked out; their own behavior is covered by
 * their own test files.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useParams: () => ({ chatId: "chat-1" }),
}));

let chatMock: { title?: string; worktreeId?: string; projectId?: string } | undefined = {
  title: "My chat",
  worktreeId: "wt-1",
};

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => chatMock,
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
