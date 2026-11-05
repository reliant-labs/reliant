/**
 * The mobile Chat sheet runs the SAME actions as the desktop ChatHeader menu
 * (useChatActions), so these assert the mutation each row fires — not only
 * that a row renders.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";
import type { WorkflowExecutionData } from "../../../types/chat";
import type { ThreadInfo } from "../../Chat/thread-views";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, params: _params, to, ...props }: { children?: React.ReactNode; params?: unknown; to: string }) => (
    <a href={to} {...props}>{children}</a>
  ),
}));

const mocks = vi.hoisted(() => ({
  rename: vi.fn(async () => {}),
  archive: vi.fn(async () => {}),
  unarchive: vi.fn(),
  compactChat: vi.fn(async () => ({ success: true, workflow_id: "", run_id: "", message: "" })),
  notify: vi.fn(),
  success: vi.fn(),
  resolveWorktree: vi.fn(async () => null as unknown),
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Fix flaky login test", worktreeId: "wt-1" } }),
  useRenameChat: () => ({ mutateAsync: mocks.rename }),
  useArchiveChat: () => ({ mutateAsync: mocks.archive }),
  useUnarchiveChat: () => ({ mutate: mocks.unarchive }),
}));
vi.mock("../../../hooks/message-queries", () => ({
  useMessages: () => ({ data: { messages: [] } }),
}));
vi.mock("../../../hooks/chat-actions", () => ({ compactChat: mocks.compactChat }));
vi.mock("../../../hooks/worktree-queries", () => ({
  resolveWorktree: mocks.resolveWorktree,
  getCachedMainWorktree: () => undefined,
}));
vi.mock("../../../hooks/worktree-actions", () => ({ loadMainWorktree: vi.fn() }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { notify: mocks.notify, success: mocks.success, error: vi.fn(), info: vi.fn() },
}));
// Its mutations need a query client; the sheet only places it.
vi.mock("../../Chat/WorkspaceRecoveryMenuItems", () => ({
  WorkspaceRecoveryMenuItems: () => <button type="button">Recreate workspace</button>,
}));

const { MobileChatSheet, workflowStatusLabel } = await import("../MobileChatSheet");

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

const THREADS = [
  thread({ id: "chat-1", name: "Main", isMain: true }),
  thread({ id: "t-review", name: "Reviewer" }),
  thread({ id: "t-spawn", name: "Spawned helper", isSpawn: true }),
];

function renderSheet(props: Partial<React.ComponentProps<typeof MobileChatSheet>> = {}) {
  const handlers = { onClose: vi.fn(), onSelectThread: vi.fn(), onOpenWorkspace: vi.fn() };
  render(
    <SurfaceProvider surface="mobile">
      <MobileChatSheet
        chatId="chat-1"
        title="Fix flaky login test"
        worktreeId="wt-1"
        threads={THREADS}
        hasMultipleThreads
        selectedThreadId={null}
        contextUsage={{ threadTokenCount: 142_000, compactionThreshold: 200_000 }}
        {...handlers}
        {...props}
      />
    </SurfaceProvider>,
  );
  return handlers;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("MobileChatSheet actions", () => {
  it("renames inline through the shared rename mutation", async () => {
    const user = userEvent.setup();
    renderSheet();
    await user.click(screen.getByRole("button", { name: "Rename chat" }));
    const input = screen.getByRole("textbox", { name: "Chat title" });
    await user.clear(input);
    await user.type(input, "  Fix the login test  {Enter}");
    expect(mocks.rename).toHaveBeenCalledWith({ chatId: "chat-1", title: "Fix the login test" });
    await waitFor(() => expect(screen.queryByRole("textbox", { name: "Chat title" })).not.toBeInTheDocument());
  });

  it("archives with an Undo toast", async () => {
    const user = userEvent.setup();
    const { onClose } = renderSheet();
    await user.click(screen.getByRole("button", { name: "Archive chat" }));
    expect(onClose).toHaveBeenCalled();
    expect(mocks.archive).toHaveBeenCalledWith("chat-1");
    await waitFor(() => expect(mocks.notify).toHaveBeenCalled());
    const [message, options] = mocks.notify.mock.calls[0] as unknown as [string, { action: { label: string; onClick: () => void } }];
    expect(message).toBe("Chat archived");
    options.action.onClick();
    expect(mocks.unarchive).toHaveBeenCalledWith("chat-1");
  });

  it("copies the chat id", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    renderSheet();
    await user.click(screen.getByRole("button", { name: "Copy chat ID" }));
    expect(writeText).toHaveBeenCalledWith("chat-1");
  });

  it("compacts the thread in view, main by default, like the composer's /compact", async () => {
    const user = userEvent.setup();
    renderSheet();
    expect(screen.getByText("142.0k / 200.0k · 71%")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Compact" }));
    expect(mocks.compactChat).toHaveBeenCalledWith("chat-1", "");
  });

  it("compacts a selected side thread", async () => {
    const user = userEvent.setup();
    renderSheet({ selectedThreadId: "t-review" });
    await user.click(screen.getByRole("button", { name: "Compact" }));
    expect(mocks.compactChat).toHaveBeenCalledWith("chat-1", "t-review");
  });

  it("has no Open Terminal: the mobile surface has no terminal", () => {
    renderSheet();
    expect(screen.queryByText(/terminal/i)).not.toBeInTheDocument();
  });

  it("opens the workspace sheet from Files, Git & Plan", async () => {
    const user = userEvent.setup();
    const { onClose, onOpenWorkspace } = renderSheet();
    await user.click(screen.getByRole("button", { name: /Files, Git & Plan/ }));
    expect(onClose).toHaveBeenCalled();
    expect(onOpenWorkspace).toHaveBeenCalled();
  });

  it("offers workspace recovery for a non-main workspace", async () => {
    mocks.resolveWorktree.mockResolvedValueOnce({ id: "wt-1", is_main: false, name: "feat", path: "/w" });
    renderSheet();
    expect(await screen.findByRole("button", { name: "Recreate workspace" })).toBeInTheDocument();
  });
});

describe("MobileChatSheet thread picker", () => {
  it("lists pickable threads, not spawned ones, and selects one", async () => {
    const user = userEvent.setup();
    const { onSelectThread, onClose } = renderSheet();
    await user.click(screen.getByRole("button", { name: /Thread/ }));
    const group = screen.getByRole("group", { name: "Threads" });
    expect(group).toHaveTextContent("All threads");
    expect(group).toHaveTextContent("Main");
    expect(group).not.toHaveTextContent("Spawned helper");
    await user.click(screen.getByRole("button", { name: "Reviewer" }));
    expect(onSelectThread).toHaveBeenCalledWith("t-review");
    expect(onClose).toHaveBeenCalled();
  });

  it("is absent when the chat has only its main thread", () => {
    renderSheet({ hasMultipleThreads: false });
    expect(screen.queryByRole("button", { name: /^Thread/ })).not.toBeInTheDocument();
  });
});

describe("MobileChatSheet workflow row", () => {
  const execution = (overrides: Partial<WorkflowExecutionData>) =>
    ({
      id: "wf-1",
      workflowName: "builtin://forge-one-shot",
      thread: "wf-1",
      state: WorkflowState.ACTIVE,
      stopReason: WorkflowStopReason.UNSPECIFIED,
      createdAt: "",
      messageCount: 0,
      children: [],
      steps: [],
      ...overrides,
    }) as WorkflowExecutionData;

  it("links to the workflow view with its live status", () => {
    renderSheet({ workflow: execution({}) });
    const link = screen.getByRole("link", { name: /Workflow: Forge One Shot/ });
    expect(link).toHaveAttribute("href", "/m/chats/$chatId/workflow");
    expect(link).toHaveTextContent("Running");
  });

  it("labels each workflow state", () => {
    expect(workflowStatusLabel({ state: WorkflowState.PENDING, stopReason: WorkflowStopReason.UNSPECIFIED })).toBe("Starting");
    expect(workflowStatusLabel({ state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED })).toBe("Completed");
    expect(workflowStatusLabel({ state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.FAILED })).toBe("Failed");
    expect(workflowStatusLabel({ state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.PAUSED })).toBe("Paused");
  });

  it("is absent without a workflow", () => {
    renderSheet({ workflow: null });
    expect(screen.queryByRole("link", { name: /Workflow/ })).not.toBeInTheDocument();
  });
});
