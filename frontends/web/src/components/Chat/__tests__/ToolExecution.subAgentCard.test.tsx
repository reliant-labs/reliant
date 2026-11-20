import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ToolExecution } from "../ToolExecution";
import { WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";
import { ApprovalSchema, ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";

// The sub-agent card replaced the spawn's tool row. It must keep every
// control that row offered: a running sub-agent can be stopped (the row's
// Cancel), and a denied one still says why.

let mockAllWorkflows: Array<{ id: string; state: WorkflowState; stopReason: WorkflowStopReason; children: unknown[] }> = [];

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => undefined,
  useChatMessages: () => [],
  useStreamingMessages: () => [],
  useToolCallStates: () => new Map(),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/task-queries", () => ({
  useTasksForChat: () => ({ data: undefined }),
}));
vi.mock("../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));

afterEach(() => {
  cleanup();
  mockAllWorkflows = [];
});

const spawnCall = (id: string, childWorkflowId?: string) => ({
  id,
  name: "spawn",
  input: { title: "Write edge-case tests" },
  finished: true,
  ...(childWorkflowId ? { childWorkflowId } : {}),
});

describe("ToolExecution sub-agent card", () => {
  it("offers Cancel on a running sub-agent, and clicking it cancels that call", () => {
    mockAllWorkflows = [{ id: "wf-child-1", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] }];
    const onCancel = vi.fn();

    render(<ToolExecution toolCall={spawnCall("toolu_spawn_run", "wf-child-1")} chatId="chat-1" onCancel={onCancel} />);

    const card = screen.getByTestId("sub-agent-card");
    const cancel = within(card).getByRole("button", { name: "Cancel tool execution" });
    expect(cancel).toBeEnabled();

    fireEvent.click(cancel);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onCancel).toHaveBeenCalledWith("toolu_spawn_run");
  });

  it("keeps Cancel shown but disabled while the sub-agent is cancelling", () => {
    const onCancel = vi.fn();

    render(<ToolExecution toolCall={spawnCall("toolu_spawn_cancelling")} status="cancelling" chatId="chat-1" onCancel={onCancel} />);

    const card = screen.getByTestId("sub-agent-card");
    const cancel = within(card).getByRole("button", { name: "Cancel tool execution" });
    expect(cancel).toBeDisabled();
    fireEvent.click(cancel);
    expect(onCancel).not.toHaveBeenCalled();
    // The card names the state instead of reading as still working.
    expect(within(card).getByText("Stopping…")).toBeInTheDocument();
    expect(within(card).queryByText("Working")).not.toBeInTheDocument();
  });

  it("offers no Cancel once the sub-agent has finished", () => {
    mockAllWorkflows = [{ id: "wf-child-2", state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED, children: [] }];

    render(<ToolExecution toolCall={spawnCall("toolu_spawn_done", "wf-child-2")} chatId="chat-1" onCancel={vi.fn()} />);

    const card = screen.getByTestId("sub-agent-card");
    expect(within(card).queryByRole("button", { name: "Cancel tool execution" })).not.toBeInTheDocument();
  });

  it("offers no Cancel when the caller cannot cancel", () => {
    mockAllWorkflows = [{ id: "wf-child-3", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] }];

    render(<ToolExecution toolCall={spawnCall("toolu_spawn_ro", "wf-child-3")} chatId="chat-1" />);

    expect(screen.queryByRole("button", { name: "Cancel tool execution" })).not.toBeInTheDocument();
  });

  it("shows a denied sub-agent as denied, with the reason, not as working", () => {
    const denied = create(ApprovalSchema, {
      id: "approval-1",
      chatId: "chat-1",
      entityId: "block-1",
      status: ApprovalStatus.DENIED,
      denialReason: "Not now",
      createdAt: new Date().toISOString(),
    });

    render(<ToolExecution toolCall={spawnCall("toolu_spawn_denied")} approval={denied} chatId="chat-1" onCancel={vi.fn()} />);

    const card = screen.getByTestId("sub-agent-card");
    expect(within(card).getByText("Denied")).toBeInTheDocument();
    expect(within(card).queryByText("Working")).not.toBeInTheDocument();
    expect(within(card).getByText("Denied: Not now")).toBeInTheDocument();
    expect(within(card).queryByRole("button", { name: "Cancel tool execution" })).not.toBeInTheDocument();
  });
});
